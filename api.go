package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type request struct {
	Action    string    `json:"action"`
	ID        string    `json:"id,omitempty"`
	Command   string    `json:"command,omitempty"`
	Name      string    `json:"name,omitempty"`
	Group     string    `json:"group,omitempty"`
	External  *External `json:"external,omitempty"`
	Confirmed bool      `json:"confirmed,omitempty"`
	Agent     string    `json:"agent,omitempty"`
}
type response struct {
	Version  string   `json:"version"`
	Snapshot Snapshot `json:"snapshot"`
	Text     string   `json:"text,omitempty"`
	ID       string   `json:"id,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func call(p paths, r request) (response, error) {
	r.Agent = os.Getenv("SSH_AUTH_SOCK")
	b, err := json.Marshal(r)
	if err != nil {
		return response{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 35 * time.Second}
	res, err := client.Post("http://local/rpc", "application/json", bytes.NewReader(b))
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	var out response
	err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&out)
	if err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}

func ensureDaemon(p paths) error {
	if res, err := call(p, request{Action: "list"}); err == nil {
		if res.Version == version {
			return nil
		}
		for _, r := range res.Snapshot.Managed {
			if r.PID != 0 || r.Wanted {
				return fmt.Errorf("后台仍为旧版本且有托管连接，请先使用旧面板停止托管连接，再重新打开；外部 SSH 不受影响")
			}
		}
		// Replacing an idle backend cannot interrupt external SSH processes.
		if _, err = call(p, request{Action: "daemon-stop"}); err != nil {
			return err
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if _, err = os.Stat(p.Socket); os.IsNotExist(err) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if _, err = os.Stat(p.Socket); !os.IsNotExist(err) {
			return errors.New("旧后台尚未退出，请稍后重新打开")
		}
	}
	if err := p.prepare(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.Logs, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Env = append(withoutEnv(os.Environ(), "TUNNELS_HOME"), "TUNNELS_HOME="+p.Root)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if _, err = call(p, request{Action: "list"}); err == nil {
			return nil
		}
	}
	return fmt.Errorf("后台启动失败，请查看 %s", filepath.Join(p.Logs, "daemon.log"))
}

func serve(p paths) error {
	if err := requireTools(); err != nil {
		return err
	}
	if err := p.prepare(); err != nil {
		return err
	}
	lock, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("后台已经运行")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	m, err := newManager(p)
	if err != nil {
		return err
	}
	defer m.shutdown()
	_ = os.Remove(p.Socket)
	l, err := net.Listen("unix", p.Socket)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(p.Socket)
	if err = os.Chmod(p.Socket, 0600); err != nil {
		return err
	}
	stop := make(chan struct{})
	var once sync.Once
	var mutations sync.Mutex // serialize actions; background worker updates use m.mu
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var req request
		dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if req.Agent != "" {
			if st, err := os.Stat(req.Agent); err == nil && st.Mode()&os.ModeSocket != 0 {
				m.mu.Lock()
				m.agent = req.Agent
				m.mu.Unlock()
			}
		}
		out := response{}
		if req.Action == "list" {
			out.Snapshot = m.snapshot()
		} else {
			mutations.Lock()
			out = m.handle(req)
			mutations.Unlock()
		}
		if req.Action == "daemon-stop" {
			once.Do(func() { close(stop) })
		}
		out.Version = version
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 10 * time.Second}
	for id, e := range m.entries {
		if e.r.Wanted {
			_ = m.startIfWanted(id)
		}
	}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(l) }()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	select {
	case <-stop:
	case <-sigs:
	case err = <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func (m *manager) handle(req request) response {
	out := response{}
	var err error
	switch req.Action {
	case "reload":
		err = m.reload()
	case "add":
		var d Definition
		d, err = importCommand(req.Command, req.Name, req.Group)
		if err == nil {
			err = m.add(d)
			out.ID = d.ID
		}
	case "start", "stop", "restart":
		err = m.setWanted(req.ID, req.Action != "stop", req.Action == "restart")
	case "logs":
		m.mu.Lock()
		_, ok := m.entries[req.ID]
		m.mu.Unlock()
		if !ok {
			err = errors.New("连接不存在")
			break
		}
		var b []byte
		b, err = os.ReadFile(filepath.Join(m.p.Logs, req.ID+".log"))
		if os.IsNotExist(err) {
			err = nil
			out.Text = "暂无日志"
		} else if err == nil {
			if len(b) > 20000 {
				b = b[len(b)-20000:]
			}
			out.Text = string(b)
		}
	case "delete":
		m.mu.Lock()
		e, ok := m.entries[req.ID]
		if !ok {
			err = errors.New("连接不存在")
		} else if e.cancel != nil || e.r.Wanted {
			err = errors.New("请先停止连接，再删除配置")
		} else {
			delete(m.entries, req.ID)
			err = m.persistLocked()
			if err != nil {
				m.entries[req.ID] = e
			}
		}
		m.mu.Unlock()
	case "import", "stop-external":
		if !req.Confirmed || req.External == nil {
			err = errors.New("操作需要确认整个 SSH 进程的影响范围")
			break
		}
		var args []string
		args, err = verifyIdentity(req.External.ProcessIdentity)
		if err != nil {
			break
		}
		var parsed parsedSSH
		parsed, err = parseSSH(args)
		if err != nil {
			break
		}
		if !parsed.PureTunnel || len(parsed.Unsupported) > 0 || len(parsed.Forwards) == 0 {
			err = errors.New("此进程无法安全自动接管：仅支持参数完整的 -N -L 隧道")
			break
		}
		// Rebuild from live argv; never trust the client's cached mapping.
		e := External{ProcessIdentity: req.External.ProcessIdentity, Forwards: parsed.Forwards, Args: parsed.Args, Destination: parsed.Destination}
		var d Definition
		if req.Action == "import" {
			name := req.Name
			if name == "" {
				name = parsed.Destination
			}
			d = Definition{ID: newID(), Name: name, Group: req.Group, Destination: parsed.Destination, Args: parsed.Args, Forwards: parsed.Forwards, AutoReconnect: true}
			err = m.add(d)
			if err != nil {
				break
			}
			out.ID = d.ID
			// Prove unattended authentication before interrupting a working tunnel.
			if err = m.checkAuthentication(d); err != nil {
				err = fmt.Errorf("原隧道保持运行；托管认证预检失败: %w", err)
				break
			}
		}
		err = stopExternal(e)
		if err == nil && req.Action == "import" {
			err = m.setWanted(d.ID, true, false)
		}
		_ = m.reload()
	case "daemon-stop":
	default:
		err = errors.New("未知操作")
	}
	if err != nil {
		out.Error = err.Error()
	}
	out.Snapshot = m.snapshot()
	sort.Slice(out.Snapshot.Managed, func(i, j int) bool { return out.Snapshot.Managed[i].Name < out.Snapshot.Managed[j].Name })
	return out
}

func logLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = clean(lines[i])
	}
	return lines
}
