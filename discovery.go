package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func identify(pid int) (ProcessIdentity, []string, error) {
	args, err := processArgs(pid)
	if err != nil {
		return ProcessIdentity{}, nil, err
	}
	if len(args) == 0 || filepath.Base(args[0]) != "ssh" {
		return ProcessIdentity{}, nil, errors.New("目标不是 SSH 进程")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := processCommand(ctx, "-p", strconv.Itoa(pid), "-o", "uid=,lstart=")
	b, err := cmd.Output()
	if err != nil {
		return ProcessIdentity{}, nil, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 6 || fields[0] != strconv.Itoa(os.Getuid()) {
		return ProcessIdentity{}, nil, errors.New("进程不属于当前用户")
	}
	h := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return ProcessIdentity{pid, strings.Join(fields[1:], " "), hex.EncodeToString(h[:])}, args, nil
}

// ps localizes lstart using the launching terminal's locale. The identity must
// remain stable across kitty, CLI clients, and daemon restarts.
func processCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := hostCommand(ctx, "ps", args...)
	cmd.Env = append(withoutEnv(withoutEnv(os.Environ(), "LC_ALL"), "LANG"), "LC_ALL=C", "LANG=C")
	return cmd
}

func verifyIdentity(want ProcessIdentity) ([]string, error) {
	got, args, err := identify(want.PID)
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, errors.New("进程身份已变化，请按 R 重新扫描")
	}
	return args, nil
}

func listeningPorts(ctx context.Context, pids []int) (map[int][]string, error) {
	out := map[int][]string{}
	if len(pids) == 0 {
		return out, nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	cmd := hostCommand(ctx, "lsof", "-nP", "-a", "-p", strings.Join(ids, ","), "-iTCP", "-sTCP:LISTEN", "-Fpn")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.TrimSpace(stderr.String()) != "" {
			return nil, fmt.Errorf("无法读取监听端口: %w %s", err, clean(stderr.String()))
		}
	}
	pid := 0
	for _, line := range strings.Split(string(b), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 {
				out[pid] = append(out[pid], line[1:])
			}
		}
	}
	return out, nil
}

func discover(ctx context.Context, owned map[int]bool) ([]External, error) {
	b, err := processCommand(ctx, "-ww", "-axo", "pid=,uid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var candidates []int
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != strconv.Itoa(os.Getuid()) || filepath.Base(fields[2]) != "ssh" {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		if !owned[pid] {
			candidates = append(candidates, pid)
		}
	}
	ports, err := listeningPorts(ctx, candidates)
	if err != nil {
		return nil, err
	}
	result := []External{}
	for _, pid := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		identity, args, err := identify(pid)
		if err != nil {
			if len(ports[pid]) > 0 {
				result = append(result, unreadableExternal(pid, ports[pid], err))
			}
			continue
		}
		p, err := parseSSH(args)
		if err != nil {
			if len(ports[pid]) > 0 {
				result = append(result, External{ProcessIdentity: identity, Listeners: ports[pid], Reason: "命令参数无法还原"})
			}
			continue
		}
		if len(p.Forwards) == 0 && len(ports[pid]) == 0 {
			continue
		}
		e := External{ProcessIdentity: identity, Destination: p.Destination, Args: p.Args, Forwards: p.Forwards, Listeners: ports[pid]}
		e.Importable = p.PureTunnel && len(p.Unsupported) == 0 && len(p.Forwards) > 0
		if !p.PureTunnel {
			e.Reason = "可能承载交互会话：仅查看"
		} else if len(p.Unsupported) > 0 {
			e.Reason = "需人工检查: " + strings.Join(p.Unsupported, ", ")
		} else if len(p.Forwards) == 0 {
			e.Reason = "目标映射未在启动参数中出现"
		}
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })
	return result, nil
}

func unreadableExternal(pid int, listeners []string, err error) External {
	return External{
		ProcessIdentity: ProcessIdentity{PID: pid},
		Listeners:       listeners,
		Reason:          "无法完整读取进程信息，仅供查看: " + clean(err.Error()),
	}
}

// Only signal a process whose identity was checked immediately beforehand.
func stopExternal(e External) error {
	args, err := verifyIdentity(e.ProcessIdentity)
	if err != nil {
		return err
	}
	p, err := parseSSH(args)
	if err != nil {
		return err
	}
	if !p.PureTunnel || len(p.Unsupported) > 0 {
		return errors.New("该进程可能承载其他会话或不支持的转发，初版仅允许查看")
	}
	if err = syscall.Kill(e.PID, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = verifyIdentity(e.ProcessIdentity); err != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("进程未在 3 秒内退出，未强制终止；请重新扫描")
}
