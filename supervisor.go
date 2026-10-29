package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type entry struct {
	r        Runtime
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool
}

type manager struct {
	mu        sync.Mutex
	p         paths
	entries   map[string]*entry
	external  []External
	scanned   time.Time
	scanError string
	agent     string
	closing   bool
}

func newManager(p paths) (*manager, error) {
	ds, err := loadDefinitions(p)
	if err != nil {
		return nil, err
	}
	m := &manager{p: p, entries: map[string]*entry{}, external: []External{}, agent: os.Getenv("SSH_AUTH_SOCK")}
	for _, d := range ds {
		m.entries[d.ID] = &entry{r: Runtime{Definition: d, State: "已停止"}}
	}
	// A daemon crash may leave its dedicated ssh child alive. Verify, stop, then rebuild.
	for _, e := range m.entries {
		b, err := os.ReadFile(m.pidPath(e.r.ID))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var identity ProcessIdentity
		if json.Unmarshal(b, &identity) != nil {
			return nil, errors.New("托管进程记录损坏，请检查 " + m.pidPath(e.r.ID))
		}
		args, err := verifyIdentity(identity)
		if err == nil {
			found := false
			for i, a := range args {
				if a == "-S" && i+1 < len(args) && args[i+1] == m.controlPath(e.r.ID) {
					found = true
				}
			}
			if !found {
				return nil, errors.New("托管进程控制路径不匹配，拒绝停止")
			}
			if err = syscall.Kill(identity.PID, syscall.SIGTERM); err != nil {
				return nil, err
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err = verifyIdentity(identity); err != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err == nil {
				return nil, errors.New("旧托管进程尚未退出，拒绝重复启动")
			}
		}
		_ = os.Remove(m.pidPath(e.r.ID))
	}
	return m, nil
}

func (m *manager) controlPath(id string) string { return filepath.Join(m.p.Root, id+".sock") }
func (m *manager) pidPath(id string) string     { return filepath.Join(m.p.Root, id+".pid.json") }

func (m *manager) persistLocked() error {
	ds := []Definition{}
	for _, e := range m.entries {
		ds = append(ds, e.r.Definition)
	}
	return saveDefinitions(m.p, ds)
}

func (m *manager) snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{Managed: []Runtime{}, External: append([]External{}, m.external...), ScannedAt: m.scanned, ScanError: m.scanError, AgentAvailable: m.agent != ""}
	for _, e := range m.entries {
		s.Managed = append(s.Managed, e.r)
	}
	return s
}

func (m *manager) reload() error {
	m.mu.Lock()
	owned := map[int]bool{}
	for _, e := range m.entries {
		if e.r.PID > 0 {
			owned[e.r.PID] = true
		}
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	es, err := discover(ctx, owned)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.scanError = err.Error()
		return err
	}
	// A managed process may have started while ps/lsof was running.
	for _, e := range m.entries {
		if e.r.PID > 0 {
			owned[e.r.PID] = true
		}
	}
	m.external = nil
	for _, e := range es {
		if !owned[e.PID] {
			m.external = append(m.external, e)
		}
	}
	m.scanned = time.Now()
	m.scanError = ""
	return nil
}

func (m *manager) add(d Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[d.ID]; ok {
		return errors.New("连接已存在")
	}
	m.entries[d.ID] = &entry{r: Runtime{Definition: d, State: "已停止"}}
	if err := m.persistLocked(); err != nil {
		delete(m.entries, d.ID)
		return err
	}
	return nil
}

func (m *manager) setWanted(id string, wanted, restart bool) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return errors.New("连接不存在")
	}
	if m.closing {
		m.mu.Unlock()
		return errors.New("后台正在退出")
	}
	previous := e.r.Wanted
	e.r.Wanted = wanted
	if err := m.persistLocked(); err != nil {
		e.r.Wanted = previous
		m.mu.Unlock()
		return err
	}
	if e.cancel != nil {
		if !wanted || restart || e.stopping || e.r.State == "需要处理" {
			e.stopping = true
			e.cancel()
			e.r.State = "停止中"
			done := e.done
			m.mu.Unlock()
			if wanted {
				go func() { <-done; _ = m.startIfWanted(id) }()
			}
			return nil
		}
		m.mu.Unlock()
		return nil
	}
	if !wanted {
		e.r.State = "已停止"
		e.r.RetryAt = time.Time{}
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	return m.startIfWanted(id)
}

