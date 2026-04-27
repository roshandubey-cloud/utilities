package sftpx

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
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
	strict, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", path, err)
	}
	var mu sync.Mutex // serialises appends so concurrent first-time dials don't race the file
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Try strict verification first. On a successful match this is the only
		// thing that runs — already-known servers are silent, no re-prompt.
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
			// Host not seen before. Append + accept.
			mu.Lock()
			defer mu.Unlock()
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

// DialOpts customises a single Dial call. A zero value uses the
// process-wide host-key callback (the one main.go installs at startup).
type DialOpts struct {
	// HostKeyCallback, if set, is used instead of the process-wide callback
	// for this one connection. Used by /api/probe in TOFU mode so the probe
	// can capture a new server's key without affecting in-flight runs.
	HostKeyCallback ssh.HostKeyCallback
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

// DialWithOpts is the same as Dial but lets the caller override the
// host-key callback for this single connection (used by /api/probe TOFU).
func DialWithOpts(host string, port int, user, pass string, opts DialOpts) (*Client, error) {
	cb := opts.HostKeyCallback
	if cb == nil {
		cb = currentCallback()
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: cb,
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	sshc, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	// Use pkg/sftp defaults everywhere — no packet-size tuning, no concurrency
	// overrides. Servers that advertise a 32 KiB max-packet limit drop connections
	// when we push bigger frames, so staying on defaults is the compatible choice.
	sc, err := sftp.NewClient(sshc)
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
