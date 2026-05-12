package sftpx

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyMode controls how SSH host keys are checked when dialing the SFTP
// server. The package-level variables let main.go install a process-wide
// callback at startup so the rest of the codebase doesn't need to know how
// host-key verification was configured.
var (
	hostKeyCallback ssh.HostKeyCallback
	hostKeyMu       sync.RWMutex
)

// SetHostKeyCallback installs the host-key verification callback used by all
// subsequent Dial() calls. Pass nil to revert to the default secure-by-failure
// behavior (refuse every connection).
func SetHostKeyCallback(cb ssh.HostKeyCallback) {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	hostKeyCallback = cb
}

// UseKnownHosts loads the OpenSSH-format known_hosts file and installs a
// strict-checking callback. Returns an error if the file is missing or
// malformed. This is the recommended production setup.
func UseKnownHosts(path string) error {
	cb, err := knownhosts.New(path)
	if err != nil {
		return fmt.Errorf("load known_hosts %s: %w", path, err)
	}
	SetHostKeyCallback(cb)
	return nil
}

// AllowAnyHostKey installs the explicitly-insecure callback. Intended only for
// throwaway lab tests against ephemeral SFTP servers; emits a warning to the
// caller-supplied logger so it can never be silent.
func AllowAnyHostKey(warn func(format string, args ...any)) {
	if warn != nil {
		warn("WARNING: SSH host-key verification disabled (--insecure-host-key). " +
			"This SFTP load tester is now vulnerable to man-in-the-middle attacks. " +
			"Use -known-hosts <path> in any environment that touches real credentials.")
	}
	SetHostKeyCallback(ssh.InsecureIgnoreHostKey())
}

// CurrentCallback exposes the process-wide host-key callback so callers
// outside this package (the bastion package, in particular) can reuse
// the same TOFU store and policy the SFTP target dials use. v0.16.0
// added this when bastion / ProxyJump support landed.
func CurrentCallback() ssh.HostKeyCallback { return currentCallback() }

// currentCallback returns the installed callback or a refuse-all fallback.
func currentCallback() ssh.HostKeyCallback {
	hostKeyMu.RLock()
	cb := hostKeyCallback
	hostKeyMu.RUnlock()
	if cb == nil {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return errors.New("host-key verification not configured: pass -known-hosts <path> or -insecure-host-key")
		}
	}
	return cb
}

// TOFUCallback returns a host-key callback that verifies against the given
// known_hosts file *and* — only when the host is unrecognized — appends the
// presented key and accepts. A KEY MISMATCH for an already-known host is
// always refused; TOFU only handles "not seen yet", never "key changed".
//
// This implements the OpenSSH `StrictHostKeyChecking=accept-new` model. The
// captured callback is invoked with (hostname, sha256-fingerprint) for any
// freshly-added key so the caller can surface it (e.g. in the probe response)
// for the operator to verify out-of-band.
//
// The known_hosts file is created (mode 0o600) if it doesn't exist. Returns
// an error only if the path can't be created or the existing file is
// malformed enough that even strict checking can't be initialised.
func TOFUCallback(path string, captured func(host, fingerprint string)) (ssh.HostKeyCallback, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			return nil, fmt.Errorf("create known_hosts %s: %w", path, err)
		}
	}
	// Verify path is readable now so misconfiguration surfaces at setup time
	// rather than at first dial. The actual strict view is loaded fresh on
	// every callback invocation below to avoid a stale-snapshot race when
	// concurrent first-time probes append to the same known_hosts file.
	if _, err := knownhosts.New(path); err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", path, err)
	}
	var mu sync.Mutex // serialises strict-check + append so concurrent first-time dials cannot race
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		// Reload the strict view fresh inside the critical section: between
		// callback creation and now, another concurrent probe may have
		// appended a new key for THIS host. Without reload we'd reject a
		// key that's actually already trusted.
		strict, sErr := knownhosts.New(path)
		if sErr != nil {
			return fmt.Errorf("reload known_hosts %s: %w", path, sErr)
		}
		strictErr := strict(hostname, remote, key)
		if strictErr == nil {
			return nil
		}
		var keErr *knownhosts.KeyError
		if errors.As(strictErr, &keErr) {
			if len(keErr.Want) > 0 {
				// Host known + a different key seen. NEVER auto-fix — this is
				// the MITM signal we want to be loud about.
				return fmt.Errorf("host key for %s has changed since the last connection — possible MITM, refusing (delete the offending line in %s only after verifying the new key out-of-band): %w",
					hostname, path, strictErr)
			}
			// Host not seen before. Append + accept. Mutex is already held.
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
			f, ferr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return fmt.Errorf("append %s to %s: %w", knownhosts.Normalize(hostname), path, ferr)
			}
			if _, werr := f.WriteString(line); werr != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", path, werr)
			}
			if cerr := f.Close(); cerr != nil {
				return fmt.Errorf("close %s: %w", path, cerr)
			}
			if captured != nil {
				captured(hostname, ssh.FingerprintSHA256(key))
			}
			return nil
		}
		return strictErr
	}, nil
}

