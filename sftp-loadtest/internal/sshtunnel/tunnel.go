// Package sshtunnel bootstraps a remote sftp-loadtest worker over SSH and
// exposes its HTTP API back through a reverse tunnel on the master.
//
// The flow ("Option A" from the v0.11.0 design):
//
//  1. Master SSH-dials the remote.
//  2. uname is run to detect the remote arch.
//  3. Any orphan worker from a previous master is reaped (defensive pkill).
//  4. The binary is installed — either pulled from a GitHub release on the
//     remote (needs egress) or uploaded over the existing SSH session's
//     SFTP subsystem (no egress required).
//  5. A smoke test verifies the binary actually runs.
//  6. The worker is spawned via nohup, bound to 127.0.0.1:18081 — no port
//     is ever exposed on the remote's external interface.
//  7. Master listens on a random local loopback port; every connection
//     accepted there is forwarded through the SSH client to the worker's
//     loopback bind. The cluster coordinator uses the local URL exactly
//     like a manually-typed worker URL.
//
// Lifecycle: the master holds the SSH session for the worker's lifetime.
// Tunnel.Close stops accepting new local conns, kills the remote process,
// closes the SSH connection, and is idempotent.
package sshtunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

// SpawnOpts is the input to Spawn. See package doc for the flow these
// fields drive.
type SpawnOpts struct {
	// Host is the SSH endpoint the master dials. Required.
	Host string
	// Port is the SSH port; defaults to "22" when empty.
	Port string
	// User is the SSH login. Required.
	User string

	// Password OR PrivateKeyPEM must be set. PrivateKeyPEM wins when both
	// are non-empty so callers that want to roll a key over without
	// removing the legacy password from a shared form can do so cleanly.
	Password      string
	PrivateKeyPEM string
	Passphrase    string

	// HostKey, when non-nil, is used for SSH host-key verification. When
	// nil, the package falls back to ssh.InsecureIgnoreHostKey() — the
	// caller is expected to supply a real callback (e.g. via the host-key
	// store) in production.
	HostKey ssh.HostKeyCallback

	// InstallMethod selects how the binary lands on the remote.
	// "download" pulls from GitHub releases (needs internet on remote).
	// "upload" streams the local master binary via SFTP-over-SSH.
	InstallMethod string

	// LocalBinaryPath is the path on the master that the SFTP upload
	// streams. Required when InstallMethod=="upload". Mode is forced to
	// 0o755 on the remote regardless of the local mode.
	LocalBinaryPath string

	// ReleaseTag is the GitHub release tag to download. Empty = "latest".
	ReleaseTag string

	// RemoteBinaryPath is where the binary lands on the remote. Defaults
	// to "/tmp/sftp-loadtest".
	RemoteBinaryPath string

	// RemoteBindAddr is the bind address the spawned worker listens on
	// (always loopback on the remote). Defaults to "127.0.0.1:18081".
	RemoteBindAddr string

	// OnStep, when non-nil, is called as each named step starts and ends.
	// It is the streaming hook the HTTP handler uses to forward NDJSON
	// progress events to the wizard. Calls are synchronous on the goroutine
	// running Spawn, so a slow callback throttles the bootstrap. Pass a
	// non-blocking callback in production.
	OnStep func(StepUpdate)
}

