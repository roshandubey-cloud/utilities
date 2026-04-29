// Package mocksftp is the in-process mock SFTP server. The cmd/mockserver
// binary is a thin flag-parsing wrapper around it; tests import this
// package directly to spin up a server on an ephemeral port without
// shelling out to a subprocess.
//
// Each SSH user gets an isolated virtual home with inbox/, outbox/, and sent/
// folders. After an upload to <user>/inbox/<name> completes and the
// trackid delay elapses:
//   1. The file is renamed in-place to <name>#<trackid>   (loadtest watcher picks this up)
//   2. A copy is placed in the destination user's outbox/ (download-phase target)
//   3. The original is moved to the sender's sent/         (audit trail)
//
// Routing pairs are passed as a map. Users without a pair route to their
// own outbox (self-loop), which is still a valid test.
//
// Uploads discard content but record size. Reads return zero-filled bytes
// of the recorded size so download throughput is measurable.
package mocksftp

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Options configures Start.
type Options struct {
	Addr      string             // "127.0.0.1:0" lets the OS pick a free port
	Delay     time.Duration      // 0 → 2 s default
	Pairs     map[string]string  // upload-user → download-user
	FailUsers map[string]bool    // uploads from these users always fail (test harness)
	Logger    *log.Logger        // nil → log.Default()
}

// Server is a running mock SFTP listener.
type Server struct {
	listener net.Listener
	logger   *log.Logger
	wg       sync.WaitGroup
	stopped  chan struct{}
}

// Addr returns the bound address (resolves the OS-picked port when
// Options.Addr was "host:0").
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Stop closes the listener and waits for all goroutines to exit.
func (s *Server) Stop() error {
	select {
	case <-s.stopped:
		return nil
	default:
		close(s.stopped)
	}
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

// Start launches a new server. The accept loop runs in a goroutine so
// callers can interact immediately. Stop must be called to release the port.
func Start(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.Delay == 0 {
		opts.Delay = 2 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	hostKey, err := generateHostKey()
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if len(pass) == 0 {
				return nil, fmt.Errorf("empty password rejected for %q", c.User())
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(hostKey)

	fs := newMockFS(opts.Delay, opts.Pairs)
	if opts.FailUsers != nil {
		fs.failUsers = opts.FailUsers
	}

	l, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	logger.Printf("mock sftp listening on %s  delay=%s  pairs=%v", l.Addr(), opts.Delay, fs.routes)

	s := &Server{listener: l, logger: logger, stopped: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				select {
				case <-s.stopped:
					return
				default:
				}
				if errors.Is(err, net.ErrClosed) {
					return
				}
				logger.Printf("accept: %v", err)
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				handleConn(conn, cfg, fs, logger)
			}()
		}
	}()
	return s, nil
}

// ParsePairs parses a "up1=dl1,up2=dl2" string into the map Options.Pairs
// expects. Exposed so the cmd/mockserver flag wrapper stays one-liner-thin.
func ParsePairs(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for _, kv := range strings.Split(s, ",") {
		p := strings.SplitN(strings.TrimSpace(kv), "=", 2)
		if len(p) == 2 && p[0] != "" && p[1] != "" {
			out[p[0]] = p[1]
		}
	}
	return out
}

// ParseFailUsers turns a comma-separated list into the set form Options
// wants. Same purpose as ParsePairs — keep the binary's main() trivial.
func ParseFailUsers(s string) map[string]bool {
	out := map[string]bool{}
	if s == "" {
		return out
	}
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out[u] = true
		}
	}
	return out
}

func handleConn(nConn net.Conn, cfg *ssh.ServerConfig, fs *mockFS, logger *log.Logger) {
	defer nConn.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		logger.Printf("handshake: %v", err)
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	user := sconn.User()

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			logger.Printf("accept channel: %v", err)
			continue
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
			}
		}(requests)

		view := &userView{fs: fs, user: user}
		handlers := sftp.Handlers{
			FileGet:  view,
			FilePut:  view,
			FileCmd:  view,
			FileList: view,
		}
		srv := sftp.NewRequestServer(ch, handlers)
		if err := srv.Serve(); err != nil && err != io.EOF {
			logger.Printf("sftp serve: %v", err)
		}
		srv.Close()
	}
}

