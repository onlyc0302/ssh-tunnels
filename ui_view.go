package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type tableRow struct{ name, source, local, target, host, status string }

func presentRow(r row) tableRow {
	var v tableRow
	var forwards []Forward
	if r.managed != nil {
		m := r.managed
		v = tableRow{name: m.Name, source: "托管", host: m.Destination, status: m.State}
		forwards = m.Forwards
	} else {
		e := r.external
		v = tableRow{name: e.Destination, source: "外部", host: e.Destination, status: externalStatus(*e)}
		if v.name == "" {
			v.name = fmt.Sprintf("SSH #%d", e.PID)
			v.host = "待确认"
		}
		forwards = e.Forwards
		if len(forwards) == 0 {
			v.local = strings.Join(e.Listeners, ", ")
			v.target = "待确认"
		}
	}
	if len(forwards) > 0 {
		v.local, v.target = forwards[0].Local(), forwards[0].Target()
		if len(forwards) > 1 {
			v.name += fmt.Sprintf(" (+%d)", len(forwards)-1)
		}
	}
	return v
}

func cell(s string, width int) string {
	s = crop(clean(s), width)
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}

func tableLine(v tableRow, width int) string {
	if width < 84 {
		localW := min(21, max(10, width/3))
		statusW := 10
		nameW := max(1, width-localW-statusW-6)
		return "  " + cell(v.name, nameW) + "  " + cell(v.local, localW) + "  " + cell(v.status, statusW)
	}
	nameW := width - 68
	if width >= 116 {
		nameW = width - 94
		return "  " + cell(v.name, nameW) + "  " + cell(v.source, 6) + "  " + cell(v.local, 21) + "  " + cell(v.target, 21) + "  " + cell(v.host, 24) + "  " + cell(v.status, 10)
	}
	return "  " + cell(v.name, nameW) + "  " + cell(v.source, 6) + "  " + cell(v.local, 21) + "  " + cell(v.target, 21) + "  " + cell(v.status, 10)
}

// All screens share the same frame, margins, footer and width accounting.
func screenFrame(lines []string, width int) string {
	border := dimStyle.Render
	var b strings.Builder
	b.WriteString(" " + border("╭"+strings.Repeat("─", width+2)+"╮") + "\n")
	for _, line := range lines {
		line = ansi.Truncate(line, width, "…")
		b.WriteString(" " + border("│") + " " + line + strings.Repeat(" ", max(0, width-lipgloss.Width(line))) + " " + border("│") + "\n")
	}
	b.WriteString(" " + border("╰"+strings.Repeat("─", width+2)+"╯"))
	return b.String()
}

