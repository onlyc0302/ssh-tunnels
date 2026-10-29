package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestParseUserCommand(t *testing.T) {
	d, err := importCommand("ssh -qTfnN root@203.0.113.10 -L 3308:127.0.0.1:3306", "数据库", "测试")
	if err != nil {
		t.Fatal(err)
	}
	if d.Destination != "root@203.0.113.10" || len(d.Forwards) != 1 || d.Forwards[0].Port != 3308 || d.Wanted {
		t.Fatalf("incorrect import: %+v", d)
	}
	if len(d.Args) != 0 {
		t.Fatalf("process flags leaked into transport: %v", d.Args)
	}
}

func TestProcessIdentityOutputIgnoresTerminalLocale(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LANG", "zh_CN.UTF-8")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := processCommand(ctx, "-p", fmt.Sprint(os.Getpid()), "-o", "uid=,lstart=").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(string(b))) != 6 {
		t.Fatalf("localized process identity: %q", b)
	}
}

func TestUnreadableListeningSSHRemainsVisible(t *testing.T) {
	e := unreadableExternal(123, []string{"127.0.0.1:3308"}, errors.New("permission denied"))
	if e.PID != 123 || e.Importable || len(e.Listeners) != 1 || !strings.Contains(e.Reason, "permission denied") {
		t.Fatalf("incomplete fallback: %+v", e)
	}
	if !strings.Contains(formatExternal(e), "3308") {
		t.Fatal("listener hidden")
	}
}

func TestTableFitsTerminalAndKeepsFooterStable(t *testing.T) {
	for _, size := range [][2]int{{48, 18}, {80, 24}, {100, 30}, {140, 40}} {
		m := ui{width: size[0], height: size[1]}
		for i := 0; i < 30; i++ {
			d, _ := importCommand("ssh -N -L 3308:localhost:3306 host", fmt.Sprintf("测试连接 %d", i), "开发")
			m.s.Managed = append(m.s.Managed, Runtime{Definition: d, State: "已停止"})
		}
		m.rebuild()
		m.selected = 20
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > m.height {
			t.Fatalf("%dx%d overflows height: %d", m.width, m.height, len(lines))
		}
		for _, line := range lines {
			if lipgloss.Width(line) > m.width {
				t.Fatalf("%dx%d overflows width: %d", m.width, m.height, lipgloss.Width(line))
			}
		}
		if !strings.Contains(view, "本地监听") || !strings.Contains(view, "R 扫描") {
			t.Fatal("missing table header or actions")
		}
	}
}

func TestParseMultipleIPv6AndQuotedKey(t *testing.T) {
	d, err := importCommand(`ssh -N -i '/tmp/key with spaces' -p2222 -J jump -L '[::1]:3308:[::1]:3306' host -L127.0.0.1:6380:cache:6379`, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Forwards) != 2 || d.Forwards[0].Spec() != "[::1]:3308:[::1]:3306" {
		t.Fatal(d.Forwards)
	}
	if !reflect.DeepEqual(d.Args, []string{"-i", "/tmp/key with spaces", "-p", "2222", "-J", "jump"}) {
		t.Fatal(d.Args)
	}
}