// StepUpdate is one progress event emitted by Spawn through OnStep. The
// wizard's spawn-log row keyed off Step.Name flips its icon from ⏳ to 🔄
// (running) to ✓ / ✗ (ok / err) as updates arrive. Detail is the
// human-readable trace fragment ("Detected linux-amd64", "Uploading
// 14.2 MB", "Worker bound to 127.0.0.1:18081").
//
// Step names are stable wire identifiers — the streaming protocol uses
// them as map keys on the client. The full set:
//
//   ssh-dial         — TCP + SSH handshake + auth
//   arch-detect      — uname -s -m
//   pkill-orphans    — defensive reap
//   install          — download or upload
//   smoke            — -version / -h
//   spawn-process    — nohup binary
//   wait-ready       — direct-tcpip probe loop
//   tunnel-listener  — local 127.0.0.1:0 listen + accept loop start
//
// Status is one of "running", "ok", "err". A given step always emits at
// least one "running" then exactly one "ok" or "err".
type StepUpdate struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Tunnel is the live state of one SSH-bootstrapped worker. LocalURL is
// what callers feed to the cluster coordinator — every HTTP call to it
// is forwarded through the SSH session.
type Tunnel struct {
	LocalURL   string
	RemoteAddr string
	Arch       string
	SpawnLog   []string

	mu        sync.Mutex
	closed    bool
	listener  net.Listener
	sshClient *ssh.Client
	sftpClient *sftp.Client
	binaryPath string
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// Spawn runs the bootstrap protocol end-to-end. On success the returned
// Tunnel is fully wired and ready to serve HTTP. On any failure every
// resource opened so far is torn down before the error returns, so the
// caller never has to clean up a half-built tunnel.
func Spawn(ctx context.Context, opts SpawnOpts) (*Tunnel, error) {
	if opts.Host == "" {
		return nil, errors.New("host required")
	}
	if opts.User == "" {
		return nil, errors.New("user required")
	}
	if opts.Password == "" && opts.PrivateKeyPEM == "" {
		return nil, errors.New("either Password or PrivateKeyPEM required")
	}
	if opts.InstallMethod == "" {
		opts.InstallMethod = "download"
	}
	if opts.InstallMethod != "download" && opts.InstallMethod != "upload" {
		return nil, fmt.Errorf("install method %q not supported (want \"download\" or \"upload\")", opts.InstallMethod)
	}
	if opts.InstallMethod == "upload" && opts.LocalBinaryPath == "" {
		return nil, errors.New("LocalBinaryPath required when InstallMethod=\"upload\"")
	}
	port := opts.Port
	if port == "" {
		port = "22"
	}
	bin := opts.RemoteBinaryPath
	if bin == "" {
		bin = "/tmp/sftp-loadtest"
	}
	bindAddr := opts.RemoteBindAddr
	if bindAddr == "" {
		bindAddr = "127.0.0.1:18081"
	}

	t := &Tunnel{
		RemoteAddr: bindAddr,
		binaryPath: bin,
	}

	emit := func(step, status, detail string) {
		if opts.OnStep == nil {
			return
		}
		opts.OnStep(StepUpdate{Step: step, Status: status, Detail: detail})
	}

	// Step 1 — SSH dial + auth.
	t.appendLog("Dialing SSH " + opts.Host + ":" + port + " as " + opts.User)
	emit("ssh-dial", "running", "Dialing "+opts.Host+":"+port+" as "+opts.User)
	auth, err := buildAuth(opts)
	if err != nil {
		emit("ssh-dial", "err", err.Error())
		return nil, err
	}
	hk := opts.HostKey
	if hk == nil {
		hk = ssh.InsecureIgnoreHostKey()
	}
	cfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         15 * time.Second,
	}
	sshAddr := net.JoinHostPort(opts.Host, port)
	client, err := dialSSHContext(ctx, sshAddr, cfg)
	if err != nil {
		emit("ssh-dial", "err", err.Error())
		return nil, fmt.Errorf("ssh dial %s: %w", sshAddr, err)
	}
	t.sshClient = client
	emit("ssh-dial", "ok", "Connected")

	// From here on, any error path must Close the tunnel.
	finish := func(step string, err error) (*Tunnel, error) {
		if step != "" {
			emit(step, "err", err.Error())
		}
		_ = t.Close()
		return nil, err
	}

	// Step 2 — Detect arch.
	t.appendLog("Detecting remote arch (uname -s -m)")
	emit("arch-detect", "running", "Running uname -s -m")
	out, err := runExec(client, "uname -s -m")
	if err != nil {
		return finish("arch-detect", fmt.Errorf("uname: %w", err))
	}
	arch, err := mapArch(out)
	if err != nil {
		return finish("arch-detect", err)
	}
	t.Arch = arch
	t.appendLog("Detected arch: " + arch)
	emit("arch-detect", "ok", "Detected "+arch)

	// macOS has $HOME-rooted default install path because Gatekeeper
	// occasionally treats /tmp writes as quarantined. Operators can still
	// override via opts.RemoteBinaryPath; we only nudge the default when
	// the caller didn't pin a path.
	isDarwin := strings.HasPrefix(arch, "darwin-")
	if isDarwin && opts.RemoteBinaryPath == "" {
		bin = "$HOME/sftp-loadtest"
		t.binaryPath = bin
	}

	// Step 3 — Defensive cleanup of any orphan from a previous master.
	t.appendLog("Reaping orphan workers (pkill)")
	emit("pkill-orphans", "running", "Reaping any orphan worker on "+bindAddr)
	pkillCmd := fmt.Sprintf("pkill -f 'sftp-loadtest -addr %s' 2>/dev/null; sleep 0.3; true", regexEscape(bindAddr))
	if _, err := runExec(client, pkillCmd); err != nil {
		// pkill returning non-zero (no matches) is normal — runExec only
		// surfaces hard errors (channel failures), not non-zero exits.
		t.appendLog("pkill warning: " + err.Error())
	}
	emit("pkill-orphans", "ok", "Reap done")

	// Step 4 — Install.
	emit("install", "running", "Installing via "+opts.InstallMethod)
	switch opts.InstallMethod {
	case "download":
		assetSuffix, err := assetSuffixForArch(arch)
		if err != nil {
			return finish("install", err)
		}
		tag := opts.ReleaseTag
		var releasePath string
		if tag == "" {
			releasePath = "latest/download"
		} else {
			releasePath = "download/" + tag
		}
		releaseURL := fmt.Sprintf(
			"https://github.com/roshandubey-cloud/utilities/releases/%s/sftp-loadtest-webui-%s.zip",
			releasePath, assetSuffix,
		)
		t.appendLog("Downloading from " + releaseURL)
		emit("install", "running", "Downloading "+releaseURL)
		dlCmd := fmt.Sprintf(
			"curl -fsSL %q -o /tmp/sftp-loadtest.zip && "+
				"unzip -o /tmp/sftp-loadtest.zip -d /tmp/sftp-loadtest-bin && "+
				"mv /tmp/sftp-loadtest-bin/sftp-loadtest-%s* %q && "+
				"chmod +x %q",
			releaseURL, assetSuffix, bin, bin,
		)
		if _, err := runExec(client, dlCmd); err != nil {
			return finish("install", fmt.Errorf("download install: %w", err))
		}
	case "upload":
		t.appendLog("Uploading local binary " + opts.LocalBinaryPath + " to " + bin)
		emit("install", "running", "Uploading "+opts.LocalBinaryPath)
		fi, err := os.Stat(opts.LocalBinaryPath)
		if err != nil {
			return finish("install", fmt.Errorf("local binary: %w", err))
		}
		sc, err := sftp.NewClient(client)
		if err != nil {
			return finish("install", fmt.Errorf("open sftp subsystem: %w", err))
		}
		t.sftpClient = sc
		if err := uploadBinary(sc, opts.LocalBinaryPath, bin); err != nil {
			return finish("install", fmt.Errorf("upload binary: %w", err))
		}
		emit("install", "running", fmt.Sprintf("Uploaded %d bytes", fi.Size()))
	}
	// macOS quarantine attribute strip — Gatekeeper otherwise can SIGKILL
	// the freshly-arrived binary when it tries to exec. No-op on non-mac
	// targets (xattr is mac-only) but we run unconditionally on darwin
	// so download AND upload both clear the flag.
	if isDarwin {
		stripCmd := fmt.Sprintf("xattr -d com.apple.quarantine %s 2>/dev/null || true", bin)
		_, _ = runExec(client, stripCmd)
		t.appendLog("Stripped com.apple.quarantine xattr")
		emit("install", "running", "Stripped com.apple.quarantine xattr")
	}
	t.appendLog("Installed at " + bin)
	emit("install", "ok", "Installed at "+bin)

	// Step 5 — Smoke test.
	t.appendLog("Smoke test: " + bin + " -version")
	emit("smoke", "running", "Running "+bin+" -version")
	smokeOut, smokeErr := runExec(client, bin+" -version 2>&1")
	if smokeErr != nil || strings.TrimSpace(smokeOut) == "" {
		// Fall back to -h: a clean exit + "sftp-loadtest" mention is enough.
		t.appendLog("(-version unavailable, falling back to -h)")
		emit("smoke", "running", "-version unavailable, falling back to -h")
		hOut, hErr := runExec(client, bin+" -h 2>&1; true")
		if hErr != nil {
			return finish("smoke", fmt.Errorf("smoke test failed: %w", hErr))
		}
		if !strings.Contains(strings.ToLower(hOut), "sftp-loadtest") {
			return finish("smoke", fmt.Errorf("smoke test: -h output did not contain 'sftp-loadtest'"))
		}
	}
	emit("smoke", "ok", "Binary runs")

	// Step 6 — Spawn the worker. nohup detaches; the exec channel returns
	// immediately because we redirect both stdout and stderr to the log.
	t.appendLog("Spawning worker on " + bindAddr)
	emit("spawn-process", "running", "nohup worker → "+bindAddr)
	spawnCmd := fmt.Sprintf(
		"nohup %s -addr %s -insecure-host-key > /tmp/sftp-loadtest.log 2>&1 &",
		bin, bindAddr,
	)
	if _, err := runExec(client, spawnCmd); err != nil {
		return finish("spawn-process", fmt.Errorf("spawn: %w", err))
	}
	emit("spawn-process", "ok", "Worker process detached")

	// Step 7 — Wait for the worker to accept connections on the remote.
	t.appendLog("Waiting for worker to be ready on " + bindAddr)
	emit("wait-ready", "running", "Probing "+bindAddr)
	if err := waitRemoteReady(ctx, client, bindAddr, 5*time.Second); err != nil {
		return finish("wait-ready", fmt.Errorf("worker did not become ready: %w", err))
	}
	emit("wait-ready", "ok", "Worker bound to "+bindAddr)

	// Step 8 — Open the master-side reverse tunnel.
	emit("tunnel-listener", "running", "Opening loopback listener")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return finish("tunnel-listener", fmt.Errorf("local listen: %w", err))
	}
	t.listener = listener
	localAddr := listener.Addr().String()
	t.LocalURL = "http://" + localAddr
	t.appendLog("Tunnel ready: " + t.LocalURL + " → " + bindAddr)

	tunCtx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.wg.Add(1)
	go t.acceptLoop(tunCtx)

	emit("tunnel-listener", "ok", "Tunnel ready: "+t.LocalURL)
	return t, nil
}