// -------- filesystem --------
//
// Paths inside the server are keyed as "<user>/<folder>/<basename>" where
// folder ∈ {inbox, outbox, sent}. Any client-relative path is resolved
// against the connected user's namespace via userView.resolve().

type fileState struct {
	size        int64
	completedAt time.Time // zero while upload in progress
	trackID     string    // empty until assigned
}

type mockFS struct {
	mu        sync.Mutex
	files     map[string]*fileState // key = "<user>/<folder>/<basename>"
	delay     time.Duration
	routes    map[string]string // up-user → dl-user (unpaired self-loops)
	failUsers map[string]bool   // test-harness: writes from these users fail
}

func newMockFS(delay time.Duration, routes map[string]string) *mockFS {
	return &mockFS{files: map[string]*fileState{}, delay: delay, routes: routes}
}

// userView is a per-connection handle that scopes all I/O to one SSH user.
type userView struct {
	fs   *mockFS
	user string
}

// resolve maps a client-supplied path like "inbox/foo" or "/inbox/foo" to
// the global key "<user>/inbox/foo". Folder defaults to inbox if the path
// is just a basename (defensive — real clients always include the folder).
func (v *userView) resolve(p string) (folder, name, key string) {
	p = strings.TrimPrefix(path.Clean(p), "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 1 {
		folder, name = "inbox", parts[0]
	} else {
		folder, name = parts[0], parts[1]
	}
	key = v.user + "/" + folder + "/" + name
	return
}

func (v *userView) dirKey(p string) (folder, prefix string) {
	p = strings.TrimPrefix(path.Clean(p), "/")
	if p == "" || p == "." {
		folder = "inbox"
	} else {
		folder = strings.SplitN(p, "/", 2)[0]
	}
	prefix = v.user + "/" + folder + "/"
	return
}

// -------- sftp request handlers --------

func (v *userView) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	_, _, key := v.resolve(r.Filepath)
	v.fs.mu.Lock()
	st := v.fs.files[key]
	v.fs.mu.Unlock()
	if st == nil {
		// Honest "does not exist" so the loadtest reports an error instead of
		// silently completing a 0-byte download. This is what a real SFTP
		// server does when a user reads a path outside their own outbox.
		return nil, os.ErrNotExist
	}
	// Synthesise zero-filled content of recorded size so download speed is measurable.
	return &zeroReader{size: st.size}, nil
}

func (v *userView) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	folder, _, key := v.resolve(r.Filepath)
	if folder != "inbox" {
		return nil, fmt.Errorf("writes only allowed to inbox/, got %s", folder)
	}
	if v.fs.failUsers[v.user] {
		return nil, fmt.Errorf("test harness: rejecting writes for user %q", v.user)
	}
	v.fs.mu.Lock()
	v.fs.files[key] = &fileState{}
	v.fs.mu.Unlock()
	return &writeHandle{fs: v.fs, key: key, user: v.user}, nil
}

func (v *userView) Filecmd(r *sftp.Request) error {
	// Accept mkdir/setstat/rename/remove as no-ops; the virtual namespace is
	// created on demand by uploads and routing.
	return nil
}

func (v *userView) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		folder, prefix := v.dirKey(r.Filepath)
		return v.listFolder(folder, prefix), nil
	case "Stat", "Lstat", "Readlink":
		return listerAt{dirInfo(path.Base(r.Filepath))}, nil
	}
	return listerAt{}, nil
}

