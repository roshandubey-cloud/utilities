package sshtunnel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSHServer is an in-process SSH server that speaks just enough of
// the protocol to drive sshtunnel.Spawn end-to-end. It accepts password
// "testpass" for user "test", canned-responds to the exec channels Spawn
// opens (uname / pkill / smoke / nohup), and honours direct-tcpip
// requests by net.Dial-ing the requested address — that's how the
// reverse tunnel actually works in the test.
type fakeSSHServer struct {
	listener       net.Listener
	hostKey        ssh.Signer
	addr           string
	t              *testing.T
	closeWorker    func()
	workerListener net.Listener
	mu             sync.Mutex
	stopped        bool
	// acceptedConns tracks every TCP conn the server handed off to
	// handleConn. The keepalive failure test calls killAcceptedConns()
	// to simulate a remote SSH session dying mid-run.
	acceptedConns []net.Conn
	// unameOut, when non-empty, overrides the canned "Linux x86_64"
	// response so a test can drive a darwin code path through the same
	// in-process server.
	unameOut string
	// sshdConfigOut, when non-empty, is returned by the canned grep
	// response on `/etc/ssh/sshd_config`. Used to exercise the
	// PasswordAuthentication probe.
	sshdConfigOut string
	// execLog records every command the server processed, so a test can
	// assert (e.g.) that `xattr -d com.apple.quarantine` ran on darwin.
	execLog   []string
	execLogMu sync.Mutex
}

func newFakeSSH(t *testing.T) *fakeSSHServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("ssh signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeSSHServer{
		listener: ln,
		hostKey:  signer,
		addr:     ln.Addr().String(),
		t:        t,
	}
	go srv.acceptLoop()
	return srv
}

func (s *fakeSSHServer) host() (string, string) {
	h, p, _ := net.SplitHostPort(s.addr)
	return h, p
}

func (s *fakeSSHServer) Close() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	_ = s.listener.Close()
	if s.closeWorker != nil {
		s.closeWorker()
	}
}