// ErrHostKeyConsentRequired is returned by CapturePreviewCallback when the
// presented host key is not in known_hosts. The callback also captures the
// key's fingerprint via the closure so the caller can show it to the user
// for explicit Accept/Reject. This is the "interactive TOFU" workflow —
// distinct from the auto-add TOFU implemented by TOFUCallback.
var ErrHostKeyConsentRequired = errors.New("host key not in known_hosts; user consent required")

// ErrHostKeyChanged is returned by CapturePreviewCallback when the presented
// host key DIFFERS from what's recorded in known_hosts. This is the OpenSSH
// "REMOTE HOST IDENTIFICATION HAS CHANGED" case — almost always a server
// rebuild but classically the man-in-the-middle signal. The callback
// captures BOTH the old and new fingerprints via the closure so the caller
// can present a high-friction prompt before overwriting.
var ErrHostKeyChanged = errors.New("host key has changed; user consent required to overwrite")

// CapturedKey is what CapturePreviewCallback feeds back to its caller via
// the captured-key closure. Previous is empty for first-time hosts; populated
// (with the SHA-256 of the previously-trusted key) on the changed-key path.
type CapturedKey struct {
	Host        string
	Fingerprint string
	Previous    string
	Changed     bool
}

// CapturePreviewCallback returns a host-key callback that:
//   - Strict-checks the presented key against the known_hosts file.
//   - On success: returns nil (key is already trusted).
//   - On a "key changed" mismatch: returns the loud MITM error (never
//     auto-fixes).
//   - On "host not seen yet": captures the key's SHA-256 fingerprint via
//     the closure AND returns ErrHostKeyConsentRequired without modifying
//     the file. The caller surfaces the fingerprint to the user, who can
//     then opt in by re-running the probe with TrustOnFirstUse: true (which
//     uses TOFUCallback to actually append).
//
// The known_hosts file is created (mode 0o600) if missing — same as
// TOFUCallback — so the very first probe against any server can populate it.
func CapturePreviewCallback(path string, captured func(CapturedKey)) (ssh.HostKeyCallback, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			return nil, fmt.Errorf("create known_hosts %s: %w", path, err)
		}
	}
	// Validate readability now; reload on every call below for the same
	// reason as TOFUCallback — a concurrent TOFU accept may append a key
	// for this host between callback creation and our check.
	if _, err := knownhosts.New(path); err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", path, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		strict, sErr := knownhosts.New(path)
		if sErr != nil {
			return fmt.Errorf("reload known_hosts %s: %w", path, sErr)
		}
		strictErr := strict(hostname, remote, key)
		if strictErr == nil {
			return nil
		}
		var keErr *knownhosts.KeyError
		if errors.As(strictErr, &keErr) {
			if len(keErr.Want) > 0 {
				// Changed key (OpenSSH "REMOTE HOST IDENTIFICATION HAS CHANGED").
				// Capture BOTH the previously-trusted and the newly-presented
				// fingerprints so the caller can show a high-friction prompt;
				// do NOT modify the file ourselves.
				prevFP := ""
				if len(keErr.Want) > 0 && keErr.Want[0].Key != nil {
					prevFP = ssh.FingerprintSHA256(keErr.Want[0].Key)
				}
				if captured != nil {
					captured(CapturedKey{
						Host:        hostname,
						Fingerprint: ssh.FingerprintSHA256(key),
						Previous:    prevFP,
						Changed:     true,
					})
				}
				return ErrHostKeyChanged
			}
			// Unknown host: capture the fingerprint so the caller can prompt
			// the user, but DO NOT modify known_hosts. Return the sentinel.
			if captured != nil {
				captured(CapturedKey{
					Host:        hostname,
					Fingerprint: ssh.FingerprintSHA256(key),
				})
			}
			return ErrHostKeyConsentRequired
		}
		return strictErr
	}, nil
}

