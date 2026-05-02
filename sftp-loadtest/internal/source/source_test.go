package source

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSynthetic_HonoursRequestedSize pins the v0.13.x default behaviour:
// a Synthetic source returns Reader+Size matching the request so the
// runner's Upload(remote, reader, size) stays byte-exact with what
// internal/generator.FastReader produced before v0.14.
func TestSynthetic_HonoursRequestedSize(t *testing.T) {
	res, err := Synthetic{}.Next(Request{SizeBytes: 4096, ContentType: "binary"})
	if err != nil {
		t.Fatalf("Synthetic.Next: %v", err)
	}
	defer res.Close()
	if res.Size != 4096 {
		t.Errorf("Size=%d want 4096", res.Size)
	}
	body, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if int64(len(body)) != res.Size {
		t.Errorf("read %d bytes want %d", len(body), res.Size)
	}
}

// TestLocalFiles_RoundRobin pins per-pool fairness: 5 calls against a
// 3-file pool yields files 0,1,2,0,1 in that order. Sequential mode
// would error on the 4th call instead.
func TestLocalFiles_RoundRobin(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 3)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, "f"+string(rune('A'+i)))
		if err := os.WriteFile(p, []byte("file-"+string(rune('A'+i))), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	src, err := NewLocalFiles(paths, ModeRoundRobin)
	if err != nil {
		t.Fatalf("NewLocalFiles: %v", err)
	}
	want := []string{"file-A", "file-B", "file-C", "file-A", "file-B"}
	for i, w := range want {
		res, err := src.Next(Request{})
		if err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
		body, _ := io.ReadAll(res.Reader)
		res.Close()
		if string(body) != w {
			t.Errorf("call #%d: got %q want %q", i, body, w)
		}
	}
}

// TestLocalFiles_RejectsDirectory pins the constructor's invariant —
// passing a directory path should fail loudly so the operator gets a
// clear error at run-start, not a confusing read failure under load.
func TestLocalFiles_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewLocalFiles([]string{dir}, ModeRoundRobin); err == nil {
		t.Fatal("expected error when passing a directory to NewLocalFiles")
	}
}

// TestLocalDir_SkipsDotfilesAndSubdirs pins the directory walk:
// hidden files (leading dot) and subdirectories are NOT pool members.
// A flat file pool keeps the load runner deterministic.
func TestLocalDir_SkipsDotfilesAndSubdirs(t *testing.T) {
	dir := t.TempDir()
	must := func(p, body string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("real.txt", "real")
	must(".hidden", "skip-hidden")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	must("subdir/nested.txt", "skip-nested")
	src, err := NewLocalDir(dir, ModeRoundRobin)
	if err != nil {
		t.Fatalf("NewLocalDir: %v", err)
	}
	if len(src.files) != 1 {
		t.Fatalf("expected 1 pool file (real.txt), got %d: %v", len(src.files), src.files)
	}
	res, _ := src.Next(Request{})
	body, _ := io.ReadAll(res.Reader)
	res.Close()
	if string(body) != "real" {
		t.Errorf("body=%q want %q", body, "real")
	}
}

// TestResolver_PerUserBeatsPerPatternBeatsDefault pins the override
// hierarchy operators rely on: PerUser > PerPattern > Default. A per-
// user override should fire even when the pattern matches a different
// per-pattern entry.
func TestResolver_PerUserBeatsPerPatternBeatsDefault(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	def := stringSource("default")
	pat := stringSource("per-pattern")
	usr := stringSource("per-user")
	r := &Resolver{
		Default:    def,
		PerPattern: map[string]FileSource{"invoice*": pat},
		PerUser:    map[string]FileSource{"alice": usr},
	}

	// alice + any pattern → per-user wins
	got, _ := r.Resolve(Request{User: "alice", Pattern: "invoice*"}).Next(Request{})
	if read(got) != "per-user" {
		t.Errorf("alice+invoice: want per-user")
	}
	// bob + invoice* → per-pattern wins
	got, _ = r.Resolve(Request{User: "bob", Pattern: "invoice*"}).Next(Request{})
	if read(got) != "per-pattern" {
		t.Errorf("bob+invoice: want per-pattern")
	}
	// bob + claim* → default
	got, _ = r.Resolve(Request{User: "bob", Pattern: "claim*"}).Next(Request{})
	if read(got) != "default" {
		t.Errorf("bob+claim: want default")
	}
	// nil resolver → Synthetic
	var nilR *Resolver
	got, _ = nilR.Resolve(Request{User: "bob", Pattern: "x", SizeBytes: 16}).Next(Request{SizeBytes: 16})
	if got.Size != 16 {
		t.Errorf("nil resolver should fall through to Synthetic, got Size=%d", got.Size)
	}
	_ = mk
}

// stringSource is a tiny test FileSource that always returns the same
// string body. Avoids the disk-IO dance for tests that only care about
// the resolver's routing logic.
type stringSource string

func (s stringSource) Next(_ Request) (Result, error) {
	return Result{
		Reader: strings.NewReader(string(s)),
		Size:   int64(len(s)),
		Close:  func() {},
	}, nil
}

func read(r Result) string {
	defer r.Close()
	body, _ := io.ReadAll(r.Reader)
	return string(body)
}
