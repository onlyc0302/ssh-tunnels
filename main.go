package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

const version = "0.2.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "help" || args[0] == "-h") {
		fmt.Print(help)
		return nil
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("tunnels", version)
		return nil
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "daemon" {
		return serve(p)
	}
	if len(args) > 0 && args[0] == "paths" {
		fmt.Println("配置:", p.Config, "\n日志:", p.Logs, "\n通信:", p.Socket)
		return nil
	}
	if len(args) > 0 && args[0] == "doctor" {
		return doctor(p)
	}
	if err = requireTools(); err != nil {
		return err
	}
	if err = ensureDaemon(p); err != nil {
		return err
	}
	if len(args) == 0 {
		return runUI(p)
	}
	req := request{Action: args[0]}
	switch args[0] {
	case "list", "reload":
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		name := fs.String("name", "", "连接名称")
		group := fs.String("group", "", "分组")
		command := fs.String("command", "", "单行 ssh 命令")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if *command == "" {
			return fmt.Errorf("请使用 --command 'ssh ...'")
		}
		req.Name = *name
		req.Group = *group
		req.Command = *command
	case "start", "stop", "restart", "logs", "delete":
		if len(args) != 2 {
			return fmt.Errorf("用法: tunnels %s <ID 或唯一名称>", args[0])
		}
		res, err := call(p, request{Action: "list"})
		if err != nil {
			return err
		}
		matches := 0
		for _, r := range res.Snapshot.Managed {
			if r.ID == args[1] || r.Name == args[1] {
				req.ID = r.ID
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("找不到唯一连接 %q，请用 list 查看 ID", args[1])
		}
	case "daemon-stop":
	default:
		return fmt.Errorf("未知命令 %q，使用 tunnels help 查看帮助", args[0])
	}
	res, err := call(p, req)
	if err != nil {
		return err
	}
	if req.Action == "logs" {
		fmt.Println(strings.Join(logLines(res.Text), "\n"))
		return nil
	}
	if req.Action == "add" {
		fmt.Println("已保存（尚未启动）:", res.ID)
		return nil
	}
	if len(args) > 1 && args[1] == "--json" {
		b, _ := json.MarshalIndent(res.Snapshot, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if req.Action == "list" || req.Action == "reload" {
		printSnapshot(res.Snapshot)
	} else {
		fmt.Println("操作完成")
	}
	return nil
}

func printSnapshot(s Snapshot) {
	fmt.Println("来源\tID/PID\t名称\t状态\t映射")
	for _, r := range s.Managed {
		fmt.Printf("托管\t%s\t%s\t%s\t%s\n", r.ID, clean(r.Name), r.State, formatForwards(r.Forwards))
	}
	for _, e := range s.External {
		fmt.Printf("外部\t%d\t%s\t%s\t%s\n", e.PID, clean(e.Destination), externalStatus(e), formatExternal(e))
	}
	if !s.ScannedAt.IsZero() {
		fmt.Println("上次扫描:", s.ScannedAt.Format("15:04:05"))
	}
	if s.ScanError != "" {
		fmt.Println("扫描失败:", clean(s.ScanError))
	}
}

const help = `tunnels — kitty SSH 隧道管理器

  tunnels                       打开终端面板，首次扫描外部 SSH 隧道
  tunnels reload [--json]        手动重新扫描外部隧道
  tunnels list [--json]          查看当前快照（不触发扫描）
  tunnels add --name NAME --group GROUP --command 'ssh -N -L ... host'
  tunnels start|stop|restart ID  操作托管连接（也支持唯一名称）
  tunnels logs ID               查看最近日志
  tunnels delete ID             删除已经停止的配置
  tunnels paths                 显示配置与日志位置
  tunnels doctor                只读检查平台、依赖与进程读取能力
  tunnels daemon-stop           停止后台及托管连接；下次启动恢复期望状态
  tunnels version

快捷键: R 重新扫描，r 重启，空格 启停，a 添加，i 接管，l 日志，Enter 详情
        d 删除已停止配置，/ 搜索，q 退出面板但保留后台连接

首版只托管 TCP -L；无人值守连接使用 SSH 密钥/agent。
密码/二次验证连接会显示需要处理；首次主机指纹须先用普通 ssh 确认。
TUNNELS_HOME 可指定独立状态目录。后台自动启动，不自动安装登录启动项。
`
