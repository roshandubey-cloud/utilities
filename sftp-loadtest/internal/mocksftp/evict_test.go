package mocksftp

// Pin the v0.19.8 evict-after-read contract: when EvictAfterRead is on
// and a download user opens an outbox file for read, the outbox entry
// + the source-side inbox + sent entries are removed from the in-memory
// files map. The reader returned to the caller still holds a reference
// to the byte slice for the duration of the read; the entries are gone
// from the index so a re-list returns the file as missing — that's the
// "memory bounded by in-flight files only" property the operator gets.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TestEvictAfterRead_DropsAllThreeCopies pins the eviction semantics:
// an outbox read drops 3 in-memory entries (outbox, source's inbox,
// source's sent) but the reader still delivers the bytes verbatim.
func TestEvictAfterRead_DropsAllThreeCopies(t *testing.T) {
	srv, err := Start(Options{
		Addr:           "127.0.0.1:0",
		Delay:          1 * time.Millisecond,
		Pairs:          map[string]string{"u1": "dl1"},
		PersistContent: true,
		EvictAfterRead: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)

	// Upload 1 KB as u1 → routed to dl1's outbox.
	payload := make([]byte, 1024)
	_, _ = rand.Read(payload)
	uploadName := "datafile.bin"
	if err := dial(host, port, "u1", "p", func(c *sftp.Client) error {
		f, err := c.Create("inbox/" + uploadName)
		if err != nil {
			return err
		}
		if _, err := f.Write(payload); err != nil {
			return err
		}
		return f.Close()
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Trigger promote by listing dl1's outbox once.
	var outboxName string
	if err := dial(host, port, "dl1", "p", func(c *sftp.Client) error {
		// Wait briefly for promote (delay=0 → immediate).
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			entries, err := c.ReadDir("outbox")
			if err == nil && len(entries) > 0 {
				outboxName = entries[0].Name()
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("promote did not happen within 2s")
	}); err != nil {
		t.Fatalf("wait for promote: %v", err)
	}

	// Sanity: source-side inbox + sent entries exist before read.
	wantPresent := []string{
		"u1/inbox/" + outboxName,
		"u1/sent/" + outboxName,
		"dl1/outbox/" + outboxName,
	}
	for _, k := range wantPresent {
		if !srv.fileExists(k) {
			t.Errorf("setup: expected %q to exist before read", k)
		}
	}

	// Read the outbox file as dl1.
	var got bytes.Buffer
	if err := dial(host, port, "dl1", "p", func(c *sftp.Client) error {
		f, err := c.Open("outbox/" + outboxName)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(&got, f)
		return err
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Len() != len(payload) {
		t.Errorf("download size mismatch: got %d want %d", got.Len(), len(payload))
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Error("download bytes diverged from upload — eviction broke byte fidelity")
	}

	// All three entries must be gone from the in-memory index.
	for _, k := range wantPresent {
		if srv.fileExists(k) {
			t.Errorf("post-read: %q still in files map (eviction did not fire)", k)
		}
	}
}

// TestEvictAfterRead_OffByDefault confirms the flag actually gates the
// behaviour — by default an outbox read does NOT evict.
func TestEvictAfterRead_OffByDefault(t *testing.T) {
	srv, err := Start(Options{
		Addr:           "127.0.0.1:0",
		Delay:          1 * time.Millisecond,
		Pairs:          map[string]string{"u1": "dl1"},
		PersistContent: true,
		// EvictAfterRead intentionally omitted (default false)
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)

	if err := dial(host, port, "u1", "p", func(c *sftp.Client) error {
		f, err := c.Create("inbox/keep.bin")
		if err != nil {
			return err
		}
		if _, err := f.Write([]byte("xxxx")); err != nil {
			return err
		}
		return f.Close()
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}

	var outboxName string
	_ = dial(host, port, "dl1", "p", func(c *sftp.Client) error {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := c.ReadDir("outbox")
			if len(entries) > 0 {
				outboxName = entries[0].Name()
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("promote did not happen")
	})

	_ = dial(host, port, "dl1", "p", func(c *sftp.Client) error {
		f, err := c.Open("outbox/" + outboxName)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(io.Discard, f)
		return err
	})

	// File should STILL be in the index (default behaviour: keep).
	if !srv.fileExists("dl1/outbox/" + outboxName) {
		t.Error("default behaviour broke: outbox entry vanished without EvictAfterRead")
	}
}

// dial is a tiny SFTP-client helper for the tests above.
func dial(host string, port int, user, pass string, fn func(*sftp.Client) error) error {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	addr := host + ":" + strconv.Itoa(port)
	sshc, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer sshc.Close()
	c, err := sftp.NewClient(sshc)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

// fileExists is a test-only accessor that reaches into the in-memory
// map under the same mutex the server uses, so a test can assert
// before/after eviction without exposing internals to public callers.
func (s *Server) fileExists(key string) bool {
	if s == nil || s.fs == nil {
		return false
	}
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	_, ok := s.fs.files[key]
	return ok
}

// Ensure os.* compiles on darwin/linux/windows (the test stack imports
// these transitively through sftp).
var _ = os.ErrNotExist
