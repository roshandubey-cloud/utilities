// Package protocol unifies the SFTP, FTP, and FTPS client surfaces behind a
// single Conn interface. Added in v0.13.0 when the runner stopped being
// SFTP-only — the runner, watcher, download phase, and probe handler all
// route through Dial() now and stay protocol-agnostic.
//
// SFTP wraps internal/sftpx so the SFTP code path is byte-identical to the
// pre-v0.13 behaviour. FTP uses github.com/jlaffaye/ftp. FTPS reuses the
// same FTP client wrapped with crypto/tls — implicit (TLS from byte 0) or
// explicit (AUTH TLS upgrade) modes selected via DialOpts.TLSMode.
package protocol

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	jlaffayeFTP "github.com/jlaffaye/ftp"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostkeys"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

// Protocol is one connection kind. Empty string is treated as SFTP for
// backwards compat with saved configs/connections from before v0.13.0.
type Protocol string

const (
	SFTP Protocol = "sftp"
	FTP  Protocol = "ftp"
	FTPS Protocol = "ftps"
)

// Normalize maps the empty string and case variants to a canonical Protocol.
// Returns SFTP for unknown values so the runner never silently drops a job
// because of a typo in a saved config.
func Normalize(p string) Protocol {
	switch Protocol(p) {
	case FTP:
		return FTP
	case FTPS:
		return FTPS
	default:
		return SFTP
	}
}

// TLSMode controls how the FTPS client establishes TLS.
//
//   - TLSNone is the default zero value. Only valid when Protocol != FTPS.
//   - TLSImplicit dials TLS from byte 0 (canonical port 990).
//   - TLSExplicit dials plaintext (canonical port 21), greets, then sends
//     AUTH TLS to upgrade the control channel.
type TLSMode int

const (
	TLSNone TLSMode = iota
	TLSImplicit
	TLSExplicit
)

// ParseTLSMode is the string→enum mapping the web layer uses to read the
// JSON request body. Empty string + unknown values fall through to TLSNone.
func ParseTLSMode(s string) TLSMode {
	switch s {
	case "implicit":
		return TLSImplicit
	case "explicit":
		return TLSExplicit
	default:
		return TLSNone
	}
}

// DialOpts unifies what every protocol's Dial needs. Per-protocol fields
// are populated only when relevant; harmless to leave defaults set on
// unrelated protocols.
type DialOpts struct {
	Host string
	Port int
	User string
	Pass string

	// SFTP-specific.
	PrivateKeyPEM      string
	Passphrase         string
	SSHHostKeyCallback ssh.HostKeyCallback // used by /api/probe TOFU/capture
	SSHAuth            []ssh.AuthMethod    // optional pre-built auth (e.g. shared signer)

	// FTPS-specific.
	TLSMode            TLSMode
	InsecureSkipVerify bool   // operator opt-in for self-signed test servers
	TLSServerName      string // SNI override; defaults to Host

	// TLSCaptureCallback is called once with the leaf certificate's SHA-256
	// fingerprint when the FTPS handshake completes. The probe surfaces the
	// fingerprint in its JSON response so the UI can drive a TOFU-style
	// consent prompt for unknown FTPS certs (mirrors the SSH host-key flow).
	TLSCaptureCallback func(fingerprint string)

	// TLSStore, when set, enforces a TOFU-style cert-fingerprint check the
	// same way internal/hostkeys does for SSH. Probe leaves this nil; runs
	// pass it in so a mid-run cert change refuses rather than silently
	// continuing.
	TLSStore *hostkeys.TLSStore

	// Streams hints at per-stream parallelism. Some implementations may
	// size internal pools off this; today only used as documentation.
	Streams int
}

// Conn is the protocol-agnostic client surface used by the runner and
// probe. Implementations forward each method to their concrete client.
type Conn interface {
	Upload(remotePath string, body io.Reader, size int64) (n int64, stage string, err error)
	List(remoteDir string) ([]FileInfo, error)
	Get(remotePath string) (io.ReadCloser, error)
	Remove(remotePath string) error
	// Stat is best-effort — returns ErrNoStat when the protocol can't
	// cheaply produce a file info (some FTP servers reject MLST/SIZE).
	// Callers fall back to List + match by name.
	Stat(remotePath string) (FileInfo, error)
	Close() error
}

// FileInfo is the protocol-neutral info struct. Mode is best effort; FTP
// servers usually don't surface POSIX bits at all so this will be 0 there.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
}

