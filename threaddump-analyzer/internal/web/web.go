// Package web glues the parser/analyzer/session/findings packages into a
// single HTTP service. Same security middleware envelope as sftp-loadtest:
// security headers, CSRF X-Requested-With check, per-IP rate limit, body-
// size cap. The whole thing fits on one page of routes.
package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/analyzer"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/findings"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/session"
)

//go:embed static/*
var staticFS embed.FS

// Server owns the in-memory session registry. Sessions are not persisted to
// disk in v0.1 — uploads live for the process lifetime only. That's a
// deliberate v0.1 choice; persistence + sharing is on the v0.2 roadmap.
type Server struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
	maxBody  int64
}

func NewServer() *Server {
	return &Server{
		sessions: map[string]*session.Session{},
		maxBody:  128 << 20, // 128 MiB — accommodates jumbo enterprise dumps
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/session", s.handleNewSession)        // POST
	mux.HandleFunc("/api/sessions", s.handleListSessions)     // GET
	mux.HandleFunc("/api/session/", s.handleSessionRouter)    // /api/session/<id>/...
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "sessions": s.sessionCount(), "time": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) handleNewSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "threaddump-analyzer" {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body is fine
	id := newSessionID()
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "incident-" + id[:8]
	}
	sess := session.New(id, label)
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "label": label})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	out := make([]map[string]any, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, map[string]any{
			"id":     sess.ID,
			"label":  sess.Label,
			"dumps":  sess.Count(),
		})
	}
	s.mu.RUnlock()
	writeJSON(w, map[string]any{"sessions": out})
}

// handleSessionRouter dispatches the /api/session/<id>/<verb> family. Keeps
// the route table compact and avoids a third-party router dependency.
func (s *Server) handleSessionRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/session/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	id := parts[0]
	verb := ""
	if len(parts) > 1 {
		verb = parts[1]
	}
	s.mu.RLock()
	sess := s.sessions[id]
	s.mu.RUnlock()
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	switch verb {
	case "upload":
		s.handleUpload(w, r, sess)
	case "analysis":
		s.handleAnalysis(w, r, sess)
	case "findings":
		s.handleFindings(w, r, sess)
	case "dumps":
		s.handleDumps(w, r, sess)
	case "":
		// session metadata
		writeJSON(w, map[string]any{"id": sess.ID, "label": sess.Label, "dumps": sess.Count()})
	default:
		http.Error(w, "unknown verb: "+verb, http.StatusNotFound)
	}
}

// handleUpload accepts a single thread dump as the raw request body (text/plain
// or application/octet-stream — we don't care, we just parse). The endpoint is
// idempotent only in the sense that the operator can re-upload to add another
// snapshot; AddDump preserves order.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "threaddump-analyzer" {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	defer r.Body.Close()

	d, err := parser.Parse(r.Body)
	if err != nil {
		http.Error(w, "parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(d.Threads) == 0 {
		http.Error(w, "no threads parsed — is this a JVM thread dump?", http.StatusBadRequest)
		return
	}
	sess.AddDump(d)
	writeJSON(w, map[string]any{
		"ok":      true,
		"threads": len(d.Threads),
		"dumps":   sess.Count(),
		"title":   d.Title,
	})
}

// handleAnalysis returns the raw analyzer output for the latest dump (state
// histogram, top contention, pool stats, signature groups). The UI uses this
// for the detail tables.
func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	dumps := sess.Dumps()
	if len(dumps) == 0 {
		http.Error(w, "session has no dumps yet", http.StatusConflict)
		return
	}
	latest := dumps[len(dumps)-1]
	writeJSON(w, map[string]any{
		"states":     analyzer.States(latest),
		"deadlocks":  analyzer.Deadlocks(latest),
		"contention": analyzer.TopContention(latest, 10),
		"pools":      analyzer.Pools(latest),
		"sig_groups": analyzer.SigGroups(latest, 6),
		"lifelines":  sess.Lifelines(8),
	})
}

// handleFindings returns the ranked findings list — the headline output of
// the whole tool. UIs render this above the analysis tables.
func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if sess.Count() == 0 {
		http.Error(w, "session has no dumps yet", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"findings": findings.For(sess)})
}

// handleDumps returns per-dump metadata (title, thread count, timestamp).
// The full thread-by-thread view is left for a v0.2 endpoint — those
// payloads get large fast.
func (s *Server) handleDumps(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	dumps := sess.Dumps()
	out := make([]map[string]any, 0, len(dumps))
	for i, d := range dumps {
		out = append(out, map[string]any{
			"index":     i,
			"title":     d.Title,
			"timestamp": d.Timestamp,
			"threads":   len(d.Threads),
		})
	}
	writeJSON(w, map[string]any{"dumps": out})
}

func (s *Server) sessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
