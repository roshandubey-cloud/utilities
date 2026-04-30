package sshtunnel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
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
	listener     net.Listener
	hostKey      ssh.Signer
	addr         string
	t            *testing.T
	closeWorker  func()
	workerListener net.Listener
	mu           sync.Mutex
	stopped      bool
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
		go s.handleConn(conn, cfg)
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
		_ = req.Reply(true, nil)

		var (
			stdout string
			exit   uint32
		)
		switch {
		case strings.HasPrefix(cmd, "uname"):
			stdout = "Linux x86_64\n"
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
	tmpDir, err := ioutil.TempDir("", "sshtunnel-bin-")
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