// ErrNoStat is returned by Conn.Stat implementations that can't satisfy the
// call cheaply (FTP without MLST). Callers fall back to List + name match.
var ErrNoStat = errors.New("stat not supported by this protocol")

// Dial returns a protocol-specific Conn. Each protocol has its own
// implementation in this file so callers (runner, watcher, download
// phase, probe) only need to know about the abstract Conn.
//
// The context bounds the dial only — once a Conn is returned, individual
// I/O calls have their own per-protocol timeouts (matches sftpx behaviour).
func Dial(ctx context.Context, p Protocol, opts DialOpts) (Conn, error) {
	switch Normalize(string(p)) {
	case SFTP:
		return dialSFTP(opts)
	case FTP:
		return dialFTP(ctx, opts, false)
	case FTPS:
		return dialFTP(ctx, opts, true)
	default:
		return nil, fmt.Errorf("unknown protocol %q", p)
	}
}

// ----- SFTP implementation -----------------------------------------------

type sftpConn struct {
	c *sftpx.Client
}

func dialSFTP(opts DialOpts) (Conn, error) {
	dialOpts := sftpx.DialOpts{HostKeyCallback: opts.SSHHostKeyCallback, Auth: opts.SSHAuth}
	c, err := sftpx.DialWithOpts(opts.Host, opts.Port, opts.User, opts.Pass, dialOpts)
	if err != nil {
		return nil, err
	}
	return &sftpConn{c: c}, nil
}

func (s *sftpConn) Upload(remotePath string, body io.Reader, size int64) (int64, string, error) {
	return s.c.Upload(remotePath, body)
}

func (s *sftpConn) List(remoteDir string) ([]FileInfo, error) {
	infos, err := s.c.List(remoteDir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(infos))
	for _, fi := range infos {
		out = append(out, FileInfo{
			Name:    fi.Name(),
			Size:    fi.Size(),
			Mode:    fi.Mode(),
			ModTime: fi.ModTime(),
		})
	}
	return out, nil
}

func (s *sftpConn) Get(remotePath string) (io.ReadCloser, error) {
	// sftpx.Client.Download streams to io.Discard. For Conn.Get we need to
	// expose a ReadCloser the runner can drain — open the file directly via
	// the underlying sftp client. The legacy Download() is retained on
	// sftpx.Client for callers that just want byte counts.
	return s.c.Open(remotePath)
}

func (s *sftpConn) Remove(remotePath string) error {
	return s.c.Remove(remotePath)
}

func (s *sftpConn) Stat(remotePath string) (FileInfo, error) {
	fi, err := s.c.Stat(remotePath)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime()}, nil
}

func (s *sftpConn) Close() error { return s.c.Close() }

// ----- FTP / FTPS implementation -----------------------------------------

type ftpConn struct {
	c *jlaffayeFTP.ServerConn

	// cert holds the leaf cert observed during the FTPS handshake (nil for
	// plain FTP). Exposed via TLSPeerCertificate so callers can pin or
	// surface the fingerprint.
	cert *x509.Certificate
}

// TLSPeerCertificate returns the leaf cert presented by an FTPS server.
// Returns nil for plain FTP. Used by the probe to surface the SHA-256 to
// the UI for cert-TOFU consent.
func TLSPeerCertificate(c Conn) *x509.Certificate {
	if fc, ok := c.(*ftpConn); ok {
		return fc.cert
	}
	return nil
}

// Fingerprint is "sha256:" + hex of the cert's DER bytes. Same shape we use
// for SSH host keys via ssh.FingerprintSHA256 (just hex instead of base64
// because the OpenSSL ecosystem standardised on hex for X.509).
func Fingerprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + hex.EncodeToString(sum[:])
}