func TestRejectUnsafeOrUnreconstructableCommands(t *testing.T) {
	for _, command := range []string{
		"ssh -N -L 0:localhost:3306 host", "ssh -N -L 3308:localhost:70000 host",
		"ssh -N -L /tmp/a:/tmp/b host", "ssh -N -L 3308:localhost:3306 host echo hello",
		"ssh -N -L 3308:localhost:3306 host; touch /tmp/no", "ssh -N -L 3308:localhost:3306 host | cat",
		"ssh -N -L 3308:localhost:3306 -o ProxyCommand=evil host", "ssh -N -L 3308:localhost:3306 -o = host",
		"ssh -N -L 3308:localhost:3306 -D1080 host", "ssh -N -L 3308:localhost:3306 -S /tmp/shared host",
	} {
		t.Run(command, func(t *testing.T) {
			if _, err := importCommand(command, "t", ""); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestShellSubstitutionNeverExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	_, _ = importCommand("ssh -N -L 3308:localhost:3306 -i '$(touch "+marker+")' host", "t", "")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command substitution was executed")
	}
}

func TestForwardValidation(t *testing.T) {
	for _, spec := range []string{"[::1:80:localhost:80", "[::1]]:80:localhost:80", "*:x:localhost:80", "localhost:80", "1::2"} {
		if _, err := parseForward(spec); err == nil {
			t.Fatalf("accepted %s", spec)
		}
	}
}

func testPaths(t *testing.T) paths {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "kt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	p := paths{root, filepath.Join(root, "api.sock"), filepath.Join(root, "config.json"), filepath.Join(root, "daemon.lock"), filepath.Join(root, "logs")}
	if err = p.prepare(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigAtomicAndCorruptionPreserved(t *testing.T) {
	p := testPaths(t)
	d, err := importCommand("ssh -N -L 3308:localhost:3306 host", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = saveDefinitions(p, []Definition{d}); err != nil {
		t.Fatal(err)
	}
	ds, err := loadDefinitions(p)
	if err != nil || len(ds) != 1 {
		t.Fatalf("%v %v", ds, err)
	}
	st, _ := os.Stat(p.Config)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	if err = os.WriteFile(p.Config, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadDefinitions(p); err == nil {
		t.Fatal("corruption ignored")
	}
	b, _ := os.ReadFile(p.Config)
	if string(b) != "broken" {
		t.Fatal("corruption overwritten")
	}
}

func TestSSHLifecycleFlagsAndRetryClassification(t *testing.T) {
	d, _ := importCommand("ssh -qTfnN host -L 3308:localhost:3306", "t", "")
	a := strings.Join(sshArgs(d, "/tmp/test.sock"), " ")
	for _, required := range []string{"ForkAfterAuthentication=no", "BatchMode=yes", "ExitOnForwardFailure=yes", "ControlPersist=no", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(a, required) {
			t.Fatal(a)
		}
	}
	for _, s := range []string{"Permission denied (publickey)", "Address already in use", "Host key verification failed"} {
		if !permanentFailure(s) {
			t.Fatal(s)
		}
	}
	if permanentFailure("Connection reset by peer") {
		t.Fatal("transient disconnect blocked")
	}
	if retryDelay(30) > 31*time.Second {
		t.Fatal("unbounded backoff")
	}
}

func TestListeningRequiresAllExpectedPorts(t *testing.T) {
	fs := []Forward{{"127.0.0.1", 3308, "localhost", 3306}, {"::1", 6380, "localhost", 6379}}
	if allListening(fs, []string{"127.0.0.1:3308"}) {
		t.Fatal("partial listeners treated as ready")
	}
	if !allListening(fs, []string{"127.0.0.1:3308", "[::1]:6380"}) {
		t.Fatal("complete listeners not ready")
	}
}

func TestReloadDoesNotBecomeRestart(t *testing.T) {
	m := ui{width: 100, height: 30}
	n, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil || !n.(ui).busy || n.(ui).message != "扫描中…" {
		t.Fatal("R did not start a scan")
	}
	// Empty external list plus 'r' must not trigger a scan or restart.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatal("r behaved as R")
	}
}

func TestExternalImportRequiresExplicitConfirmation(t *testing.T) {
	m, err := newManager(testPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	res := m.handle(request{Action: "import", External: &External{ProcessIdentity: ProcessIdentity{PID: os.Getpid()}}})
	if !strings.Contains(res.Error, "确认") {
		t.Fatal(res.Error)
	}
}

func TestTUIRendersChineseAndSanitizesControlCharacters(t *testing.T) {
	m := ui{width: 100, height: 30, message: "ready\x1b[31m"}
	d, _ := importCommand("ssh -N -L 3308:localhost:3306 host", "测试 MySQL", "开发")
	m.s.Managed = []Runtime{{Definition: d, State: "已停止"}}
	m.rebuild()
	view := m.View()
	for _, s := range []string{"测试 MySQL", "R 扫描", "3308", "3306"} {
		if !strings.Contains(view, s) {
			t.Fatalf("missing %s", s)
		}
	}
	if clean("hello\x1b\nworld") != "hello  world" {
		t.Fatal("unsafe rendering")
	}
}