// RemoveKnownHostEntries rewrites the known_hosts file with every line for
// the given hostname removed. Used by the changed-key consent flow: the UI
// asks the operator to confirm the key has truly rotated; on accept the old
// entry is wiped and a TOFUCallback re-add captures the new one. Matching is
// done against the OpenSSH "[host]:port" / "host" form via knownhosts.Normalize
// so a hostname rewrite via Normalize matches both "127.0.0.1:2222" → "[127.0.0.1]:2222".
func RemoveKnownHostEntries(path, hostname string) error {
	target := knownhosts.Normalize(hostname)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read known_hosts %s: %w", path, err)
	}
	var out []byte
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line...)
			continue
		}
		// First field is one or more comma-separated host patterns.
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			out = append(out, line...)
			continue
		}
		hosts := strings.Split(fields[0], ",")
		matched := false
		for _, h := range hosts {
			if strings.TrimSpace(h) == target {
				matched = true
				break
			}
		}
		if matched {
			continue // drop this line
		}
		out = append(out, line...)
	}
	tmp := path + ".tmp"
	if werr := os.WriteFile(tmp, out, 0o600); werr != nil {
		return fmt.Errorf("write %s: %w", tmp, werr)
	}
	return os.Rename(tmp, path)
}

// DialOpts customises a single Dial call. A zero value uses the
// process-wide host-key callback (the one main.go installs at startup).
type DialOpts struct {
	// HostKeyCallback, if set, is used instead of the process-wide callback
	// for this one connection. Used by /api/probe in TOFU mode so the probe
	// can capture a new server's key without affecting in-flight runs.
	HostKeyCallback ssh.HostKeyCallback

	// Auth, if non-empty, is used in place of the password fallback. Lets
	// callers wire ssh.PublicKeys (or any other ssh.AuthMethod) for runs
	// that authenticate with a private key instead of a password. Empty
	// means "fall back to ssh.Password(pass)" — preserving the behaviour
	// every existing caller already gets.
	Auth []ssh.AuthMethod

	// HostKeyAlgorithms / KeyExchanges (v0.16.0) thread the named
	// quirk profile's SSH algorithm overrides into ssh.ClientConfig.
	// Nil = leave library defaults in place (modern algorithms only).
	// Non-nil = explicit ordered preference list — typically used to
	// re-enable legacy algorithms (ssh-rsa, dh-group14-sha1) for old
	// sshd installs that haven't been upgraded.
	HostKeyAlgorithms []string
	KeyExchanges      []string

	// BastionDialer (v0.16.0), if non-nil, is used to dial the target
	// SSH endpoint instead of net.Dial. The bastion package builds
	// this from the operator-supplied jump-host config; the SFTP path
	// stays bastion-agnostic and just uses whatever Conn the dialer
	// produces. nil means "direct dial" — the legacy behaviour.
	BastionDialer func(network, addr string) (net.Conn, error)
}

// ParsePrivateKey parses a PEM-encoded SSH private key, decrypting it with
// passphrase when one is supplied. Returns cleanly-wrapped errors for the
// three failure modes operators actually hit (bad PEM, bad passphrase,
// unsupported key type) so the UI can surface a usable message instead of
// the raw library error string.
func ParsePrivateKey(pem []byte, passphrase string) (ssh.Signer, error) {
	if len(pem) == 0 {
		return nil, errors.New("private key is empty")
	}
	var (
		signer ssh.Signer
		err    error
	)
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	if err == nil {
		return signer, nil
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "decryption password"), strings.Contains(low, "incorrect passphrase"), strings.Contains(low, "x509: decryption password"):
		return nil, fmt.Errorf("private key passphrase is incorrect")
	case strings.Contains(low, "passphrase protected"), strings.Contains(low, "encrypted"):
		return nil, fmt.Errorf("private key is encrypted — supply a passphrase")
	case strings.Contains(low, "no key found"), strings.Contains(low, "pem"):
		return nil, fmt.Errorf("private key PEM is malformed (no key block found)")
	case strings.Contains(low, "unsupported key type"), strings.Contains(low, "unknown key type"):
		return nil, fmt.Errorf("private key type is not supported (use ed25519, RSA, or ECDSA)")
	default:
		return nil, fmt.Errorf("private key parse failed: %s", msg)
	}
}

