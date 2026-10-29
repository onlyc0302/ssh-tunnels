package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#176B87", Dark: "#8BD5CA"})
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#667085", Dark: "#8B94A7"})
	selectStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#17324D", Dark: "#E6EDF3"}).Background(lipgloss.AdaptiveColor{Light: "#E5EFF7", Dark: "#243345"})
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B54708", Dark: "#E5B567"})
)

type row struct {
	managed  *Runtime
	external *External
}

func (r row) key() string {
	if r.managed != nil {
		return r.managed.ID
	}
	return fmt.Sprint("pid:", r.external.PID)
}

type tickMsg time.Time
type resultMsg struct {
	action string
	res    response
	err    error
}
type ui struct {
	p                       paths
	s                       Snapshot
	rows                    []row
	selected, width, height int
	mode                    string
	fields                  [3]string
	field                   int
	query, message, details string
	pending                 request
	busy, polling           bool
	scroll                  int
	scanBaseline            map[string]bool
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func rpc(p paths, r request) tea.Cmd {
	return func() tea.Msg { res, err := call(p, r); return resultMsg{r.Action, res, err} }
}
func (m ui) Init() tea.Cmd { return tea.Batch(rpc(m.p, request{Action: "reload"}), tick()) }

func (m *ui) rebuild() {
	key := ""
	if len(m.rows) > 0 && m.selected < len(m.rows) {
		key = m.rows[m.selected].key()
	}
	m.rows = nil
	sort.Slice(m.s.Managed, func(i, j int) bool {
		a, b := m.s.Managed[i], m.s.Managed[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	q := strings.ToLower(m.query)
	for i := range m.s.Managed {
		r := &m.s.Managed[i]
		if q == "" || strings.Contains(strings.ToLower(r.Name+" "+r.Group+" "+r.Destination+" "+formatForwards(r.Forwards)), q) {
			m.rows = append(m.rows, row{managed: r})
		}
	}
	for i := range m.s.External {
		e := &m.s.External[i]
		if q == "" || strings.Contains(strings.ToLower(e.Destination+" "+formatExternal(*e)), q) {
			m.rows = append(m.rows, row{external: e})
		}
	}
	for i, r := range m.rows {
		if r.key() == key {
			m.selected = i
			return
		}
	}
	if m.selected >= len(m.rows) {
		m.selected = len(m.rows) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m ui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		if m.polling {
			return m, tick()
		}
		m.polling = true
		return m, tea.Batch(tick(), rpc(m.p, request{Action: "list"}))
	case resultMsg:
		if msg.action == "list" {
			m.polling = false
		} else {
			m.busy = false
		}
		if msg.err != nil {
			m.message = "错误: " + clean(msg.err.Error())
			return m, nil
		}
		old := m.scanBaseline
		if msg.action == "reload" {
			added, removed := 0, 0
			now := map[string]bool{}
			for _, e := range msg.res.Snapshot.External {
				k := fmt.Sprint(e.PID, ":", e.Start)
				now[k] = true
				if !old[k] {
					added++
				}
			}
			for k := range old {
				if !now[k] {
					removed++
				}
			}
			m.message = fmt.Sprintf("扫描完成：新增 %d，移除 %d；共 %d 个外部 SSH 进程", added, removed, len(now))
		} else if msg.action == "logs" {
			m.mode = "details"
			m.details = "最近日志（↑↓ 翻阅，Esc 返回）\n\n" + strings.Join(logLines(msg.res.Text), "\n")
			m.scroll = 0
		} else if msg.action != "list" {
			m.message = "操作已提交"
			if msg.action == "add" {
				m.message = "配置已保存，按空格启动"
			}
		}
		m.s = msg.res.Snapshot
		m.rebuild()
	case tea.KeyMsg:
		k := msg.String()
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		if m.mode == "details" {
			switch k {
			case "esc", "q", "enter":
				m.mode = ""
			case "down", "j":
				m.scroll++
			case "up", "k":
				if m.scroll > 0 {
					m.scroll--
				}
			case "pgdown":
				m.scroll += 10
			case "pgup":
				m.scroll = max(0, m.scroll-10)
			}
			return m, nil
		}
		if m.mode == "confirm" {
			if k == "esc" || k == "n" {
				m.mode = ""
				return m, nil
			}
			if k == "y" || k == "Y" {
				m.mode = ""
				m.busy = true
				return m, rpc(m.p, m.pending)
			}
			return m, nil
		}
		if m.mode == "add" || m.mode == "search" {
			if k == "esc" {
				if m.mode == "search" {
					m.query = ""
					m.rebuild()
				}
				m.mode = ""
				return m, nil
			}
			if k == "enter" {
				if m.mode == "search" {
					m.mode = ""
					return m, nil
				}
				if _, err := importCommand(m.fields[2], m.fields[0], m.fields[1]); err != nil {
					m.message = "错误: " + clean(err.Error())
					return m, nil
				}
				m.mode = ""
				m.busy = true
				return m, rpc(m.p, request{Action: "add", Name: m.fields[0], Group: m.fields[1], Command: m.fields[2]})
			}
			if m.mode == "add" {
				if k == "tab" {
					m.field = (m.field + 1) % 3
					return m, nil
				}
				if k == "shift+tab" {
					m.field = (m.field + 2) % 3
					return m, nil
				}
			}
			value := &m.query
			if m.mode == "add" {
				value = &m.fields[m.field]
			}
			if k == "backspace" || k == "ctrl+h" {
				if len(*value) > 0 {
					_, size := utf8.DecodeLastRuneInString(*value)
					*value = (*value)[:len(*value)-size]
				}
			} else if k == "ctrl+u" {
				*value = ""
			} else if msg.Type == tea.KeyRunes {
				if len(*value) < 8192 {
					*value += string(msg.Runes)
				}
			} else if k == " " {
				*value += " "
			}
			if m.mode == "search" {
				m.rebuild()
			}
			return m, nil
		}
		if k == "q" {
			return m, tea.Quit
		}
		if k == "down" || k == "j" {
			m.selected = min(m.selected+1, len(m.rows)-1)
			if m.selected < 0 {
				m.selected = 0
			}
			return m, nil
		}
		if k == "up" || k == "k" {
			m.selected = max(0, m.selected-1)
			return m, nil
		}
		if k == "/" {
			m.mode = "search"
			return m, nil
		}
		if k == "?" {
			m.mode = "details"
			m.scroll = 0
			m.details = "快捷键\n\nR / Shift+r    重新扫描外部 SSH 隧道\nr              重启选中的托管连接\n空格           启动 / 停止\na              新建连接（粘贴 SSH 命令）\ni              外部连接纳入托管\nl              查看托管日志\nd              删除已停止的配置\n/              搜索；Esc 清除搜索\nEnter          完整详情\nq              退出面板，保留后台连接\n\n外部连接仅在打开面板或按 R 时重新扫描。\n托管连接的运行状态与自动重连由后台维护。"
			return m, nil
		}
		if k == "esc" && m.query != "" {
			m.query = ""
			m.rebuild()
			return m, nil
		}
		if m.busy {
			m.message = "正在处理，请稍候"
			return m, nil
		}
		if k == "R" {
			m.busy = true
			m.message = "扫描中…"
			m.scanBaseline = map[string]bool{}
			for _, e := range m.s.External {
				m.scanBaseline[fmt.Sprint(e.PID, ":", e.Start)] = true
			}
			return m, rpc(m.p, request{Action: "reload"})
		}
		if k == "a" {
			m.mode = "add"
			m.fields = [3]string{}
			m.field = 0
			m.message = ""
			return m, nil
		}
		if len(m.rows) == 0 {
			return m, nil
		}
		r := m.rows[m.selected]
		if k == "enter" {
			m.details = details(r)
			m.scroll = 0
			m.mode = "details"
			return m, nil
		}
		if r.managed != nil {
			id := r.managed.ID
			action := ""
			switch k {
			case " ":
				action = "start"
				if r.managed.Wanted && r.managed.State != "需要处理" {
					action = "stop"
				}
			case "r":
				action = "restart"
			case "l":
				action = "logs"
			case "d":
				m.pending = request{Action: "delete", ID: id}
				m.mode = "confirm"
				m.details = "删除配置「" + clean(r.managed.Name) + "」？仅允许删除已停止的连接。"
				return m, nil
			}
			if action != "" {
				m.busy = true
				return m, rpc(m.p, request{Action: action, ID: id})
			}
		} else {
			e := r.external
			if k == "i" || k == " " {
				if !e.Importable {
					m.message = "仅可查看: " + clean(e.Reason)
					return m, nil
				}
				action := "import"
				verb := "保存配置并重建为托管连接"
				if k == " " {
					action = "stop-external"
					verb = "停止整个外部 SSH 进程"
				}
				m.pending = request{Action: action, External: e, Confirmed: true}
				m.mode = "confirm"
				m.details = fmt.Sprintf("%s（PID %d）？\n\n%s\n\n将断开该进程的全部映射及其可能共享的会话。\n已有数据库连接需要重新连接。\n托管需密钥/agent；接管前先检查认证，失败会保留原连接。", verb, e.PID, formatExternal(*e))
				return m, nil
			}
			if k == "r" {
				m.message = "外部连接须先按 i 纳入托管，才能重启"
			}
			if k == "l" {
				m.message = "外部进程的历史日志不可获取"
			}
		}
	}
	return m, nil
}

func crop(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	out := ""
	for _, r := range s {
		if lipgloss.Width(out+string(r))+1 > w {
			break
		}
		out += string(r)
	}
	return out + "…"
}
func tailCrop(s string, w int) string {
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[1:]
	}
	if string(r) != s {
		return "…" + string(r)
	}
	return s
}
func formatForwards(fs []Forward) string {
	parts := []string{}
	for _, f := range fs {
		parts = append(parts, f.Local()+" → "+f.Target())
	}
	return strings.Join(parts, "; ")
}
func formatExternal(e External) string {
	if len(e.Forwards) > 0 {
		return formatForwards(e.Forwards)
	}
	return strings.Join(e.Listeners, ", ") + " → 目标待确认"
}
func externalStatus(e External) string {
	if len(e.Listeners) > 0 {
		return "监听中"
	}
	return "未发现监听"
}
func details(r row) string {
	if r.managed != nil {
		v := r.managed
		return fmt.Sprintf("%s\n\nID: %s\n分组: %s\nSSH: %s\n状态: %s\n期望运行: %t\nPID: %d\n本次启动: %s\n自动重连: %t\n累计重试: %d\n\n%s\n\n最近错误: %s\n\n服务状态: 未检测（SSH 已建立不代表数据库可用）", v.Name, v.ID, v.Group, v.Destination, v.State, v.Wanted, v.PID, v.Started.Format(time.RFC3339), v.AutoReconnect, v.Retries, strings.ReplaceAll(formatForwards(v.Forwards), "; ", "\n"), v.LastError)
	}
	e := r.external
	return fmt.Sprintf("外部 SSH 进程\n\nPID: %d\n启动时间: %s\nSSH: %s\n状态: %s\n\n%s\n\n实际监听: %s\n允许接管: %t\n说明: %s\n\n外部状态仅在按 R 时更新；原进程的历史日志不可获取。", e.PID, e.Start, e.Destination, externalStatus(*e), strings.ReplaceAll(formatExternal(*e), "; ", "\n"), strings.Join(e.Listeners, ", "), e.Importable, e.Reason)
}

func runUI(p paths) error {
	_, err := tea.NewProgram(ui{p: p, width: 100, height: 30, busy: true, message: "扫描中…"}, tea.WithAltScreen()).Run()
	return err
}