// dialSSHContext lets a caller-supplied context cancel an otherwise
// blocking SSH dial. The crypto/ssh API doesn't take a context, so we
// race the dial against ctx.Done().
func dialSSHContext(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	type result struct {
		c   *ssh.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ssh.Dial("tcp", addr, cfg)
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.err
	}
}

func buildAuth(opts SpawnOpts) ([]ssh.AuthMethod, error) {
	if opts.PrivateKeyPEM != "" {
		signer, err := sftpx.ParsePrivateKey([]byte(opts.PrivateKeyPEM), opts.Passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return []ssh.AuthMethod{ssh.Password(opts.Password)}, nil
}

// PreflightResult is what Preflight returns — a structured snapshot of
// "can the master actually do this?" answered before the operator
// commits to a full Spawn. Each field is true iff the corresponding
// remote check came back clean. The Log mirrors Spawn's step-by-step
// log so the UI can render it identically.
type PreflightResult struct {
	OK        bool   `json:"ok"`
	Reachable bool   `json:"reachable"`      // SSH dial + auth succeeded
	Arch      string `json:"arch,omitempty"` // detected platform-arch
	CanWrite  bool   `json:"can_write"`      // remote bin path's parent is writable
	HasCurl   bool   `json:"has_curl"`       // download method requires curl + unzip
	HasUnzip  bool   `json:"has_unzip"`
	WhoAmI    string `json:"whoami,omitempty"` // remote `id -un`
	Hostname  string `json:"hostname,omitempty"`
	// PasswordAuthDisabled is true on darwin remotes whose sshd_config
	// pins PasswordAuthentication to "no". macOS ships with this default
	// since Ventura — operators trying to spawn with password auth will
	// see a 15-second hang then "unable to authenticate" otherwise. The
	// wizard reads this flag on Step S2 and warns BEFORE the spawn.
	PasswordAuthDisabled bool     `json:"password_auth_disabled,omitempty"`
	Log                  []string `json:"log"`
	Error                string   `json:"error,omitempty"`
}

// Preflight dials SSH with the same auth + host-key behaviour Spawn uses,
// runs a handful of read-only checks, and returns a structured answer.
// No state is mutated on the remote — no install, no spawn, no pkill.
// Operators run this BEFORE Spawn to verify the remote is reachable, the
// credentials work, the install path is writable, and (for download
// method) curl + unzip are present.
func Preflight(ctx context.Context, opts SpawnOpts) (*PreflightResult, error) {
	if opts.Host == "" {
		return nil, errors.New("host required")
	}
	if opts.User == "" {
		return nil, errors.New("user required")
	}
	if opts.Password == "" && opts.PrivateKeyPEM == "" {
		return nil, errors.New("either Password or PrivateKeyPEM required")
	}
	port := opts.Port
	if port == "" {
		port = "22"
	}
	bin := opts.RemoteBinaryPath
	if bin == "" {
		bin = "/tmp/sftp-loadtest"
	}
	res := &PreflightResult{}
	appendLog := func(s string) { res.Log = append(res.Log, s) }

	appendLog("Dialing SSH " + opts.Host + ":" + port + " as " + opts.User)
	auth, err := buildAuth(opts)
	if err != nil {
		res.Error = "auth: " + err.Error()
		appendLog("✗ auth setup: " + err.Error())
		return res, nil
	}
	hk := opts.HostKey
	if hk == nil {
		hk = ssh.InsecureIgnoreHostKey()
	}
	cfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         15 * time.Second,
	}
	sshAddr := net.JoinHostPort(opts.Host, port)

	dialDone := make(chan struct{})
	var client *ssh.Client
	var dialErr error
	go func() {
		defer close(dialDone)
		client, dialErr = ssh.Dial("tcp", sshAddr, cfg)
	}()
	select {
	case <-ctx.Done():
		res.Error = "ssh dial cancelled: " + ctx.Err().Error()
		appendLog("✗ ssh dial cancelled")
		return res, nil
	case <-dialDone:
	}
	if dialErr != nil {
		res.Error = "ssh dial: " + dialErr.Error()
		appendLog("✗ ssh dial: " + dialErr.Error())
		return res, nil
	}
	defer client.Close()
	res.Reachable = true
	appendLog("✓ ssh dial + auth ok")

	// Detect arch — same uname trick Spawn uses.
	if out, runErr := runExec(client, "uname -s -m"); runErr == nil && out != "" {
		if arch, mapErr := mapArch(out); mapErr == nil {
			res.Arch = arch
			appendLog("✓ remote arch: " + arch + " (" + strings.TrimSpace(out) + ")")
		} else {
			appendLog("✗ uname returned unsupported platform: " + strings.TrimSpace(out))
		}
	} else if runErr != nil {
		appendLog("✗ uname failed: " + runErr.Error())
	}

	// Identity — useful diagnostic for the operator.
	if out, err := runExec(client, "id -un"); err == nil {
		res.WhoAmI = strings.TrimSpace(out)
		appendLog("✓ remote whoami: " + res.WhoAmI)
	}
	if out, err := runExec(client, "hostname"); err == nil {
		res.Hostname = strings.TrimSpace(out)
		appendLog("✓ remote hostname: " + res.Hostname)
	}

	// macOS: probe sshd_config for PasswordAuthentication. Catches the
	// "default macOS sshd refuses passwords" gotcha BEFORE the operator
	// burns 15 seconds on a hanging dial.
	if strings.HasPrefix(res.Arch, "darwin-") {
		res.PasswordAuthDisabled = detectMacPasswordAuthDisabled(client)
		if res.PasswordAuthDisabled {
			appendLog("⚠ macOS sshd has PasswordAuthentication disabled — switch to a private key")
		}
	}

	// Write check on the install path's parent. If the operator chose a
	// custom path (e.g. /opt/sftp-loadtest), this is where we confirm
	// they have permission to drop the binary there.
	parent := bin
	if i := strings.LastIndex(bin, "/"); i > 0 {
		parent = bin[:i]
	} else {
		parent = "/tmp"
	}
	writeCmd := fmt.Sprintf("test -d %s && test -w %s && echo OK", parent, parent)
	if out, err := runExec(client, writeCmd); err == nil && strings.TrimSpace(out) == "OK" {
		res.CanWrite = true
		appendLog("✓ install path writable: " + parent)
	} else {
		appendLog("✗ install path NOT writable: " + parent + " (need a directory the operator's user can write)")
	}

	// curl + unzip — required only for the download install method, but
	// always check so the UI can warn the operator they'll have to switch
	// to upload mode if the binaries aren't present.
	if out, err := runExec(client, "command -v curl"); err == nil && strings.TrimSpace(out) != "" {
		res.HasCurl = true
		appendLog("✓ curl available")
	} else {
		appendLog("✗ curl not found (required for download install method)")
	}
	if out, err := runExec(client, "command -v unzip"); err == nil && strings.TrimSpace(out) != "" {
		res.HasUnzip = true
		appendLog("✓ unzip available")
	} else {
		appendLog("✗ unzip not found (required for download install method)")
	}

	res.OK = res.Reachable && res.Arch != "" && res.CanWrite
	if res.OK {
		appendLog("Preflight passed — ready to spawn.")
	} else {
		appendLog("Preflight INCOMPLETE — fix the failing checks above before spawning.")
	}
	return res, nil
}

// runExec opens a fresh exec channel, runs cmd, and returns the combined
// stdout+stderr trimmed. A non-zero exit status from the command is NOT
// treated as a hard error here — the caller decides whether the output is
// meaningful (e.g. pkill returning non-zero when nothing matches is fine).
// Channel-level errors (couldn't open, couldn't start) are surfaced.
func runExec(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if err := sess.Run(cmd); err != nil {
		// Combine streams so the caller can see whatever the remote
		// printed before the failure. We deliberately don't return err
		// itself for non-zero exits: callers can inspect the output.
		out := strings.TrimSpace(stdout.String() + stderr.String())
		if out != "" {
			return out, nil
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String() + stderr.String()), nil
}

// waitRemoteReady probes 127.0.0.1:<bindAddr> through the SSH client by
// opening a direct-tcpip channel. Each probe has a 1 s timeout; the
// outer deadline is `total`.
func waitRemoteReady(ctx context.Context, client *ssh.Client, bindAddr string, total time.Duration) error {
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := client.Dial("tcp", bindAddr)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", total)
}

// acceptLoop is the master-side accept loop for the reverse tunnel. Each
// accepted connection is paired with a fresh ssh.Client.Dial("tcp", ...)
// channel and bidirectional io.Copy stitches them together. When the
// context cancels, the listener is closed, which unblocks Accept and
// drains the loop.
func (t *Tunnel) acceptLoop(ctx context.Context) {
	defer t.wg.Done()
	go func() {
		<-ctx.Done()
		_ = t.listener.Close()
	}()
	for {
		local, err := t.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Transient accept errors are rare — log via the tunnel's
			// SpawnLog so the operator sees them in the modal even after
			// initial spawn finishes.
			t.appendLog("accept error: " + err.Error())
			return
		}
		t.wg.Add(1)
		go func(local net.Conn) {
			defer t.wg.Done()
			defer local.Close()
			remote, err := t.sshClient.Dial("tcp", t.RemoteAddr)
			if err != nil {
				t.appendLog("ssh dial " + t.RemoteAddr + ": " + err.Error())
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
			go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
			<-done
		}(local)
	}
}

// Close shuts everything down. Idempotent.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel := t.cancel
	listener := t.listener
	sshClient := t.sshClient
	sftpClient := t.sftpClient
	bindAddr := t.RemoteAddr
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if listener != nil {
		_ = listener.Close()
	}
	if sshClient != nil {
		// Best-effort kill of the remote worker. We can't reuse the
		// context here (it's already cancelled).
		killCmd := fmt.Sprintf("pkill -f 'sftp-loadtest -addr %s' 2>/dev/null; true", regexEscape(bindAddr))
		_, _ = runExec(sshClient, killCmd)
	}
	if sftpClient != nil {
		_ = sftpClient.Close()
	}
	if sshClient != nil {
		_ = sshClient.Close()
	}
	t.wg.Wait()
	return nil
}

func (t *Tunnel) appendLog(s string) {
	t.mu.Lock()
	t.SpawnLog = append(t.SpawnLog, s)
	t.mu.Unlock()
}

// uploadBinary streams local→remote and chmods to 0o755.
func uploadBinary(sc *sftp.Client, local, remote string) error {
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()
	// Remove first so a stale binary from a previous run can't shadow
	// this upload's permissions.
	_ = sc.Remove(remote)
	dst, err := sc.Create(remote)
	if err != nil {
		return fmt.Errorf("create remote %s: %w", remote, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close remote: %w", err)
	}
	if err := sc.Chmod(remote, 0o755); err != nil {
		return fmt.Errorf("chmod 0755: %w", err)
	}
	return nil
}

// mapArch parses `uname -s -m` output into our canonical arch string.
// Anything we don't ship a binary for is rejected with a clear error so
// the operator doesn't get a confusing download-404 later.
func mapArch(unameOut string) (string, error) {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(unameOut)))
	if len(parts) < 2 {
		return "", fmt.Errorf("uname output unrecognised: %q", unameOut)
	}
	osName, machine := parts[0], parts[1]
	switch osName {
	case "linux":
		switch machine {
		case "x86_64", "amd64":
			return "linux-amd64", nil
		case "aarch64", "arm64":
			return "linux-arm64", nil
		}
	case "darwin":
		switch machine {
		case "arm64":
			return "darwin-arm64", nil
		case "x86_64", "amd64":
			return "darwin-amd64", nil
		}
	}
	// Windows via Git Bash / Cygwin / MSYS prints e.g.
	//   MINGW64_NT-10.0 x86_64
	//   MSYS_NT-10.0    x86_64
	//   CYGWIN_NT-10.0  x86_64
	// — the "-<version>" suffix moves with the kernel build, so prefix-
	// match instead of an exact compare.
	if strings.HasPrefix(osName, "mingw") ||
		strings.HasPrefix(osName, "msys") ||
		strings.HasPrefix(osName, "cygwin") ||
		strings.HasPrefix(osName, "windows") {
		if machine == "x86_64" || machine == "amd64" {
			return "windows-amd64", nil
		}
	}
	return "", fmt.Errorf("unsupported remote OS/arch %q (need linux/darwin amd64-or-arm64, or windows amd64)", unameOut)
}