func (v *userView) listFolder(folder, prefix string) sftp.ListerAt {
	v.fs.mu.Lock()
	defer v.fs.mu.Unlock()

	// On every list of any user's inbox, promote completed uploads: rename in
	// place (adds #trackid), route a copy to destination user's outbox, and
	// move the sender's record into sent/. This is lazy but bounded — any
	// client poll will trigger it within a poll interval.
	v.fs.promoteInboxesLocked()

	out := make([]os.FileInfo, 0, 32)
	for k, st := range v.fs.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.TrimPrefix(k, prefix)
		if strings.Contains(name, "/") {
			continue // not at this level
		}
		out = append(out, &fileInfo{name: name, size: st.size, mod: st.completedAt})
	}
	_ = folder
	return listerAt(out)
}

// promoteInboxesLocked walks every inbox entry across all users, promoting
// any upload older than delay. Called under fs.mu.
func (fs *mockFS) promoteInboxesLocked() {
	now := time.Now()
	type pending struct {
		oldKey, user, base string
	}
	var ready []pending
	for k, st := range fs.files {
		if st == nil || st.completedAt.IsZero() {
			continue
		}
		parts := strings.SplitN(k, "/", 3)
		if len(parts) != 3 || parts[1] != "inbox" {
			continue
		}
		if strings.Contains(parts[2], "#") {
			continue
		}
		if now.Sub(st.completedAt) < fs.delay {
			continue
		}
		ready = append(ready, pending{oldKey: k, user: parts[0], base: parts[2]})
	}
	for _, p := range ready {
		st := fs.files[p.oldKey]
		tid := randHex(8)
		st.trackID = tid
		trackedName := p.base + "#" + tid

		// 1. rename in place: inbox/<name> → inbox/<name>#<tid>
		newInboxKey := p.user + "/inbox/" + trackedName
		fs.files[newInboxKey] = st
		delete(fs.files, p.oldKey)

		// 2. route a copy to destination user's outbox/
		dst := fs.routes[p.user]
		if dst == "" {
			dst = p.user // self-loop when unpaired
		}
		outboxKey := dst + "/outbox/" + trackedName
		fs.files[outboxKey] = &fileState{
			size:        st.size,
			completedAt: now,
			trackID:     tid,
		}

		// 3. record in sender's sent/ (size only, no content needed)
		sentKey := p.user + "/sent/" + trackedName
		fs.files[sentKey] = &fileState{
			size:        st.size,
			completedAt: now,
			trackID:     tid,
		}
	}
}

// -------- write handle --------

type writeHandle struct {
	fs   *mockFS
	key  string
	user string

	mu   sync.Mutex
	size int64
}

func (w *writeHandle) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	end := off + int64(len(p))
	if end > w.size {
		w.size = end
	}
	w.mu.Unlock()
	return len(p), nil
}

func (w *writeHandle) Close() error {
	w.mu.Lock()
	size := w.size
	w.mu.Unlock()
	w.fs.mu.Lock()
	if st, ok := w.fs.files[w.key]; ok {
		st.size = size
		st.completedAt = time.Now()
	}
	w.fs.mu.Unlock()
	return nil
}

// -------- helpers --------

type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if off+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}

type fileInfo struct {
	name string
	size int64
	mod  time.Time
}

func (f *fileInfo) Name() string       { return f.name }
func (f *fileInfo) Size() int64        { return f.size }
func (f *fileInfo) Mode() os.FileMode  { return 0644 }
func (f *fileInfo) ModTime() time.Time { return f.mod }
func (f *fileInfo) IsDir() bool        { return false }
func (f *fileInfo) Sys() any           { return nil }

func dirInfo(name string) os.FileInfo {
	return &fileInfo{name: name, size: 0, mod: time.Now()}
}

// zeroReader streams `size` zero bytes for downloads. ReaderAt contract.
type zeroReader struct{ size int64 }

func (z *zeroReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= z.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if off+n > z.size {
		n = z.size - off
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	if off+n >= z.size {
		return int(n), io.EOF
	}
	return int(n), nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func generateHostKey() (ssh.Signer, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