func (m ui) View() string {
	w, h := m.width, m.height
	if w == 0 {
		w = 100
	}
	if h == 0 {
		h = 30
	}
	if w < 48 || h < 18 {
		return "SSH 隧道\n请将终端扩大到至少 48 列 × 18 行。\nq 退出"
	}
	inner := w - 6
	rule := dimStyle.Render(strings.Repeat("─", inner))
	header := titleStyle.Render("SSH 隧道") + dimStyle.Render("  /  tunnels "+version)
	if m.mode == "details" {
		lines := []string{header, dimStyle.Render("详情与日志"), rule}
		content := []string{}
		for _, l := range strings.Split(m.details, "\n") {
			content = append(content, strings.Split(ansi.Hardwrap(clean(l), inner, true), "\n")...)
		}
		slots := h - 9
		start := min(m.scroll, max(0, len(content)-slots))
		end := min(len(content), start+slots)
		lines = append(lines, content[start:end]...)
		for len(lines) < 3+slots {
			lines = append(lines, "")
		}
		lines = append(lines, rule, dimStyle.Render("↑↓ / PgUp / PgDn 翻阅     Esc 返回"))
		return screenFrame(lines, inner)
	}
	if m.mode == "add" || m.mode == "confirm" {
		label, footer := "新建连接", "Tab 切换   Enter 保存   Esc 取消"
		body := []string{}
		if m.mode == "add" {
			labels := []string{"名称", "分组（可留空）", "SSH 命令"}
			for i, label := range labels {
				style := dimStyle
				if i == m.field {
					style = titleStyle
					label = "› " + label
				}
				body = append(body, style.Render(label))
				value := tailCrop(clean(m.fields[i]), inner-4)
				if value == "" {
					value = " "
				}
				input := cell("  "+value, inner)
				if i == m.field {
					input = selectStyle.Render(input)
				}
				body = append(body, input, "")
			}
		} else {
			label = "确认操作"
			footer = "y 确认   n / Esc 取消"
			for _, l := range logLines(m.details) {
				body = append(body, strings.Split(ansi.Hardwrap(l, inner, true), "\n")...)
			}
		}
		lines := []string{header, dimStyle.Render(label), rule}
		lines = append(lines, body...)
		lines = append(lines, "")
		if m.message != "" {
			lines = append(lines, errorStyle.Render(crop(clean(m.message), inner)))
		}
		lines = append(lines, rule, dimStyle.Render(footer))
		return screenFrame(lines, inner)
	}
	scan := "尚未扫描"
	if !m.s.ScannedAt.IsZero() {
		scan = m.s.ScannedAt.Format("15:04:05")
	}
	summary := fmt.Sprintf("托管 %d    外部 %d    最近扫描 %s", len(m.s.Managed), len(m.s.External), scan)
	if m.query != "" || m.mode == "search" {
		summary = "搜索 / " + clean(m.query) + fmt.Sprintf("    %d 个结果 · Esc 清除", len(m.rows))
	}
	lines := []string{header, dimStyle.Render(crop(summary, inner)), rule,
		dimStyle.Render(tableLine(tableRow{name: "名称 / SSH 主机", source: "来源", local: "本地监听", target: "远端目标", host: "SSH 主机", status: "状态"}, inner)), ""}
	slots := max(1, h-16)
	start := max(0, m.selected-slots+1)
	end := min(len(m.rows), start+slots)
	for i := start; i < end; i++ {
		line := tableLine(presentRow(m.rows[i]), inner)
		if i == m.selected {
			line = "› " + strings.TrimPrefix(line, "  ")
			line = selectStyle.Render(cell(line, inner))
		}
		lines = append(lines, line)
	}
	if len(m.rows) == 0 {
		empty := "没有发现隧道。R 重新扫描，a 新建连接。"
		if m.query != "" {
			empty = "没有匹配结果。按 Esc 清除搜索。"
		}
		lines = append(lines, dimStyle.Render(crop("  "+empty, inner)))
	}
	for len(lines) < 5+slots {
		lines = append(lines, "")
	}
	lines = append(lines, rule)
	selection, meta, actions := "选择一条连接查看映射详情", "外部连接按 R 刷新；托管连接在后台持续维护。", "a 新建连接"
	if len(m.rows) > 0 {
		r := m.rows[m.selected]
		v := presentRow(r)
		selection = v.local + "  →  " + v.target
		if r.managed != nil {
			meta = fmt.Sprintf("SSH %s   ·   %s   ·   PID %d", v.host, v.source, r.managed.PID)
			if r.managed.Group != "" {
				meta += "   ·   " + r.managed.Group
			}
			actions = "空格 启停   r 重启   l 日志   a 新建"
		} else {
			meta = fmt.Sprintf("SSH %s   ·   外部进程 %d", v.host, r.external.PID)
			actions = "i 纳入托管   空格 停止   a 新建"
			if !r.external.Importable {
				actions = "仅供查看   Enter 查看原因   a 新建"
			}
		}
		meta += fmt.Sprintf("   ·   %d/%d", m.selected+1, len(m.rows))
	}
	lines = append(lines, titleStyle.Render(crop(selection, inner)), dimStyle.Render(crop(meta, inner)))
	message := m.message
	if message == "" {
		message = "就绪"
	}
	if m.s.ScanError != "" {
		message = "扫描失败，保留旧列表: " + m.s.ScanError
	}
	style := dimStyle
	if strings.Contains(message, "错误") || m.s.ScanError != "" {
		style = errorStyle
	}
	lines = append(lines, style.Render(crop(clean(message), inner)), rule,
		dimStyle.Render(crop(actions, inner)), dimStyle.Render(crop("R 扫描   / 搜索   Enter 详情   ? 帮助   q 退出", inner)))
	return screenFrame(lines, inner)
}