// assetSuffixForArch maps the canonical arch back to the asset-naming
// convention used by the GitHub releases (the same suffixes the website
// shows in the Pre-built binaries table).
func assetSuffixForArch(arch string) (string, error) {
	switch arch {
	case "darwin-arm64":
		return "mac-apple-silicon", nil
	case "darwin-amd64":
		return "mac-intel", nil
	case "linux-amd64":
		return "linux-amd64", nil
	case "linux-arm64":
		return "linux-arm64", nil
	case "windows-amd64":
		return "windows-amd64", nil
	}
	return "", fmt.Errorf("no release asset known for arch %q", arch)
}

// detectMacPasswordAuthDisabled reads /etc/ssh/sshd_config on a darwin
// remote and returns true iff the last PasswordAuthentication directive
// resolves to "no". macOS Ventura+ ships with this default — the wizard
// uses the result to warn the operator before they click Spawn.
//
// Returns false on any read error (file unreadable / sudo required) — we
// don't want to spook the operator when we genuinely don't know.
func detectMacPasswordAuthDisabled(client *ssh.Client) bool {
	out, err := runExec(client, "grep -E '^[[:space:]]*PasswordAuthentication' /etc/ssh/sshd_config 2>/dev/null | tail -n1")
	if err != nil {
		return false
	}
	return parsePasswordAuthLine(out)
}

// parsePasswordAuthLine returns true iff the (possibly empty) sshd_config
// line resolves to a literal `PasswordAuthentication no`. Commented-out
// lines and the empty string return false — we only flag explicit "no".
// Extracted into its own helper so the test suite can exercise the
// parsing branches without standing up a fake SSH server.
func parsePasswordAuthLine(grepOut string) bool {
	line := strings.ToLower(strings.TrimSpace(grepOut))
	if line == "" {
		return false
	}
	// Commented-out config: platform default applies. On macOS Ventura+
	// that default is "no", but we don't claim certainty without an
	// explicit setting, so we don't flag it.
	if strings.HasPrefix(line, "#") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "passwordauthentication" && fields[1] == "no" {
		return true
	}
	return false
}

// regexEscape escapes a literal so it's safe to embed inside a pkill -f
// pattern. We only need to escape the characters pkill / regex-extended
// treats specially in our use case (port colon, dots).
func regexEscape(s string) string {
	r := strings.NewReplacer(
		`.`, `\.`,
		`+`, `\+`,
		`*`, `\*`,
		`?`, `\?`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`|`, `\|`,
		`^`, `\^`,
		`$`, `\$`,
	)
	return r.Replace(s)
}