func (m *manager) startIfWanted(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil || !e.r.Wanted || m.closing || e.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	e.r.LastError = ""
	e.r.Retries = 0
	e.r.State = "连接中"
	go m.supervise(ctx, e)
	return nil
}

func (m *manager) change(e *entry, fn func(*Runtime)) { m.mu.Lock(); defer m.mu.Unlock(); fn(&e.r) }

func (m *manager) supervise(ctx context.Context, e *entry) {
	defer func() {
		m.mu.Lock()
		e.cancel = nil
		e.stopping = false
		e.r.PID = 0
		e.r.RetryAt = time.Time{}
		if ctx.Err() != nil {
			e.r.State = "已停止"
		}
		close(e.done)
		m.mu.Unlock()
	}()
	m.mu.Lock()
	d := e.r.Definition
	m.mu.Unlock()
	log := &rotatingLog{path: filepath.Join(m.p.Logs, d.ID+".log")}
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		m.change(e, func(r *Runtime) { r.State = "连接中"; r.RetryAt = time.Time{} })
		started := time.Now()
		err := m.attempt(ctx, e, d, log)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("SSH 连接已退出")
		}
		reason := clean(err.Error())
		fmt.Fprintf(log, "%s %s\n", time.Now().Format(time.RFC3339), reason)
		if permanentFailure(reason) || !d.AutoReconnect {
			m.change(e, func(r *Runtime) { r.State = "需要处理"; r.LastError = reason; r.PID = 0 })
			return
		}
		if time.Since(started) > time.Minute {
			failures = 0
		}
		delay := retryDelay(failures)
		failures++
		m.change(e, func(r *Runtime) {
			r.State = "等待重连"
			r.LastError = reason
			r.PID = 0
			r.Retries++
			r.RetryAt = time.Now().Add(delay)
		})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func retryDelay(n int) time.Duration {
	if n > 5 {
		n = 5
	}
	d := time.Duration(1<<n) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d + time.Duration(rand.IntN(500))*time.Millisecond
}

func permanentFailure(s string) bool {
	s = strings.ToLower(s)
	for _, v := range []string{"permission denied", "host key verification failed", "remote host identification has changed", "bad configuration", "bad owner or permissions", "unsupported option", "too many authentication failures", "invalid format", "address already in use", "cannot listen to port", "private key file", "unprotected private key", "no such identity", "转发未就绪"} {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

type tailWriter struct {
	mu   sync.Mutex
	text string
	log  *rotatingLog
}

func (w *tailWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.text += string(b)
	if len(w.text) > 4096 {
		w.text = w.text[len(w.text)-4096:]
	}
	w.mu.Unlock()
	return w.log.Write(b)
}
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.text)
}

func sshArgs(d Definition, socket string) []string {
	a := []string{"-N", "-T", "-M", "-S", socket,
		"-o", "ControlPersist=no", "-o", "ForkAfterAuthentication=no", "-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1", "-o", "PermitLocalCommand=no",
		"-o", "StrictHostKeyChecking=yes", "-o", "SessionType=none"}
	a = append(a, d.Args...)
	for _, f := range d.Forwards {
		a = append(a, "-L", f.Spec())
	}
	return append(a, d.Destination)
}

func preflight(ds []Forward) error {
	for _, f := range ds {
		addr := f.Local()
		if f.Bind == "*" {
			addr = net.JoinHostPort("", fmt.Sprint(f.Port))
		}
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("本地端口不可用 %s: %w", f.Local(), err)
		}
		l.Close()
	}
	return nil
}

