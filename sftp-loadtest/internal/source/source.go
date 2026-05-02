// Package source provides pluggable file sources for the upload phase.
//
// Until v0.14.0 every upload was synthetic — the runner generated random
// bytes via internal/generator.FastReader. That's the right default for
// pure-throughput tests, but real-world QA scenarios need to push REAL
// payloads (a fixed PDF, a known EDI batch, a regression-probe tarball)
// so server-side validators behave like they do in production. This
// package adds that flexibility without breaking the synthetic default.
//
// Three implementations:
//
//   * Synthetic    — random bytes of caller-requested size. Drop-in
//                    replacement for the pre-v0.14 generator path.
//   * LocalFiles   — pool of explicit file paths. Picks one per Next()
//                    call by mode: round-robin / random / sequential.
//   * LocalDir     — walks a directory at construction time and treats
//                    every regular file as a pool member. Same picker
//                    modes as LocalFiles.
//
// Resolution hierarchy (operator-controlled at config time):
//
//   1. PerUser[username]      — most specific; wins outright.
//   2. PerPattern[pattern]    — second-most specific.
//   3. Top-level config       — fallback default for the kind (normal / large).
//   4. Synthetic              — final fallback when nothing else is configured.
//
// All sources are concurrency-safe: the runner fires uploads from many
// goroutines in parallel.

package source

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/generator"
)

// Request is what the runner asks for at each upload tick.
type Request struct {
	User      string // upload user (CSV row 1)
	Pattern   string // selected pattern for this upload (e.g. "invoice*")
	Kind      string // "normal" | "large" — for routing per-kind sources
	SizeBytes int64  // requested size; honoured by Synthetic, ignored by file-backed sources
	// ContentType only matters for the synthetic source ("binary" or "ascii").
	// File-backed sources copy bytes verbatim from the file.
	ContentType string
}

// Result is returned to the runner. Reader is consumed once and Close
// is called when the upload finishes (success or failure).
type Result struct {
	Reader io.Reader
	Size   int64
	Close  func() // file handle close, etc. Always non-nil.
}

// FileSource is the contract every upload source implements.
type FileSource interface {
	// Next returns one Result tailored to the request. Implementations
	// must be safe for concurrent calls.
	Next(req Request) (Result, error)
}

// Synthetic is the default, drop-in replacement for the pre-v0.14
// inline generator path. Random bytes of the caller-requested size.
type Synthetic struct{}

func (Synthetic) Next(req Request) (Result, error) {
	content := req.ContentType
	if content == "" {
		content = generator.ContentBinary
	}
	return Result{
		Reader: generator.FastReader(req.SizeBytes, content),
		Size:   req.SizeBytes,
		Close:  func() {},
	}, nil
}

// PickMode controls how a file-pool source selects its next file.
type PickMode string

const (
	ModeRoundRobin PickMode = "round-robin"
	ModeRandom     PickMode = "random"
	ModeSequential PickMode = "sequential" // exhausts the pool then errors
)

// LocalFiles uses an explicit list of file paths as the pool. Each
// Next() call opens a fresh os.File so concurrent uploads don't fight
// over the same fd / read offset.
type LocalFiles struct {
	files []string // resolved absolute paths, validated at construction
	mode  PickMode
	idx   atomic.Int64 // round-robin / sequential cursor
	rnd   *rand.Rand
	mu    sync.Mutex // guards rnd which isn't goroutine-safe
}

// NewLocalFiles returns a LocalFiles source. Paths are validated at
// construction (must exist, must be readable). An empty list is an
// error — Synthetic should be used instead in that case.
func NewLocalFiles(paths []string, mode PickMode) (*LocalFiles, error) {
	if len(paths) == 0 {
		return nil, errors.New("local-files source: at least one file is required")
	}
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		ap, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("local-files: resolve %q: %w", p, err)
		}
		fi, err := os.Stat(ap)
		if err != nil {
			return nil, fmt.Errorf("local-files: stat %q: %w", ap, err)
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("local-files: %q is a directory — use kind=local-dir for that", ap)
		}
		abs = append(abs, ap)
	}
	if mode == "" {
		mode = ModeRoundRobin
	}
	return &LocalFiles{
		files: abs,
		mode:  mode,
		rnd:   rand.New(rand.NewSource(int64(len(abs)))),
	}, nil
}

func (l *LocalFiles) Next(req Request) (Result, error) {
	pick, err := l.pickIndex()
	if err != nil {
		return Result{}, err
	}
	return openFile(l.files[pick])
}

