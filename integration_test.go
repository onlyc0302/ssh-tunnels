package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A loopback-only SSH fixture. It accepts only the ephemeral test key and can
// forward only to its own echo socket. No real server or database is contacted.
type sshFixture struct {
	listener     net.Listener
	echo         net.Listener
	config       *ssh.ServerConfig
	mu           sync.Mutex
	connections  map[net.Conn]bool
	closing      bool
	key, known   string
	port, target int
}

func TestReleaseBinaryBackgroundLifecycle(t *testing.T) {
	binary := os.Getenv("TUNNELS_TEST_BINARY")
	if binary == "" {
		t.Skip("set TUNNELS_TEST_BINARY to validate a packaged executable")
	}
	p := testPaths(t)
	f := newSSHFixture(t, p.Root)
	port := freePort(t)
	runCLI := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(withoutEnv(os.Environ(), "TUNNELS_HOME"), "TUNNELS_HOME="+p.Root)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, b)
		}
		return b
	}
	runCLI("doctor")
	runCLI("list", "--json")
	defer call(p, request{Action: "daemon-stop"})
	args := append([]string{"ssh", "-N"}, f.args()...)
	args = append(args, "-L", fmt.Sprintf("%d:127.0.0.1:%d", port, f.target), "fixture@127.0.0.1")
	res, err := call(p, request{Action: "add", Name: "release-test", Command: strings.Join(args, " ")})
	if err != nil {
		t.Fatal(err)
	}
	id := res.ID
	if _, err = call(p, request{Action: "start", ID: id}); err != nil {
		t.Fatal(err)
	}
	waitState := func(state string) Runtime {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var last Runtime
		for time.Now().Before(deadline) {
			res, err := call(p, request{Action: "list"})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res.Snapshot.Managed {
				if r.ID == id {
					last = r
					if r.State == state {
						return r
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("release state %s not reached: %s %s", state, last.State, last.LastError)
		return last
	}
	first := waitState("已建立")
	assertForward(t, port)
	for i := 0; i < 2; i++ {
		var s Snapshot
		if err = json.Unmarshal(runCLI("list", "--json"), &s); err != nil {
			t.Fatal(err)
		}
		if len(s.Managed) != 1 || s.Managed[0].PID != first.PID {
			t.Fatal("reopening CLI replaced the SSH process")
		}
		assertForward(t, port)
	}
	runCLI("stop", id)
	waitState("已停止")
	runCLI("daemon-stop")
}

func newSSHFixture(t *testing.T, root string) *sshFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "temporary test key")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "test_key")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), clientPub.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("unknown test key")
	}}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	f := &sshFixture{listener: l, echo: echo, config: config, connections: map[net.Conn]bool{}, key: keyPath, known: filepath.Join(root, "known_hosts"), port: l.Addr().(*net.TCPAddr).Port, target: echo.Addr().(*net.TCPAddr).Port}
	known := fmt.Sprintf("[127.0.0.1]:%d %s", f.port, ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err = os.WriteFile(f.known, []byte(known), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.mu.Lock()
		f.closing = true
		f.mu.Unlock()
		l.Close()
		echo.Close()
		f.drop()
	})
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.accept(c)
		}
	}()
	return f
}

