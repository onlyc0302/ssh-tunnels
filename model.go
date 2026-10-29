package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Forward struct {
	Bind       string `json:"bind"`
	Port       int    `json:"port"`
	Host       string `json:"host"`
	TargetPort int    `json:"target_port"`
}

func (f Forward) Local() string  { return net.JoinHostPort(f.Bind, strconv.Itoa(f.Port)) }
func (f Forward) Target() string { return net.JoinHostPort(f.Host, strconv.Itoa(f.TargetPort)) }
func (f Forward) Spec() string   { return f.Local() + ":" + f.Target() }
func (f Forward) Validate() error {
	if f.Port < 1 || f.Port > 65535 || f.TargetPort < 1 || f.TargetPort > 65535 {
		return errors.New("端口必须在 1–65535 之间")
	}
	if f.Bind != "localhost" && f.Bind != "*" && net.ParseIP(f.Bind) == nil {
		return errors.New("监听地址须为 IP、localhost 或 *")
	}
	if f.Host == "" || strings.ContainsAny(f.Host, " \t\r\n") || clean(f.Host) != f.Host {
		return errors.New("目标地址无效")
	}
	return nil
}

type Definition struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Group         string    `json:"group,omitempty"`
	Destination   string    `json:"destination"`
	Args          []string  `json:"args,omitempty"`
	Forwards      []Forward `json:"forwards"`
	Wanted        bool      `json:"wanted"`
	AutoReconnect bool      `json:"auto_reconnect"`
}

func (d Definition) Validate() error {
	if len(d.ID) != 16 {
		return errors.New("无效连接 ID")
	}
	if _, err := hex.DecodeString(d.ID); err != nil {
		return errors.New("无效连接 ID")
	}
	if strings.TrimSpace(d.Name) == "" || strings.ContainsAny(d.Name, "\r\n\x1b") {
		return errors.New("连接名称不能为空或包含控制字符")
	}
	if clean(d.Name) != d.Name || clean(d.Group) != d.Group {
		return errors.New("名称或分组包含控制字符")
	}
	if d.Destination == "" || strings.HasPrefix(d.Destination, "-") || strings.ContainsAny(d.Destination, " \t\r\n") || clean(d.Destination) != d.Destination {
		return errors.New("SSH 主机无效")
	}
	if len(d.Forwards) == 0 {
		return errors.New("至少需要一个 -L 映射")
	}
	for _, f := range d.Forwards {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return validateTransportArgs(d.Args)
}

type ProcessIdentity struct {
	PID     int    `json:"pid"`
	Start   string `json:"start"`
	ArgHash string `json:"arg_hash"`
}

type External struct {
	ProcessIdentity
	Destination string    `json:"destination"`
	Forwards    []Forward `json:"forwards"`
	Listeners   []string  `json:"listeners"`
	Args        []string  `json:"args,omitempty"`
	Importable  bool      `json:"importable"`
	Reason      string    `json:"reason,omitempty"`
}

type Runtime struct {
	Definition
	State     string    `json:"state"`
	PID       int       `json:"pid,omitempty"`
	Started   time.Time `json:"started,omitempty"`
	RetryAt   time.Time `json:"retry_at,omitempty"`
	Retries   int       `json:"retries"`
	LastError string    `json:"last_error,omitempty"`
}

type Snapshot struct {
	Managed        []Runtime  `json:"managed"`
	External       []External `json:"external"`
	ScannedAt      time.Time  `json:"scanned_at"`
	ScanError      string     `json:"scan_error,omitempty"`
	AgentAvailable bool       `json:"agent_available"`
}

type paths struct{ Root, Socket, Config, Lock, Logs string }

func statePaths() (paths, error) {
	root := os.Getenv("TUNNELS_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return paths{}, err
		}
		root = defaultStateRoot(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"))
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return paths{}, err
	}
	// Keep the conservative macOS limit so config and control socket paths are portable.
	socket := filepath.Join(root, "api.sock")
	if len(filepath.Join(root, "0123456789abcdef.sock")) >= 104 {
		return paths{}, errors.New("状态目录路径太长，请用 TUNNELS_HOME 指定短于 82 字节的本机目录")
	}
	return paths{root, socket, filepath.Join(root, "config.json"), filepath.Join(root, "daemon.lock"), filepath.Join(root, "logs")}, nil
}

func defaultStateRoot(goos, home, xdgConfig string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "kitty-tunnels")
	}
	if !filepath.IsAbs(xdgConfig) {
		xdgConfig = filepath.Join(home, ".config")
	}
	return filepath.Join(xdgConfig, "kitty-tunnels")
}

func (p paths) prepare() error {
	for _, dir := range []string{p.Root, p.Logs} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func loadDefinitions(p paths) ([]Definition, error) {
	b, err := os.ReadFile(p.Config)
	if os.IsNotExist(err) {
		return []Definition{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ds []Definition
	if err = json.Unmarshal(b, &ds); err != nil {
		return nil, fmt.Errorf("配置解析失败，原文件保持不变: %w", err)
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if err = d.Validate(); err != nil {
			return nil, err
		}
		if seen[d.ID] {
			return nil, errors.New("配置包含重复 ID")
		}
		seen[d.ID] = true
	}
	return ds, nil
}

func saveDefinitions(p paths, ds []Definition) error {
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Root, ".config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, p.Config)
}

// Log files stay bounded; callers never write passwords or command transcripts.
type rotatingLog struct {
	mu   sync.Mutex
	path string
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := os.Stat(l.path); err == nil && st.Size() > 512*1024 {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Write(b)
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || (r >= 127 && r < 160) {
			return ' '
		}
		return r
	}, s)
}
