package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func toolPath(name string) (string, error) {
	if runtime.GOOS == "darwin" {
		paths := map[string]string{"ssh": "/usr/bin/ssh", "ps": "/bin/ps", "lsof": "/usr/sbin/lsof"}
		if path := paths[name]; path != "" {
			if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode().Perm()&0111 != 0 {
				return path, nil
			}
		}
	}
	return exec.LookPath(name)
}

func hostCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	path, err := toolPath(name)
	if err != nil {
		return exec.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, path, args...)
}

func requireTools() error {
	var missing []string
	for _, name := range []string{"ssh", "ps", "lsof"} {
		if _, err := toolPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少依赖 %s；Ubuntu/Debian/WSL 请先运行 sudo apt-get install openssh-client procps lsof", strings.Join(missing, ", "))
	}
	return nil
}

// This check deliberately does not create directories or contact an SSH host.
func doctor(p paths) error {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
		platform += " (WSL)"
	}
	fmt.Printf("tunnels %s · %s\n状态目录: %s\n", version, platform, p.Root)
	var failures []string
	for _, name := range []string{"ssh", "ps", "lsof"} {
		path, err := toolPath(name)
		if err != nil {
			failures = append(failures, name)
			fmt.Printf("[缺少] %s\n", name)
		} else {
			fmt.Printf("[可用] %s: %s\n", name, path)
		}
	}
	if len(failures) > 0 {
		return requireTools()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if b, err := processCommand(ctx, "-p", fmt.Sprint(os.Getpid()), "-o", "uid=,lstart=").Output(); err != nil || len(strings.Fields(string(b))) != 6 {
		return errors.New("ps 进程信息不兼容；Linux 请安装 procps（不使用 BusyBox ps）")
	}
	if args, err := processArgs(os.Getpid()); err != nil || len(args) == 0 {
		return fmt.Errorf("无法读取本机进程参数: %v", err)
	}
	fmt.Println("[可用] 进程信息读取\n检查完成；未连接远端服务器。")
	return nil
}