func (f *sshFixture) accept(c net.Conn) {
	f.mu.Lock()
	if f.closing {
		f.mu.Unlock()
		c.Close()
		return
	}
	f.connections[c] = true
	f.mu.Unlock()
	defer func() { c.Close(); f.mu.Lock(); delete(f.connections, c); f.mu.Unlock() }()
	conn, channels, requests, err := ssh.NewServerConn(c, f.config)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		for req := range requests {
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()
	for ch := range channels {
		if ch.ChannelType() != "direct-tcpip" {
			_ = ch.Reject(ssh.UnknownChannelType, "test only")
			continue
		}
		var msg struct {
			Host       string
			Port       uint32
			Origin     string
			OriginPort uint32
		}
		if ssh.Unmarshal(ch.ExtraData(), &msg) != nil || msg.Host != "127.0.0.1" || int(msg.Port) != f.target {
			_ = ch.Reject(ssh.Prohibited, "fixture destination only")
			continue
		}
		upstream, err := net.Dial("tcp", f.echo.Addr().String())
		if err != nil {
			_ = ch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		stream, reqs, err := ch.Accept()
		if err != nil {
			upstream.Close()
			continue
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			defer stream.Close()
			defer upstream.Close()
			go func() { io.Copy(stream, upstream); stream.CloseWrite() }()
			io.Copy(upstream, stream)
		}()
	}
}

func (f *sshFixture) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.connections {
		c.Close()
	}
}
func (f *sshFixture) args() []string {
	return []string{"-F", "/dev/null", "-p", strconv.Itoa(f.port), "-i", f.key, "-o", "UserKnownHostsFile=" + f.known, "-o", "IdentitiesOnly=yes"}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitRuntime(t *testing.T, m *manager, id string, predicate func(Runtime) bool) Runtime {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last Runtime
	for time.Now().Before(deadline) {
		for _, r := range m.snapshot().Managed {
			if r.ID == id {
				last = r
				if predicate(r) {
					return r
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("state did not converge: state=%s pid=%d error=%s", last.State, last.PID, last.LastError)
	return last
}

func assertForward(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	const payload = "tunnel-echo-verification"
	if _, err = io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatal("forwarded payload mismatch")
	}
}

func TestIntegrationExternalImportReconnectAndManualStop(t *testing.T) {
	// Reproduce the user's kitty locale, including the localized ps lstart bug.
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	p := testPaths(t)
	f := newSSHFixture(t, p.Root)
	port := freePort(t)
	args := append(f.args(), "-qTfnN", "fixture@127.0.0.1", "-L", fmt.Sprintf("%d:127.0.0.1:%d", port, f.target))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if b, err := hostCommand(ctx, "ssh", args...).CombinedOutput(); err != nil {
		t.Fatalf("launch fixture ssh: %v %s", err, b)
	}
	m, err := newManager(p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.shutdown()
	if err = m.reload(); err != nil {
		t.Fatal(err)
	}
	var external *External
	for _, e := range m.snapshot().External {
		for _, mapping := range e.Forwards {
			if mapping.Port == port && mapping.TargetPort == f.target {
				copy := e
				external = &copy
			}
		}
	}
	if external == nil {
		t.Fatal("manual ssh -qTfnN ... -L tunnel was not discovered")
	}
	defer func() {
		if _, err := verifyIdentity(external.ProcessIdentity); err == nil {
			_ = stopExternal(*external)
		}
	}()
	if !external.Importable || len(external.Listeners) == 0 {
		t.Fatalf("external tunnel incomplete: importable=%t reason=%s", external.Importable, external.Reason)
	}
	assertForward(t, port)
	if err = m.reload(); err != nil {
		t.Fatal(err)
	}
	if _, err = verifyIdentity(external.ProcessIdentity); err != nil {
		t.Fatal("reload interrupted original tunnel")
	}
	res := m.handle(request{Action: "import", External: external, Confirmed: true, Name: "fixture-db", Group: "test"})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	first := waitRuntime(t, m, res.ID, func(r Runtime) bool { return r.State == "已建立" })
	assertForward(t, port)
	if err = m.reload(); err != nil {
		t.Fatal(err)
	}
	for _, e := range m.snapshot().External {
		if e.PID == first.PID {
			t.Fatal("managed connection duplicated as external")
		}
	}
	f.drop()
	waitRuntime(t, m, res.ID, func(r Runtime) bool { return r.State == "等待重连" })
	second := waitRuntime(t, m, res.ID, func(r Runtime) bool { return r.State == "已建立" && r.PID != first.PID })
	if second.Retries < 1 {
		t.Fatal("reconnect not counted")
	}
	assertForward(t, port)
	if err = m.setWanted(res.ID, false, false); err != nil {
		t.Fatal(err)
	}
	waitRuntime(t, m, res.ID, func(r Runtime) bool { return r.State == "已停止" && r.PID == 0 })
	time.Sleep(1500 * time.Millisecond)
	for _, r := range m.snapshot().Managed {
		if r.ID == res.ID && (r.Wanted || r.PID != 0) {
			t.Fatal("manual stop unexpectedly reconnected")
		}
	}
	// Port conflict is visible and does not terminate or replace the other listener.
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err = m.setWanted(res.ID, true, false); err != nil {
		t.Fatal(err)
	}
	waitRuntime(t, m, res.ID, func(r Runtime) bool { return r.State == "需要处理" })
	probe, err := net.DialTimeout("tcp", occupied.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("conflicting listener disrupted")
	}
	probe.Close()
	if err = m.setWanted(res.ID, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationBadIdentityBlocksRetry(t *testing.T) {
	p := testPaths(t)
	f := newSSHFixture(t, p.Root)
	_, bad, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(bad, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.key, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := newManager(p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.shutdown()
	d := Definition{ID: newID(), Name: "bad-key", Destination: "fixture@127.0.0.1", Args: f.args(), Forwards: []Forward{{"127.0.0.1", freePort(t), "127.0.0.1", f.target}}, AutoReconnect: true}
	if err = m.add(d); err != nil {
		t.Fatal(err)
	}
	if err = m.setWanted(d.ID, true, false); err != nil {
		t.Fatal(err)
	}
	r := waitRuntime(t, m, d.ID, func(r Runtime) bool { return r.State == "需要处理" })
	if r.Retries != 0 {
		t.Fatal("authentication failure should not loop")
	}
}

func TestIntegrationImportAuthFailurePreservesExternal(t *testing.T) {
	p := testPaths(t)
	f := newSSHFixture(t, p.Root)
	port := freePort(t)
	args := append(f.args(), "-qTfnN", "fixture@127.0.0.1", "-L", fmt.Sprintf("%d:127.0.0.1:%d", port, f.target))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if b, err := hostCommand(ctx, "ssh", args...).CombinedOutput(); err != nil {
		t.Fatalf("launch: %v %s", err, b)
	}
	m, err := newManager(p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.shutdown()
	if err = m.reload(); err != nil {
		t.Fatal(err)
	}
	var external *External
	for _, e := range m.snapshot().External {
		for _, mapping := range e.Forwards {
			if mapping.Port == port && mapping.TargetPort == f.target {
				copy := e
				external = &copy
			}
		}
	}
	if external == nil {
		t.Fatal("test SSH process missing")
	}
	defer stopExternal(*external)
	// Existing SSH transport remains authenticated, but future authentication fails.
	_, bad, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(bad, "")
	if err = os.WriteFile(f.key, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	res := m.handle(request{Action: "import", External: external, Confirmed: true})
	if res.Error == "" {
		t.Fatal("expected failed authentication preflight")
	}
	if _, err = verifyIdentity(external.ProcessIdentity); err != nil {
		t.Fatal("original SSH process was interrupted")
	}
	assertForward(t, port)
}
