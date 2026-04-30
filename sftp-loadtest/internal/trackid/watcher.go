package trackid

import (
	"context"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/protocol"
)

// Watcher polls one remote folder per user and matches uploaded basenames
// to their server-renamed "<basename>#<trackid>" form. Protocol-agnostic
// since v0.13.0 — the opener returns a protocol.Conn so SFTP, FTP and
// FTPS routes all share this code.
type Watcher struct {
	poll    time.Duration
	timeout time.Duration
	folder  string

	mu       sync.Mutex
	pending  map[string]*entry // key = user|basename
	results  chan Result

	clientsMu sync.Mutex
	clients   map[string]protocol.Conn // key = user
	opener    func(user string) (protocol.Conn, error)
}

type entry struct {
	user      string
	basename  string
	uploaded  time.Time
	deadline  time.Time
}

type Result struct {
	User        string
	Basename    string
	TrackID     string
	DetectedAt  time.Time
	TimedOut    bool
}

func New(folder string, poll, timeout time.Duration, opener func(user string) (protocol.Conn, error)) *Watcher {
	return &Watcher{
		poll:    poll,
		timeout: timeout,
		folder:  folder,
		pending: map[string]*entry{},
		results: make(chan Result, 512),
		clients: map[string]protocol.Conn{},
		opener:  opener,
	}
}

func (w *Watcher) Results() <-chan Result { return w.results }

func (w *Watcher) Register(user, basename string) {
	key := user + "|" + basename
	now := time.Now()
	w.mu.Lock()
	w.pending[key] = &entry{
		user:     user,
		basename: basename,
		uploaded: now,
		deadline: now.Add(w.timeout),
	}
	w.mu.Unlock()
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.closeAllClients()
			return
		case <-t.C:
			w.tick()
		}
	}
}

func (w *Watcher) tick() {
	// Snapshot pending grouped by user.
	w.mu.Lock()
	byUser := map[string][]*entry{}
	now := time.Now()
	for k, e := range w.pending {
		if now.After(e.deadline) {
			delete(w.pending, k)
			select {
			case w.results <- Result{User: e.user, Basename: e.basename, TimedOut: true, DetectedAt: now}:
			default:
			}
			continue
		}
		byUser[e.user] = append(byUser[e.user], e)
	}
	w.mu.Unlock()

	for user, entries := range byUser {
		c, err := w.getClient(user)
		if err != nil {
			continue
		}
		infos, err := c.List(w.folder)
		if err != nil {
			w.closeClient(user)
			continue
		}
		// Build a prefix map once: basename → trackid for every "<basename>#<id>"
		// entry in the directory. This makes each pending lookup O(1) instead of
		// scanning the whole listing per pending entry. Critical at 100K+ fpm.
		prefixMap := make(map[string]string, len(infos))
		for _, fi := range infos {
			name := path.Base(fi.Name)
			if idx := strings.IndexByte(name, '#'); idx > 0 {
				prefixMap[name[:idx]] = name[idx+1:]
			}
		}
		detectedAt := time.Now()
		for _, e := range entries {
			tid, ok := prefixMap[e.basename]
			if !ok {
				continue
			}
			w.mu.Lock()
			delete(w.pending, e.user+"|"+e.basename)
			w.mu.Unlock()
			select {
			case w.results <- Result{User: e.user, Basename: e.basename, TrackID: tid, DetectedAt: detectedAt}:
			default:
			}
		}
	}
}

func (w *Watcher) getClient(user string) (protocol.Conn, error) {
	w.clientsMu.Lock()
	c, ok := w.clients[user]
	w.clientsMu.Unlock()
	if ok {
		return c, nil
	}
	c, err := w.opener(user)
	if err != nil {
		return nil, err
	}
	w.clientsMu.Lock()
	w.clients[user] = c
	w.clientsMu.Unlock()
	return c, nil
}

func (w *Watcher) closeClient(user string) {
	w.clientsMu.Lock()
	if c, ok := w.clients[user]; ok {
		c.Close()
		delete(w.clients, user)
	}
	w.clientsMu.Unlock()
}

func (w *Watcher) closeAllClients() {
	w.clientsMu.Lock()
	for u, c := range w.clients {
		c.Close()
		delete(w.clients, u)
	}
	w.clientsMu.Unlock()
}

// PendingCount returns how many track-ids are still being awaited.
func (w *Watcher) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