func dialFTP(ctx context.Context, opts DialOpts, useTLS bool) (Conn, error) {
	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
	dialOpts := []jlaffayeFTP.DialOption{
		jlaffayeFTP.DialWithTimeout(15 * time.Second),
	}
	if ctx != nil {
		dialOpts = append(dialOpts, jlaffayeFTP.DialWithContext(ctx))
	}

	var capturedCert *x509.Certificate
	captureCert := func(state tls.ConnectionState) {
		if len(state.PeerCertificates) > 0 {
			capturedCert = state.PeerCertificates[0]
		}
	}

	if useTLS {
		serverName := opts.TLSServerName
		if serverName == "" {
			serverName = opts.Host
		}
		tlsCfg := &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // operator opt-in for self-signed lab servers
			VerifyConnection: func(state tls.ConnectionState) error {
				captureCert(state)
				if opts.TLSStore != nil {
					if len(state.PeerCertificates) == 0 {
						return errors.New("ftps: server presented no certificate")
					}
					return opts.TLSStore.Verify(opts.Host, opts.Port, state.PeerCertificates[0])
				}
				return nil
			},
		}
		switch opts.TLSMode {
		case TLSImplicit:
			dialOpts = append(dialOpts, jlaffayeFTP.DialWithTLS(tlsCfg))
		case TLSExplicit, TLSNone:
			// Default to explicit (AUTH TLS upgrade on the standard port).
			dialOpts = append(dialOpts, jlaffayeFTP.DialWithExplicitTLS(tlsCfg))
		}
	}

	c, err := jlaffayeFTP.Dial(addr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("ftp dial %s: %w", addr, err)
	}
	if err := c.Login(opts.User, opts.Pass); err != nil {
		c.Quit()
		return nil, fmt.Errorf("ftp login: %w", err)
	}
	if useTLS && capturedCert != nil && opts.TLSCaptureCallback != nil {
		opts.TLSCaptureCallback(Fingerprint(capturedCert))
	}
	return &ftpConn{c: c, cert: capturedCert}, nil
}

func (f *ftpConn) Upload(remotePath string, body io.Reader, size int64) (int64, string, error) {
	// jlaffaye's STOR streams from the supplied reader. Wrap so we can
	// count bytes (the library returns nil n on success).
	cr := &countingReader{r: body}
	if err := f.c.Stor(remotePath, cr); err != nil {
		// Map the failure to a runner-recognised stage. jlaffaye doesn't
		// distinguish create vs write internally; "write" is the most
		// honest bucket since the connection completed login already.
		return cr.n, "write", fmt.Errorf("stor %s: %w", remotePath, err)
	}
	return cr.n, "", nil
}

func (f *ftpConn) List(remoteDir string) ([]FileInfo, error) {
	entries, err := f.c.List(remoteDir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, FileInfo{
			Name:    e.Name,
			Size:    int64(e.Size),
			Mode:    0, // FTP LIST doesn't reliably surface POSIX mode bits
			ModTime: e.Time,
		})
	}
	return out, nil
}

func (f *ftpConn) Get(remotePath string) (io.ReadCloser, error) {
	r, err := f.c.Retr(remotePath)
	if err != nil {
		return nil, fmt.Errorf("retr %s: %w", remotePath, err)
	}
	return r, nil
}

func (f *ftpConn) Remove(remotePath string) error {
	return f.c.Delete(remotePath)
}

func (f *ftpConn) Stat(remotePath string) (FileInfo, error) {
	// jlaffaye exposes SIZE via FileSize; fall back to a parent List walk
	// for the time/name when only size is available.
	size, err := f.c.FileSize(remotePath)
	if err != nil {
		return FileInfo{}, ErrNoStat
	}
	return FileInfo{Name: remotePath, Size: size}, nil
}

func (f *ftpConn) Close() error {
	// Quit sends the QUIT command and closes the control connection. Errors
	// here are advisory — the test harness sometimes tears down the server
	// before the client can quit.
	return f.c.Quit()
}

// countingReader wraps an io.Reader to tally bytes read. Used by FTP STOR
// because the jlaffaye API doesn't surface byte counts on the success
// path.
type countingReader struct {
	r io.Reader
	n int64
	// mu guards n only; reads on r are sequential by contract (jlaffaye
	// streams synchronously) but lock anyway so a future concurrent caller
	// doesn't trip race detector.
	mu sync.Mutex
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += int64(n)
	c.mu.Unlock()
	return n, err
}

// Drain copies the remote file at remotePath to io.Discard and returns the
// byte count. Used by the download phase to measure pull throughput
// without materialising the file locally. Same shape sftpx.Client.Download
// had pre-v0.13.0 so the runner code stays one-line at the call site.
func Drain(c Conn, remotePath string) (int64, error) {
	rc, err := c.Get(remotePath)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return io.Copy(io.Discard, rc)
}

// Compile-time interface assertions. If sftp.Client gains a new method we
// rely on, the build will tell us at this point rather than at runtime.
var _ Conn = (*sftpConn)(nil)
var _ Conn = (*ftpConn)(nil)

// Ensure pkg/sftp is used (referenced via sftpx.Client.Open which is added
// in this commit). Keeping the import here prevents a broken refactor from
// silently dropping the dependency.
var _ = sftp.ErrSshFxOk
