package mockftp

import (
	"bytes"
	"strings"
	"testing"
	"time"

	jftp "github.com/jlaffaye/ftp"
)

// Pin: LIST outbox MUST list the dst user's outbox after promotion. Until
// v0.19.11 splitPath defaulted "outbox" → folder=inbox/name=outbox, so
// listForUser silently listed inbox and the FTPS cluster downloader saw
// 0 files. The pin uploads to normal1, lets promotion fire, then asserts
// dl1's outbox carries the file via the public LIST command — failing
// pre-fix.
func TestListOutboxAfterPromotion(t *testing.T) {
	srv, err := Start(Options{
		Addr:           "127.0.0.1:0",
		Delay:          50 * time.Millisecond,
		Pairs:          map[string]string{"normal1": "dl1"},
		PersistContent: true,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr().String()

	upload, err := jftp.Dial(addr, jftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("dial upload: %v", err)
	}
	if err := upload.Login("normal1", "x"); err != nil {
		t.Fatalf("login normal1: %v", err)
	}
	body := bytes.Repeat([]byte{0xAB}, 1024)
	if err := upload.Stor("inbox/payload.bin", bytes.NewReader(body)); err != nil {
		t.Fatalf("stor: %v", err)
	}
	_ = upload.Quit()

	// Wait past Delay so promoteAll routes the file.
	time.Sleep(200 * time.Millisecond)

	dl, err := jftp.Dial(addr, jftp.DialWithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("dial dl: %v", err)
	}
	if err := dl.Login("dl1", "x"); err != nil {
		t.Fatalf("login dl1: %v", err)
	}
	defer dl.Quit()

	entries, err := dl.List("outbox")
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected dl1/outbox to contain the routed file, got 0 entries")
	}
	var name string
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "payload.bin") {
			name = e.Name
			break
		}
	}
	if name == "" {
		t.Fatalf("expected payload.bin#<tid> in outbox, got %+v", entries)
	}

	r, err := dl.Retr("outbox/" + name)
	if err != nil {
		t.Fatalf("retr: %v", err)
	}
	defer r.Close()
	got := make([]byte, 0, len(body))
	buf := make([]byte, 4096)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("byte mismatch: got %d bytes, want %d", len(got), len(body))
	}
}