// startWorkerListener spins up a tiny HTTP server on a loopback port
// and remembers the port. The test's "spawn" exec handler returns this
// port via /tmp/sftp-loadtest.log so the rest of Spawn (waitRemoteReady
// + accept loop) can dial it. We pin it to 127.0.0.1:18081 to match the
// default RemoteBindAddr — Spawn's wait + dial both use that exact
// string.
func (s *fakeSSHServer) startWorkerListener() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerListener != nil {
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:18081")
	if err != nil {
		// Test environment already has 18081 in use — skip the test
		// rather than fight the port.
		s.t.Skipf("could not bind 127.0.0.1:18081 for worker stub: %v", err)
		return
	}
	s.workerListener = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/probe-test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true,"src":"fake-worker"}`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	s.closeWorker = func() {
		_ = srv.Close()
		_ = ln.Close()
	}
}

func (s *fakeSSHServer) acceptLoop() {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "test" && string(pass) == "testpass" {
				return nil, nil
			}
			return nil, fmt.Errorf("auth denied")
		},
	}
	cfg.AddHostKey(s.hostKey)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.acceptedConns = append(s.acceptedConns, conn)
		s.mu.Unlock()
		go s.handleConn(conn, cfg)
	}
}

// killAcceptedConns force-closes every TCP conn the server has handed
// off to handleConn. The remote *ssh.ClientConn held by sshtunnel.Tunnel
// will see I/O errors on its next SendRequest, which is exactly what
// the keepalive loop is supposed to detect.
func (s *fakeSSHServer) killAcceptedConns() {
	s.mu.Lock()
	conns := append([]net.Conn(nil), s.acceptedConns...)
	s.acceptedConns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *fakeSSHServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		switch newCh.ChannelType() {
		case "session":
			ch, sessReqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			go s.handleSession(ch, sessReqs)
		case "direct-tcpip":
			s.handleDirectTCPIP(newCh)
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

// handleSession serves one "session" channel. We only respond to "exec"
// and immediately close after canned output; no shell, no env, no pty.
func (s *fakeSSHServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		// exec payload is a single string preceded by a 4-byte length.
		if len(req.Payload) < 4 {
			_ = req.Reply(false, nil)
			continue
		}
		n := binary.BigEndian.Uint32(req.Payload[:4])
		if int(n)+4 > len(req.Payload) {
			_ = req.Reply(false, nil)
			continue
		}
		cmd := string(req.Payload[4 : 4+n])
		s.execLogMu.Lock()
		s.execLog = append(s.execLog, cmd)
		s.execLogMu.Unlock()
		_ = req.Reply(true, nil)

		var (
			stdout string
			exit   uint32
		)
		switch {
		case strings.HasPrefix(cmd, "uname"):
			if s.unameOut != "" {
				stdout = s.unameOut
			} else {
				stdout = "Linux x86_64\n"
			}
		case strings.HasPrefix(cmd, "grep") && strings.Contains(cmd, "sshd_config"):
			stdout = s.sshdConfigOut
		case strings.HasPrefix(cmd, "xattr"):
			// Pretend xattr exists on the remote — Spawn discards the
			// output anyway; we just want the server to record the
			// invocation so the test can assert on it.
			stdout = ""
		case strings.HasPrefix(cmd, "pkill"):
			// pkill returns 0 when killed something, 1 when nothing matched.
			// Either is fine; we go with 0 because runExec doesn't care.
			exit = 0
		case strings.Contains(cmd, "-version"):
			stdout = "sftp-loadtest 0.11.0\n"
		case strings.Contains(cmd, "nohup") && strings.Contains(cmd, "-addr"):
			// "Spawn the worker": kick off the in-process HTTP server
			// stub so the subsequent waitRemoteReady direct-tcpip dial
			// finds something to connect to.
			s.startWorkerListener()
			stdout = ""
		default:
			stdout = ""
		}
		if stdout != "" {
			_, _ = io.WriteString(ch, stdout)
		}
		// Send exit-status request, then close.
		statusBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(statusBuf, exit)
		_, _ = ch.SendRequest("exit-status", false, statusBuf)
		return
	}
}

// handleDirectTCPIP honours port-forward requests by net.Dialing the
// requested address and copying bytes both ways. The Spawn flow uses
// this for waitRemoteReady AND for every HTTP call that flows through
// the local listener → ssh → worker stub.
func (s *fakeSSHServer) handleDirectTCPIP(newCh ssh.NewChannel) {
	type req struct {
		HostToConnect       string
		PortToConnect       uint32
		OriginatorIPAddress string
		OriginatorPort      uint32
	}
	var r req
	if err := ssh.Unmarshal(newCh.ExtraData(), &r); err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target := net.JoinHostPort(r.HostToConnect, fmt.Sprintf("%d", r.PortToConnect))
	upstream, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newCh.Accept()
	if err != nil {
		upstream.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { _, _ = io.Copy(upstream, ch); upstream.Close() }()
	go func() { _, _ = io.Copy(ch, upstream); ch.Close() }()
}

// TestSpawn_UploadEndToEnd drives the full Spawn flow against the
// in-process server: SSH dial → uname → pkill → upload binary → smoke
// → spawn worker → waitRemoteReady → reverse tunnel listener → HTTP
// GET round-trip → Close. A successful run proves every step composes.
func TestSpawn_UploadEndToEnd(t *testing.T) {
	srv := newFakeSSH(t)
	defer srv.Close()
	host, port := srv.host()

	// Local "binary" payload — the upload path streams whatever the file
	// at LocalBinaryPath contains. Content doesn't matter; pkg/sftp will
	// see Open succeed and Copy will run.
	tmpDir, err := os.MkdirTemp("", "sshtunnel-bin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	binPath := filepath.Join(tmpDir, "fake-bin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// NOTE: our fake server doesn't actually run the SFTP subsystem —
	// pkg/sftp would block waiting for a real "subsystem" channel reply.
	// The test therefore exercises everything EXCEPT the upload step by
	// using install_method="download". The "download" path's exec is
	// just a curl one-liner which our fake server canned-responds to as
	// a no-op (default branch returns "" + exit 0).
	tun, err := Spawn(ctx, SpawnOpts{
		Host:          host,
		Port:          port,
		User:          "test",
		Password:      "testpass",
		HostKey:       ssh.InsecureIgnoreHostKey(),
		InstallMethod: "download",
		ReleaseTag:    "v0.11.0",
	})
	if err != nil {
		t.Fatalf("Spawn: %v\nlog so far: %v", err, func() []string {
			if tun != nil {
				return tun.SpawnLog
			}
			return nil
		}())
	}
	defer tun.Close()

	if tun.LocalURL == "" {
		t.Fatal("LocalURL empty")
	}
	if tun.Arch != "linux-amd64" {
		t.Fatalf("Arch = %q, want linux-amd64", tun.Arch)
	}

	// Round-trip: HTTP GET via the reverse tunnel reaches the fake
	// worker.
	resp, err := http.Get(tun.LocalURL + "/probe-test")
	if err != nil {
		t.Fatalf("GET /probe-test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, body %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "fake-worker") {
		t.Fatalf("body = %q, want fake-worker marker", string(body))
	}

	// Close should be clean + idempotent.
	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// After close, fresh GET attempts must fail (listener gone).
	c := &http.Client{Timeout: 500 * time.Millisecond}
	if _, err := c.Get(tun.LocalURL + "/probe-test"); err == nil {
		t.Fatalf("expected GET to fail after Close, got nil err")
	}
}

// TestKeepAlive_ClosesTunnelOnSessionLoss is the runtime pin for the SSH
// KeepAlive loop. The loop's job is: notice a dead SSH session within
// keepAliveMaxFails × keepAliveInterval and proactively Close the tunnel
// so cluster status flips to "unreachable" instead of silently aggregating
// zero progress.
//
// We exercise the fixed feature, not a symptom: spawn a real tunnel against
// the in-process fake server, then yank the rug by closing the server's
// listener and forcibly killing every accepted SSH conn it holds. The
// keepalive's SendRequest must error, accumulate fails, and trigger Close
// inside the shrunk interval window.
func TestKeepAlive_ClosesTunnelOnSessionLoss(t *testing.T) {
	// Override the production knobs so the test runs in ~150ms instead
	// of 90s. Restored on exit.
	savedInterval, savedMax := keepAliveInterval, keepAliveMaxFails
	keepAliveInterval = 30 * time.Millisecond
	keepAliveMaxFails = 2
	defer func() {
		keepAliveInterval = savedInterval
		keepAliveMaxFails = savedMax
	}()

	srv := newFakeSSH(t)
	host, port := srv.host()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tun, err := Spawn(ctx, SpawnOpts{
		Host:          host,
		Port:          port,
		User:          "test",
		Password:      "testpass",
		HostKey:       ssh.InsecureIgnoreHostKey(),
		InstallMethod: "download",
		ReleaseTag:    "v0.11.0",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer tun.Close()
	if tun.IsClosed() {
		t.Fatal("tunnel should not be closed immediately after Spawn")
	}

	// Kill the underlying TCP conns the server accepted — the *ssh.Client
	// held by the tunnel will see I/O errors on its next SendRequest.
	// listener.Close() alone wouldn't do it; existing conns survive that.
	srv.killAcceptedConns()
	defer srv.Close()

	// Within keepAliveMaxFails × keepAliveInterval (60ms) the loop should
	// flip the tunnel to closed. Allow generous headroom for CI noise.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tun.IsClosed() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !tun.IsClosed() {
		t.Fatalf("keepalive did not close tunnel after session loss; SpawnLog=%v", tun.SpawnLog)
	}

	// SpawnLog should have at least one keepalive-failed entry; this
	// confirms the loop ran the failure branch rather than the tunnel
	// closing for some other reason (context cancel, etc).
	sawKeepalive := false
	for _, line := range tun.SpawnLog {
		if strings.Contains(line, "keepalive failed") || strings.Contains(line, "ssh session lost") {
			sawKeepalive = true
			break
		}
	}
	if !sawKeepalive {
		t.Fatalf("expected keepalive failure log entry; got %v", tun.SpawnLog)
	}
}

// TestSpawn_OnStepEmitsAllStepsInOrder asserts that every named step in
// the documented protocol fires through OnStep, in order, and each one
// reaches a terminal "ok" status on the happy path. The wizard's NDJSON
// streaming wire is exactly this sequence — if a step ever stops emitting,
// the corresponding ⏳ in the wizard would never flip.
func TestSpawn_OnStepEmitsAllStepsInOrder(t *testing.T) {
	srv := newFakeSSH(t)
	defer srv.Close()
	host, port := srv.host()

	var mu sync.Mutex
	var got []StepUpdate
	collect := func(u StepUpdate) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, u)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tun, err := Spawn(ctx, SpawnOpts{
		Host:          host,
		Port:          port,
		User:          "test",
		Password:      "testpass",
		HostKey:       ssh.InsecureIgnoreHostKey(),
		InstallMethod: "download",
		ReleaseTag:    "v0.11.0",
		OnStep:        collect,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer tun.Close()

	// Expected step names in expected order; each must appear at least
	// once with a terminal "ok" status.
	want := []string{
		"ssh-dial",
		"arch-detect",
		"pkill-orphans",
		"install",
		"smoke",
		"spawn-process",
		"wait-ready",
		"tunnel-listener",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("OnStep was never called")
	}
	terminals := map[string]string{}
	firstSeen := map[string]int{}
	for i, u := range got {
		if _, ok := firstSeen[u.Step]; !ok {
			firstSeen[u.Step] = i
		}
		if u.Status == "ok" || u.Status == "err" {
			terminals[u.Step] = u.Status
		}
	}
	for _, name := range want {
		if _, ok := firstSeen[name]; !ok {
			t.Errorf("missing OnStep call for step %q", name)
		}
		if terminals[name] != "ok" {
			t.Errorf("step %q terminal status = %q, want ok (got events: %+v)", name, terminals[name], got)
		}
	}
	// Strict order: first-seen index must be monotonically increasing in
	// the documented order.
	prevIdx := -1
	for _, name := range want {
		idx, ok := firstSeen[name]
		if !ok {
			continue
		}
		if idx <= prevIdx {
			t.Errorf("step %q first-seen at index %d, but previous step was at %d (out of order)", name, idx, prevIdx)
		}
		prevIdx = idx
	}
}

// TestSpawn_DarwinStripsQuarantineXattr drives Spawn against a fake
// darwin remote and asserts the `xattr -d com.apple.quarantine` cleanup
// command was run on the install path. Critical for v0.13.5 — without
// the strip, Gatekeeper SIGKILLs the freshly-arrived binary as soon as
// the worker tries to exec.
func TestSpawn_DarwinStripsQuarantineXattr(t *testing.T) {
	srv := newFakeSSH(t)
	defer srv.Close()
	srv.unameOut = "Darwin arm64\n"
	host, port := srv.host()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tun, err := Spawn(ctx, SpawnOpts{
		Host:          host,
		Port:          port,
		User:          "test",
		Password:      "testpass",
		HostKey:       ssh.InsecureIgnoreHostKey(),
		InstallMethod: "download",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer tun.Close()
	if tun.Arch != "darwin-arm64" {
		t.Fatalf("Arch = %q, want darwin-arm64", tun.Arch)
	}

	srv.execLogMu.Lock()
	defer srv.execLogMu.Unlock()
	sawXattr := false
	for _, c := range srv.execLog {
		if strings.Contains(c, "xattr -d com.apple.quarantine") {
			sawXattr = true
			break
		}
	}
	if !sawXattr {
		t.Fatalf("expected xattr -d com.apple.quarantine to run on darwin remote, exec log: %v", srv.execLog)
	}
}

// TestPreflight_DarwinPasswordAuthDisabled checks that the new
// password_auth_disabled flag gets set when the macOS sshd_config probe
// returns a literal "PasswordAuthentication no" line. This is the
// pre-spawn warning the wizard surfaces on Step S2.
func TestPreflight_DarwinPasswordAuthDisabled(t *testing.T) {
	srv := newFakeSSH(t)
	defer srv.Close()
	srv.unameOut = "Darwin arm64\n"
	srv.sshdConfigOut = "PasswordAuthentication no\n"
	host, port := srv.host()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Preflight(ctx, SpawnOpts{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "testpass",
		HostKey:  ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !res.Reachable {
		t.Fatalf("expected reachable, got log: %v", res.Log)
	}
	if !res.PasswordAuthDisabled {
		t.Fatalf("PasswordAuthDisabled = false; expected true on darwin with sshd_config 'PasswordAuthentication no'")
	}
}

// TestPreflight_LinuxNoPasswordAuthFlag asserts the password-auth probe
// is darwin-only — Linux remotes never get the flag set even if they
// happen to have a "PasswordAuthentication no" line. Linux operators
// usually realise this through their cloud console or terminal first;
// the warning is a macOS-specific gotcha.
func TestPreflight_LinuxNoPasswordAuthFlag(t *testing.T) {
	srv := newFakeSSH(t)
	defer srv.Close()
	srv.sshdConfigOut = "PasswordAuthentication no\n" // would-be setting; should be ignored on linux
	host, port := srv.host()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Preflight(ctx, SpawnOpts{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "testpass",
		HostKey:  ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if res.PasswordAuthDisabled {
		t.Fatal("PasswordAuthDisabled true on linux — should only fire on darwin")
	}
}

func TestDetectMacPasswordAuthDisabled_Parsing(t *testing.T) {
	// Direct unit test of the parser. We can't easily inject an
	// *ssh.Client here, so verify the parsing branches via a small
	// inline helper that mirrors the production logic.
	cases := []struct {
		grepOut string
		want    bool
	}{
		{"", false},
		{"PasswordAuthentication no", true},
		{"PasswordAuthentication  no", true},
		{"  PasswordAuthentication no  ", true},
		{"PasswordAuthentication yes", false},
		{"#PasswordAuthentication no", false},
		{"# PasswordAuthentication no", false},
	}
	for _, tc := range cases {
		got := parsePasswordAuthLine(tc.grepOut)
		if got != tc.want {
			t.Errorf("parsePasswordAuthLine(%q) = %v, want %v", tc.grepOut, got, tc.want)
		}
	}
}

func TestSpawnOpts_Validation(t *testing.T) {
	cases := []struct {
		name string
		opts SpawnOpts
		want string
	}{
		{"missing host", SpawnOpts{User: "u", Password: "p"}, "host required"},
		{"missing user", SpawnOpts{Host: "h", Password: "p"}, "user required"},
		{"missing auth", SpawnOpts{Host: "h", User: "u"}, "either Password or PrivateKeyPEM required"},
		{"bad install method", SpawnOpts{Host: "h", User: "u", Password: "p", InstallMethod: "ftp"}, "install method"},
		{"upload missing local path", SpawnOpts{Host: "h", User: "u", Password: "p", InstallMethod: "upload"}, "LocalBinaryPath required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Spawn(context.Background(), tc.opts)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestMapArch(t *testing.T) {
	cases := []struct {
		in, want string
		isErr    bool
	}{
		{"Linux x86_64", "linux-amd64", false},
		{"Linux aarch64", "linux-arm64", false},
		{"Darwin arm64", "darwin-arm64", false},
		{"Darwin x86_64", "darwin-amd64", false},
		{"MINGW64_NT-10.0 x86_64", "windows-amd64", false},
		{"OpenBSD amd64", "", true},
		{"junk", "", true},
	}
	for _, tc := range cases {
		got, err := mapArch(tc.in)
		if tc.isErr {
			if err == nil {
				t.Fatalf("mapArch(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("mapArch(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("mapArch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAssetSuffixForArch(t *testing.T) {
	cases := map[string]string{
		"darwin-arm64":  "mac-apple-silicon",
		"darwin-amd64":  "mac-intel",
		"linux-amd64":   "linux-amd64",
		"linux-arm64":   "linux-arm64",
		"windows-amd64": "windows-amd64",
	}
	for arch, want := range cases {
		got, err := assetSuffixForArch(arch)
		if err != nil {
			t.Fatalf("assetSuffixForArch(%q): %v", arch, err)
		}
		if got != want {
			t.Fatalf("assetSuffixForArch(%q) = %q, want %q", arch, got, want)
		}
	}
	if _, err := assetSuffixForArch("plan9-amd64"); err == nil {
		t.Fatal("expected error for unsupported arch")
	}
}