func (l *LocalFiles) pickIndex() (int, error) {
	switch l.mode {
	case ModeRandom:
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.rnd.Intn(len(l.files)), nil
	case ModeSequential:
		i := l.idx.Add(1) - 1
		if i >= int64(len(l.files)) {
			return 0, fmt.Errorf("local-files sequential: pool exhausted after %d files", len(l.files))
		}
		return int(i), nil
	default: // round-robin
		i := l.idx.Add(1) - 1
		return int(i % int64(len(l.files))), nil
	}
}

// LocalDir walks a directory at construction time and uses every
// regular file as a pool member. Reads the directory ONCE — files
// added or removed at runtime are not picked up. That's deliberate:
// a load test should run against a stable input set so results are
// reproducible.
type LocalDir struct {
	*LocalFiles
}

// NewLocalDir scans dir for regular files and returns a LocalDir.
// Hidden files (leading dot) are skipped. Subdirectories are NOT
// recursed — a flat file pool is what the load runner expects.
func NewLocalDir(dir string, mode PickMode) (*LocalDir, error) {
	if dir == "" {
		return nil, errors.New("local-dir: dir is required")
	}
	ap, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("local-dir: resolve %q: %w", dir, err)
	}
	fi, err := os.Stat(ap)
	if err != nil {
		return nil, fmt.Errorf("local-dir: stat %q: %w", ap, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("local-dir: %q is not a directory", ap)
	}
	entries, err := os.ReadDir(ap)
	if err != nil {
		return nil, fmt.Errorf("local-dir: read %q: %w", ap, err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files = append(files, filepath.Join(ap, e.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("local-dir: %q contains no regular files", ap)
	}
	sort.Strings(files) // deterministic order for round-robin / sequential
	lf, err := NewLocalFiles(files, mode)
	if err != nil {
		return nil, err
	}
	return &LocalDir{LocalFiles: lf}, nil
}

// Layout names the strategies LocalTree understands. Documented in the
// SourceConfig comment in internal/config/config.go.
type Layout string

const (
	LayoutFlat            Layout = "flat"
	LayoutByUser          Layout = "by-user"
	LayoutByPattern       Layout = "by-pattern"
	LayoutByUserAndPattern Layout = "by-user-pattern"
)

// LocalTree resolves a per-user / per-pattern file pool against a single
// root directory. Lazy: the file list for (user, pattern) is built on
// first use and cached for the rest of the run, so an N-account test
// against `<root>/<account>/*` is cheap even when N is large.
//
// Layouts:
//   - flat               — <root>/* (one shared pool; identical to LocalDir)
//   - by-user            — <root>/<username>/*
//   - by-pattern         — <root>/* filtered by filepath.Match(pattern, basename)
//   - by-user-pattern    — <root>/<username>/* filtered by filepath.Match
//
// Hidden files (leading dot) and subdirectories are skipped at every
// layer. An empty resolved pool fails the upload — operators see the
// pattern mismatch immediately instead of falling back to synthetic.
type LocalTree struct {
	root   string
	layout Layout
	mode   PickMode
	mu     sync.Mutex
	pools  map[string]*LocalFiles
}

// NewLocalTree validates the root directory exists and returns a
// LocalTree. Per-(user, pattern) resolution is deferred to Next().
func NewLocalTree(root string, layout Layout, mode PickMode) (*LocalTree, error) {
	if root == "" {
		return nil, errors.New("local-tree: root is required")
	}
	ap, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("local-tree: resolve %q: %w", root, err)
	}
	fi, err := os.Stat(ap)
	if err != nil {
		return nil, fmt.Errorf("local-tree: stat %q: %w", ap, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("local-tree: %q is not a directory", ap)
	}
	if layout == "" {
		layout = LayoutFlat
	}
	switch layout {
	case LayoutFlat, LayoutByUser, LayoutByPattern, LayoutByUserAndPattern:
		// ok
	default:
		return nil, fmt.Errorf("local-tree: unknown layout %q", layout)
	}
	if mode == "" {
		mode = ModeRoundRobin
	}
	return &LocalTree{
		root:   ap,
		layout: layout,
		mode:   mode,
		pools:  map[string]*LocalFiles{},
	}, nil
}

func (l *LocalTree) Next(req Request) (Result, error) {
	pool, err := l.poolFor(req)
	if err != nil {
		return Result{}, err
	}
	return pool.Next(req)
}

func (l *LocalTree) poolFor(req Request) (*LocalFiles, error) {
	key := string(l.layout) + "|" + req.User + "|" + req.Pattern
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.pools[key]; ok {
		return p, nil
	}
	files, err := l.resolveFor(req)
	if err != nil {
		return nil, err
	}
	p, err := NewLocalFiles(files, l.mode)
	if err != nil {
		return nil, err
	}
	l.pools[key] = p
	return p, nil
}

// resolveFor expands the layout to a concrete (dir, pattern) pair and
// returns the matching files. Errors here are friendly enough to land
// in /api/probe-source output verbatim.
func (l *LocalTree) resolveFor(req Request) ([]string, error) {
	dir := l.root
	var glob string
	switch l.layout {
	case LayoutByUser:
		if req.User == "" {
			return nil, errors.New("local-tree by-user: empty username (CSV row missing username column?)")
		}
		dir = filepath.Join(l.root, req.User)
	case LayoutByPattern:
		glob = req.Pattern
	case LayoutByUserAndPattern:
		if req.User == "" {
			return nil, errors.New("local-tree by-user-pattern: empty username")
		}
		dir = filepath.Join(l.root, req.User)
		glob = req.Pattern
	}
	return readDirGlob(dir, glob)
}

// readDirGlob lists dir's top-level regular files (skipping dotfiles
// and subdirs) and optionally filters by a filepath.Match glob against
// the basename. An empty match is an error so an operator who typoed a
// pattern doesn't silently get an empty pool.
func readDirGlob(dir, glob string) ([]string, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("local-tree: stat %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("local-tree: %q is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("local-tree: read %q: %w", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if glob != "" {
			ok, err := filepath.Match(glob, e.Name())
			if err != nil {
				return nil, fmt.Errorf("local-tree: bad pattern %q: %w", glob, err)
			}
			if !ok {
				continue
			}
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	if len(out) == 0 {
		if glob != "" {
			return nil, fmt.Errorf("local-tree: no files in %q match pattern %q", dir, glob)
		}
		return nil, fmt.Errorf("local-tree: %q contains no regular files", dir)
	}
	sort.Strings(out)
	return out, nil
}

// FilesFor exposes resolveFor to /api/probe-source so the UI can
// preview the per-user file list before the operator commits to a run.
// Returns nil + the friendly error verbatim on failure.
func (l *LocalTree) FilesFor(user, pattern string) ([]string, error) {
	return l.resolveFor(Request{User: user, Pattern: pattern})
}

// Layout returns the configured layout — useful for the probe UI to
// decide whether to render a per-user matrix or a single pool view.
func (l *LocalTree) LayoutName() Layout { return l.layout }

// Root returns the configured root directory.
func (l *LocalTree) Root() string { return l.root }

// openFile wraps an os.File so the runner can stream upload bytes
// directly from disk without buffering the whole file in memory.
// Close is wired through Result so the runner releases the fd as
// soon as the upload finishes.
func openFile(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open source file %q: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return Result{}, fmt.Errorf("stat source file %q: %w", path, err)
	}
	return Result{
		Reader: f,
		Size:   fi.Size(),
		Close:  func() { f.Close() },
	}, nil
}

// Resolver chains per-user, per-pattern, and default sources so the
// runner can call one method per upload regardless of how the operator
// configured things. PerUser wins over PerPattern, which wins over
// Default. A nil Resolver returns Synthetic{} unconditionally.
type Resolver struct {
	Default    FileSource
	PerUser    map[string]FileSource
	PerPattern map[string]FileSource
}

// Files returns the resolved absolute paths in the LocalFiles pool —
// exposed so the /api/probe-source UI can preview what the runner
// would actually upload before the operator commits to a real run.
func (l *LocalFiles) Files() []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l.files))
	copy(out, l.files)
	return out
}

// Resolve returns the source the runner should call Next() on for this
// upload. Falls through user → pattern → default → Synthetic.
func (r *Resolver) Resolve(req Request) FileSource {
	if r == nil {
		return Synthetic{}
	}
	if r.PerUser != nil {
		if s, ok := r.PerUser[req.User]; ok && s != nil {
			return s
		}
	}
	if r.PerPattern != nil {
		if s, ok := r.PerPattern[req.Pattern]; ok && s != nil {
			return s
		}
	}
	if r.Default != nil {
		return r.Default
	}
	return Synthetic{}
}