func (m *manager) attempt(ctx context.Context, e *entry, d Definition, log *rotatingLog) error {
	if err := preflight(d.Forwards); err != nil {
		return err
	}
	socket := m.controlPath(d.ID)
	_ = os.Remove(socket)
	cmd := hostCommand(ctx, "ssh", sshArgs(d, socket)...)
	m.mu.Lock()
	agent := m.agent
	m.mu.Unlock()
	cmd.Env = append(withoutEnv(os.Environ(), "SSH_AUTH_SOCK"), "SSH_AUTH_SOCK="+agent, "LC_ALL=C")
	stderr := &tailWriter{log: log}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	m.change(e, func(r *Runtime) { r.PID = cmd.Process.Pid; r.Started = time.Now() })
	// Write an identity record before acknowledging a healthy managed connection.
	identity, _, identityErr := identify(cmd.Process.Pid)
	if identityErr == nil {
		b, _ := json.Marshal(identity)
		identityErr = os.WriteFile(m.pidPath(d.ID), b, 0600)
	}
	if identityErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if s := stderr.String(); s != "" {
			return errors.New(s)
		}
		return fmt.Errorf("无法记录托管进程: %w", identityErr)
	}
	defer os.Remove(m.pidPath(d.ID))
	defer os.Remove(socket)
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	ready := false
	readyDeadline := time.Now().Add(25 * time.Second)
	for {
		select {
		case err := <-finished:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if s := stderr.String(); s != "" {
				return errors.New(s)
			}
			return err
		case <-ticker.C:
			if ready {
				continue
			}
			if time.Now().After(readyDeadline) {
				_ = cmd.Process.Kill()
				<-finished
				return errors.New("转发未就绪：25 秒内未确认控制连接与全部监听端口，请检查 SSH 配置和日志")
			}
			if _, err := os.Stat(socket); err != nil {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			check := hostCommand(probeCtx, "ssh", "-F", "/dev/null", "-S", socket, "-O", "check", d.Destination)
			err := check.Run()
			cancel()
			if err != nil {
				continue
			}
			probeCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
			ports, err := listeningPorts(probeCtx, []int{cmd.Process.Pid})
			cancel()
			if err != nil || !allListening(d.Forwards, ports[cmd.Process.Pid]) {
				continue
			}
			ready = true
			m.change(e, func(r *Runtime) { r.State = "已建立"; r.LastError = "" })
		}
	}
}

func allListening(fs []Forward, listeners []string) bool {
	for _, f := range fs {
		found := false
		for _, l := range listeners {
			host, port, err := net.SplitHostPort(l)
			if err != nil {
				continue
			}
			if port != fmt.Sprint(f.Port) {
				continue
			}
			if f.Bind == host || f.Bind == "localhost" && (host == "127.0.0.1" || host == "::1") || (f.Bind == "*" || f.Bind == "0.0.0.0" || f.Bind == "::") && host == "*" {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func withoutEnv(env []string, key string) []string {
	out := []string{}
	for _, v := range env {
		if !strings.HasPrefix(v, key+"=") {
			out = append(out, v)
		}
	}
	return out
}

// No remote command or forward is created. The connection is closed as soon as
// the dedicated control socket confirms successful noninteractive authentication.
func (m *manager) checkAuthentication(d Definition) error {
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Second)
	defer cancel()
	socket := m.controlPath(d.ID)
	probe := d
	probe.Forwards = nil
	args := sshArgs(probe, socket)
	args = append(args[:len(args)-1], "-o", "ClearAllForwardings=yes", d.Destination)
	cmd := hostCommand(ctx, "ssh", args...)
	m.mu.Lock()
	agent := m.agent
	m.mu.Unlock()
	cmd.Env = append(withoutEnv(os.Environ(), "SSH_AUTH_SOCK"), "SSH_AUTH_SOCK="+agent, "LC_ALL=C")
	stderr := &tailWriter{log: &rotatingLog{path: filepath.Join(m.p.Logs, d.ID+".log")}}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer os.Remove(socket)
	defer os.Remove(m.pidPath(d.ID))
	if identity, _, err := identify(cmd.Process.Pid); err == nil {
		b, _ := json.Marshal(identity)
		if err = os.WriteFile(m.pidPath(d.ID), b, 0600); err != nil {
			cancel()
			<-done
			return err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if s := stderr.String(); s != "" {
				return errors.New(s)
			}
			if err == nil {
				return errors.New("认证连接提前退出")
			}
			return err
		case <-ticker.C:
			if _, err := os.Stat(socket); err != nil {
				continue
			}
			checkCtx, checkCancel := context.WithTimeout(ctx, time.Second)
			err := hostCommand(checkCtx, "ssh", "-F", "/dev/null", "-S", socket, "-O", "check", d.Destination).Run()
			checkCancel()
			if err == nil {
				cancel()
				<-done
				return nil
			}
		}
	}
}

func (m *manager) shutdown() {
	m.mu.Lock()
	m.closing = true
	var done []chan struct{}
	for _, e := range m.entries {
		if e.cancel != nil {
			e.cancel()
			done = append(done, e.done)
		}
	}
	m.mu.Unlock()
	for _, d := range done {
		<-d
	}
}
