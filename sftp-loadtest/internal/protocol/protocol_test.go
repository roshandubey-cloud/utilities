package protocol_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mockftp"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mocksftp"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/protocol"

	"golang.org/x/crypto/ssh"
)

// TestSFTP_RoundTrip exercises the SFTP wrapper end to end against the
// existing mocksftp server: Dial, Upload, List, Stat, Get, Drain, Close.
// Asserts that every Conn method maps onto the right pkg/sftp call.
func TestSFTP_RoundTrip(t *testing.T) {
	srv, err := mocksftp.Start(mocksftp.Options{Addr: "127.0.0.1:0", Delay: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("mocksftp start: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr().String()
	host, port := splitHostPort(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, protocol.SFTP, protocol.DialOpts{
		Host:               host,
		Port:               port,
		User:               "u1",
		Pass:               "p",
		SSHHostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	body := []byte("hello-from-protocol-pkg")
	n, stage, uerr := c.Upload("inbox/x.txt", bytes.NewReader(body), int64(len(body)))
	if uerr != nil {
		t.Fatalf("upload: stage=%s err=%v", stage, uerr)
	}
	if n != int64(len(body)) {
		t.Fatalf("upload n: got %d want %d", n, len(body))
	}

	// Wait for promotion (#trackid suffix) to land.
	deadline := time.Now().Add(5 * time.Second)
	var entries []protocol.FileInfo
	for time.Now().Before(deadline) {
		entries, err = c.List("inbox")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		hasHash := false
		for _, e := range entries {
			if strings.Contains(e.Name, "#") {
				hasHash = true
				break
			}
		}
		if hasHash {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one entry")
	}
	var trackedName string
	for _, e := range entries {
		if strings.Contains(e.Name, "#") {
			trackedName = e.Name
			break
		}
	}
	if trackedName == "" {
		t.Fatalf("no #trackid file found, entries=%v", entries)
	}

	// Stat should work for SFTP.
	if _, err := c.Stat("inbox/" + trackedName); err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Get + drain — should return the recorded size of zero bytes
	// (mocksftp synthesises zero-filled bytes).
	rc, err := c.Get("inbox/" + trackedName)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if int64(len(got)) != n {
		t.Fatalf("got %d bytes back; want %d", len(got), n)
	}

	// Trackid suffix proves the runner's downstream pipeline can spot the
	// upload regardless of which protocol was used.
	if !strings.Contains(trackedName, "#") {
		t.Fatalf("tracked name missing # suffix: %q", trackedName)
	}
}

// TestFTP_RoundTrip exercises the plain-FTP wrapper against mockftp.
func TestFTP_RoundTrip(t *testing.T) {
	srv, err := mockftp.Start(mockftp.Options{Addr: "127.0.0.1:0", Delay: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("mockftp start: %v", err)
	}
	defer srv.Stop()

	host, port := splitHostPort(t, srv.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, protocol.FTP, protocol.DialOpts{
		Host: host, Port: port, User: "u1", Pass: "p",
	})
	if err != nil {
		t.Fatalf("dial ftp: %v", err)
	}
	defer c.Close()

	body := []byte("ftp-round-trip-hello")
	if _, _, err := c.Upload("inbox/y.txt", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Wait for promotion to "y.txt#<id>".
	tracked, err := waitForTrackID(c, "inbox", "y.txt")
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(tracked, "#") {
		t.Fatalf("tracked %q missing #", tracked)
	}
}

// TestFTPS_Implicit_RoundTrip — implicit-mode TLS from byte 0.
func TestFTPS_Implicit_RoundTrip(t *testing.T) {
	srv, err := mockftp.Start(mockftp.Options{
		Addr:  "127.0.0.1:0",
		Delay: 50 * time.Millisecond,
		TLS:   &mockftp.TLSOptions{EnableImplicit: true, ImplicitAddr: "127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("mockftp implicit start: %v", err)
	}
	defer srv.Stop()

	host, port := splitHostPort(t, srv.ImplicitAddr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var capturedFP string
	c, err := protocol.Dial(ctx, protocol.FTPS, protocol.DialOpts{
		Host: host, Port: port, User: "u1", Pass: "p",
		TLSMode:            protocol.TLSImplicit,
		InsecureSkipVerify: true,
		TLSCaptureCallback: func(fp string) { capturedFP = fp },
	})
	if err != nil {
		t.Fatalf("dial ftps implicit: %v", err)
	}
	defer c.Close()
	if capturedFP == "" {
		t.Fatalf("expected TLS fingerprint capture, got empty string")
	}
	if cert := protocol.TLSPeerCertificate(c); cert == nil {
		t.Fatalf("expected non-nil TLS peer cert")
	}

	body := []byte("ftps-implicit-hello")
	if _, _, err := c.Upload("inbox/z.txt", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := waitForTrackID(c, "inbox", "z.txt"); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestFTPS_Explicit_RoundTrip — AUTH TLS upgrade against the same listener.
func TestFTPS_Explicit_RoundTrip(t *testing.T) {
	srv, err := mockftp.Start(mockftp.Options{
		Addr:  "127.0.0.1:0",
		Delay: 50 * time.Millisecond,
		TLS:   &mockftp.TLSOptions{EnableExplicit: true},
	})
	if err != nil {
		t.Fatalf("mockftp explicit start: %v", err)
	}
	defer srv.Stop()

	host, port := splitHostPort(t, srv.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, protocol.FTPS, protocol.DialOpts{
		Host: host, Port: port, User: "u1", Pass: "p",
		TLSMode:            protocol.TLSExplicit,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("dial ftps explicit: %v", err)
	}
	defer c.Close()

	body := []byte("ftps-explicit-hello")
	if _, _, err := c.Upload("inbox/q.txt", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := waitForTrackID(c, "inbox", "q.txt"); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestFTPS_FingerprintCapture asserts the TLS callback (and TLSPeerCertificate)
// produce the same SHA-256 the mock advertises.
func TestFTPS_FingerprintCapture(t *testing.T) {
	srv, err := mockftp.Start(mockftp.Options{
		Addr:  "127.0.0.1:0",
		TLS:   &mockftp.TLSOptions{EnableImplicit: true, ImplicitAddr: "127.0.0.1:0"},
		Delay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("mockftp start: %v", err)
	}
	defer srv.Stop()
	want := srv.Fingerprint()
	if want == "" {
		t.Fatalf("expected non-empty mock fingerprint")
	}

	host, port := splitHostPort(t, srv.ImplicitAddr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got string
	c, err := protocol.Dial(ctx, protocol.FTPS, protocol.DialOpts{
		Host: host, Port: port, User: "u1", Pass: "p",
		TLSMode:            protocol.TLSImplicit,
		InsecureSkipVerify: true,
		TLSCaptureCallback: func(fp string) { got = fp },
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if got != want {
		t.Fatalf("captured fingerprint mismatch:\n got=%q\nwant=%q", got, want)
	}
	if got2 := protocol.Fingerprint(protocol.TLSPeerCertificate(c)); got2 != want {
		t.Fatalf("fingerprint via TLSPeerCertificate mismatch: got=%q want=%q", got2, want)
	}
}

// TestEndsWith_TrackID is the protocol-agnostic suffix check the runner's
// trackid watcher relies on. Each protocol's mock should rename inbox files
// to "<n>#<id>" so the runner's match-by-basename code works unchanged.
func TestEndsWith_TrackID(t *testing.T) {
	cases := []struct {
		name     string
		dialFn   func(t *testing.T) protocol.Conn
		closeFn  func()
		filename string
	}{
		{"sftp", makeSFTPDial(t), nil, "f.sftp"},
		{"ftp", makeFTPDial(t, false), nil, "f.ftp"},
		{"ftps", makeFTPDial(t, true), nil, "f.ftps"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := tc.dialFn(t)
			defer c.Close()
			body := []byte("trackid-shape")
			if _, _, err := c.Upload("inbox/"+tc.filename, bytes.NewReader(body), int64(len(body))); err != nil {
				t.Fatalf("upload: %v", err)
			}
			tracked, err := waitForTrackID(c, "inbox", tc.filename)
			if err != nil {
				t.Fatalf("wait: %v", err)
			}
			parts := strings.SplitN(tracked, "#", 2)
			if len(parts) != 2 || parts[0] != tc.filename || parts[1] == "" {
				t.Fatalf("expected %q#<id>, got %q", tc.filename, tracked)
			}
		})
	}
}

// ----- helpers -----

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := splitHP(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func splitHP(addr string) (string, string, error) {
	idx := strings.LastIndexByte(addr, ':')
	if idx < 0 {
		return "", "", io.ErrUnexpectedEOF
	}
	return addr[:idx], addr[idx+1:], nil
}

func atoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, io.ErrUnexpectedEOF
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// waitForTrackID polls List until a file named "<base>#<id>" appears.
func waitForTrackID(c protocol.Conn, folder, base string) (string, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := c.List(folder)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name, base+"#") {
				return e.Name, nil
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	return "", io.ErrUnexpectedEOF
}

func makeSFTPDial(t *testing.T) func(*testing.T) protocol.Conn {
	t.Helper()
	srv, err := mocksftp.Start(mocksftp.Options{Addr: "127.0.0.1:0", Delay: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("mocksftp start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	host, port := splitHostPort(t, srv.Addr().String())
	return func(t *testing.T) protocol.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		c, err := protocol.Dial(ctx, protocol.SFTP, protocol.DialOpts{
			Host: host, Port: port, User: "u1", Pass: "p",
			SSHHostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			t.Fatalf("dial sftp: %v", err)
		}
		return c
	}
}

func makeFTPDial(t *testing.T, ftps bool) func(*testing.T) protocol.Conn {
	t.Helper()
	var srv *mockftp.Server
	var err error
	if ftps {
		srv, err = mockftp.Start(mockftp.Options{Addr: "127.0.0.1:0", Delay: 50 * time.Millisecond,
			TLS: &mockftp.TLSOptions{EnableImplicit: true, ImplicitAddr: "127.0.0.1:0"}})
	} else {
		srv, err = mockftp.Start(mockftp.Options{Addr: "127.0.0.1:0", Delay: 50 * time.Millisecond})
	}
	if err != nil {
		t.Fatalf("mockftp start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	addr := srv.Addr().String()
	if ftps {
		addr = srv.ImplicitAddr().String()
	}
	host, port := splitHostPort(t, addr)
	return func(t *testing.T) protocol.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		opts := protocol.DialOpts{Host: host, Port: port, User: "u1", Pass: "p"}
		proto := protocol.FTP
		if ftps {
			proto = protocol.FTPS
			opts.TLSMode = protocol.TLSImplicit
			opts.InsecureSkipVerify = true
		}
		c, err := protocol.Dial(ctx, proto, opts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return c
	}
}