// keepaliveInterval is how often we send an out-of-band keepalive request
// to the SSH server. 30 s is comfortably below every common idle-timeout
// (sshd defaults to 120 s, load balancers often enforce 60 s) but far enough
// apart that it's not meaningful traffic.
const keepaliveInterval = 30 * time.Second

type Client struct {
	ssh      *ssh.Client
	sftp     *sftp.Client
	stopCh   chan struct{}
	closeOnce sync.Once
}

func Dial(host string, port int, user, pass string) (*Client, error) {
	return DialWithOpts(host, port, user, pass, DialOpts{})
}

// PasswordAuthMethods returns the two auth methods that together
// approximate what every third-party SFTP client (FileZilla, WinSCP,
// OpenSSH) does when given a password: try password auth, AND offer
// to satisfy a keyboard-interactive challenge with the same password.
//
// Many enterprise SFTP gateways (Progress MoveIT Transfer, Tectia
// SSH Server, IBM Sterling Connect:Direct, JSCAPE, GlobalSCAPE) do
// not advertise `password` as a supported method on the wire — they
// advertise only `keyboard-interactive`, then immediately ask a
// single "Password:" prompt as the first KI question. Go's
// golang.org/x/crypto/ssh does NOT auto-fall-back: if the only auth
// method in ClientConfig.Auth is ssh.Password and the server doesn't
// list `password` in `userauth_failure`, the dial fails with "unable
// to authenticate, attempted methods [password], no supported
// methods remain".
//
// Including both methods is the standard "well-behaved client"
// posture and matches what every operator's third-party tool already
// does. The KI responder answers EVERY prompt with the supplied
// password — when the prompt is asking for the password (the
// universal case) this is correct; when the server asks for
// something exotic (e.g. an OTP) the response is just wrong and the
// server fails the auth cleanly. Either outcome is no worse than
// the password-only behaviour we had before; the common case
// (password challenge wrapped in KI) now succeeds.
func PasswordAuthMethods(pass string) []ssh.AuthMethod {
	return passwordAuthMethods(pass)
}

func passwordAuthMethods(pass string) []ssh.AuthMethod {
	ki := ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = pass
		}
		return answers, nil
	})
	return []ssh.AuthMethod{ssh.Password(pass), ki}
}

// DialWithOpts is the same as Dial but lets the caller override the
// host-key callback for this single connection (used by /api/probe TOFU).
func DialWithOpts(host string, port int, user, pass string, opts DialOpts) (*Client, error) {
	cb := opts.HostKeyCallback
	if cb == nil {
		cb = currentCallback()
	}
	auth := opts.Auth
	if len(auth) == 0 {
		auth = passwordAuthMethods(pass)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: cb,
		Timeout:         15 * time.Second,
	}
	// v0.16.0 — quirk profiles. When the caller supplies an explicit
	// algorithm list, set it on ClientConfig so legacy servers
	// (ssh-rsa host keys, dh-group14-sha1 KEX) can negotiate. Nil
	// preserves the library's modern defaults.
	if len(opts.HostKeyAlgorithms) > 0 {
		cfg.HostKeyAlgorithms = opts.HostKeyAlgorithms
	}
	if len(opts.KeyExchanges) > 0 {
		cfg.Config.KeyExchanges = opts.KeyExchanges
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	// v0.16.0 — bastion / ProxyJump. When BastionDialer is set, dial
	// the underlying TCP via the bastion's open SSH session
	// (ssh.Client.Dial); then negotiate the target SSH layer over
	// that net.Conn. nil dialer = direct dial (legacy behaviour).
	var sshc *ssh.Client
	if opts.BastionDialer != nil {
		conn, derr := opts.BastionDialer("tcp", addr)
		if derr != nil {
			return nil, fmt.Errorf("bastion dial %s: %w", addr, derr)
		}
		sc, chans, reqs, herr := ssh.NewClientConn(conn, addr, cfg)
		if herr != nil {
			conn.Close()
			return nil, fmt.Errorf("ssh handshake via bastion %s: %w", addr, herr)
		}
		sshc = ssh.NewClient(sc, chans, reqs)
	} else {
		var err error
		sshc, err = ssh.Dial("tcp", addr, cfg)
		if err != nil {
			return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
		}
	}
	// v0.19.0 — enable pipelined writes. pkg/sftp's default behaviour
	// is to ack each WRITE packet before sending the next, which makes
	// per-file throughput round-trip-bound on any non-trivial latency
	// (the partner's library doc itself says "not using them will
	// degrade performance"). Pipelining sends multiple write requests
	// without waiting per-ack — 2-5x per-file throughput on long-haul
	// links, no behaviour change on localhost.
	//
	// Safety in our setting: pkg/sftp warns "if you receive an error
	// during io.Copy you may need to Truncate the target". We don't —
	// failed uploads are already marked Incomplete with a stable
	// ErrorCode and never read back as authoritative; if a partner
	// renames a partial file (rare), the optional SHA-256 round-trip
	// verifier correctly detects it as HASH_MISMATCH (the right
	// outcome).
	//
	// Packet size and per-file concurrency stay on pkg/sftp defaults
	// (32 KiB / 64 in-flight requests). Some servers cap at 32 KiB; we
	// don't probe the cap automatically yet, so leave room for a future
	// quirk-profile knob to bump it on modern OpenSSH.
	sc, err := sftp.NewClient(sshc, sftp.UseConcurrentWrites(true))
	if err != nil {
		sshc.Close()
		return nil, fmt.Errorf("sftp open: %w", err)
	}
	c := &Client{ssh: sshc, sftp: sc, stopCh: make(chan struct{})}
	go c.keepalive()
	return c, nil
}

// keepalive sends SSH out-of-band keepalive requests every keepaliveInterval
// so servers + middleboxes don't silently idle-close the connection during
// long load tests. Exits the first time the server fails to reply — the next
// operation on this Client will surface the real error, and the pool's
// reconnect path will redial.
func (c *Client) keepalive() {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			// Out-of-band global request; payload intentionally empty.
			if _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// Upload streams src to the remote path. The returned stage pinpoints where
// a failure happened (empty string on success): "create" for the initial
// open, "write" for mid-stream I/O, "close" for a failed flush/close. Lets
// callers attach a stable ErrorCode to the failure record.
func (c *Client) Upload(remotePath string, src io.Reader) (int64, string, error) {
	f, err := c.sftp.Create(remotePath)
	if err != nil {
		return 0, "create", fmt.Errorf("create %s: %w", remotePath, err)
	}
	n, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return n, "write", copyErr
	}
	if closeErr != nil {
		return n, "close", closeErr
	}
	return n, "", nil
}

func (c *Client) List(remotePath string) ([]os.FileInfo, error) {
	return c.sftp.ReadDir(remotePath)
}

// Download streams a remote file into io.Discard. No local storage — we only
// care about byte count and timing for speed measurement.
func (c *Client) Download(remotePath string) (int64, error) {
	f, err := c.sftp.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", remotePath, err)
	}
	defer f.Close()
	return io.Copy(io.Discard, f)
}

// Open returns the remote file as an io.ReadCloser. Used by the protocol
// abstraction's SFTP wrapper so callers that want the bytes (not just a
// throughput count) have a uniform interface across SFTP/FTP/FTPS.
func (c *Client) Open(remotePath string) (io.ReadCloser, error) {
	f, err := c.sftp.Open(remotePath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", remotePath, err)
	}
	return f, nil
}

// Stat returns os.FileInfo for the remote path. Wraps pkg/sftp's Stat so
// the protocol-neutral wrapper can satisfy Conn.Stat.
func (c *Client) Stat(remotePath string) (os.FileInfo, error) {
	return c.sftp.Stat(remotePath)
}

// Remove deletes a remote file. Same purpose as Stat — used by the
// protocol-abstraction wrapper.
func (c *Client) Remove(remotePath string) error {
	return c.sftp.Remove(remotePath)
}

// Close tears down the keepalive goroutine and the SSH/SFTP connections.
// Idempotent via sync.Once so concurrent closes from pool teardown + caller
// paths don't double-close the ssh conn (which pkg/ssh tolerates, but it's
// nicer to not rely on that).
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.stopCh)
		if c.sftp != nil {
			c.sftp.Close()
		}
		if c.ssh != nil {
			err = c.ssh.Close()
		}
	})
	return err
}
