// Package web glues parser/analyzer/session/findings/patterns/gclog/cpu
// into one HTTP service. Same security envelope as sftp-loadtest:
// X-Requested-With CSRF guard, body-size cap, bind to 127.0.0.1 by default.
package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/analyzer"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/cpu"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/findings"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/gclog"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/patterns"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/session"
)

//go:embed static/*
var staticFS embed.FS

// Server is the in-process state: session registry + the shared pattern
// registry. Both are immutable after startup so all reads are lock-free.
type Server struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
	// auxiliary per-session state: parsed GC log + CPU sample (raw text
	// preserved too for re-parse on rule changes).
	gcLogs map[string]*gclog.Log
	cpuSamples map[string]*cpu.Sample
	auxRaw map[string]map[string]string // sessionID -> {gc:..., cpu:...}

	patternReg *patterns.Registry
	dataDir    string

	maxBody int64
}

func NewServer(dataDir string, patternReg *patterns.Registry) *Server {
	s := &Server{
		sessions:   map[string]*session.Session{},
		gcLogs:     map[string]*gclog.Log{},
		cpuSamples: map[string]*cpu.Sample{},
		auxRaw:     map[string]map[string]string{},
		patternReg: patternReg,
		dataDir:    dataDir,
		maxBody:    128 << 20,
	}
	if dataDir != "" {
		if loaded, err := session.LoadAll(dataDir); err == nil {
			for _, sess := range loaded {
				s.sessions[sess.ID] = sess
			}
			if len(loaded) > 0 {
				log.Printf("rehydrated %d session(s) from %s", len(loaded), dataDir)
			}
		} else {
			log.Printf("persistence load: %v (continuing with empty state)", err)
		}
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/patterns", s.handlePatternList)
	mux.HandleFunc("/api/session", s.handleNewSession)
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/session/", s.handleSessionRouter)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"ok":       true,
		"sessions": s.sessionCount(),
		"patterns": len(s.patternReg.Rules()),
		"data_dir": s.dataDir,
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handlePatternList(w http.ResponseWriter, r *http.Request) {
	rules := s.patternReg.Rules()
	out := make([]map[string]any, 0, len(rules))
	for _, rl := range rules {
		out = append(out, map[string]any{
			"id":          rl.ID,
			"kind":        rl.Kind,
			"severity":    rl.Severity,
			"confidence":  rl.Confidence,
			"headline":    rl.Headline,
			"min_threads": rl.MinThreads,
		})
	}
	writeJSON(w, map[string]any{"patterns": out})
}

func (s *Server) handleNewSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.csrfOK(r) {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := newSessionID()
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "incident-" + id[:8]
	}
	sess := session.New(id, label)
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	if err := sess.Save(s.dataDir); err != nil {
		log.Printf("save session %s: %v", id, err)
	}
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
			"id":    sess.ID,
			"label": sess.Label,
			"dumps": sess.Count(),
		})
	}
	s.mu.RUnlock()
	writeJSON(w, map[string]any{"sessions": out})
}

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
	case "upload-gclog":
		s.handleUploadGCLog(w, r, sess)
	case "upload-cpu":
		s.handleUploadCPU(w, r, sess)
	case "analysis":
		s.handleAnalysis(w, r, sess)
	case "findings":
		s.handleFindings(w, r, sess)
	case "progressions":
		s.handleProgressions(w, r, sess)
	case "predictions":
		s.handlePredictions(w, r, sess)
	case "patterns":
		s.handleApplyPatterns(w, r, sess)
	case "dumps":
		s.handleDumps(w, r, sess)
	case "":
		writeJSON(w, map[string]any{"id": sess.ID, "label": sess.Label, "dumps": sess.Count()})
	default:
		http.Error(w, "unknown verb: "+verb, http.StatusNotFound)
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.csrfOK(r) {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	defer r.Body.Close()

	d, err := parser.ParseAuto(r.Body)
	if err != nil {
		http.Error(w, "parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(d.Threads) == 0 {
		http.Error(w, "no threads parsed — is this a JVM thread dump?", http.StatusBadRequest)
		return
	}
	sess.AddDump(d)
	if err := sess.Save(s.dataDir); err != nil {
		log.Printf("save session %s: %v", sess.ID, err)
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"threads": len(d.Threads),
		"dumps":   sess.Count(),
		"title":   d.Title,
	})
}

func (s *Server) handleUploadGCLog(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.csrfOK(r) {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return
	}
	g, err := gclog.Parse(strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "parse gc log: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.gcLogs[sess.ID] = g
	if s.auxRaw[sess.ID] == nil {
		s.auxRaw[sess.ID] = map[string]string{}
	}
	s.auxRaw[sess.ID]["gc"] = string(body)
	gcRaw := s.auxRaw[sess.ID]["gc"]
	cpuRaw := s.auxRaw[sess.ID]["cpu"]
	s.mu.Unlock()
	_ = sess.SaveAuxiliary(s.dataDir, gcRaw, cpuRaw)
	writeJSON(w, map[string]any{"ok": true, "stats": g.Stats()})
}

func (s *Server) handleUploadCPU(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !s.csrfOK(r) {
		http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return
	}
	c, err := cpu.Parse(strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "parse cpu: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.cpuSamples[sess.ID] = c
	if s.auxRaw[sess.ID] == nil {
		s.auxRaw[sess.ID] = map[string]string{}
	}
	s.auxRaw[sess.ID]["cpu"] = string(body)
	gcRaw := s.auxRaw[sess.ID]["gc"]
	cpuRaw := s.auxRaw[sess.ID]["cpu"]
	s.mu.Unlock()
	_ = sess.SaveAuxiliary(s.dataDir, gcRaw, cpuRaw)
	writeJSON(w, map[string]any{"ok": true, "rows": len(c.Threads), "source": c.Source})
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	dumps := sess.Dumps()
	if len(dumps) == 0 {
		http.Error(w, "session has no dumps yet", http.StatusConflict)
		return
	}
	latest := dumps[len(dumps)-1]
	out := map[string]any{
		"states":     analyzer.States(latest),
		"deadlocks":  analyzer.Deadlocks(latest),
		"contention": analyzer.TopContention(latest, 10),
		"pools":      analyzer.Pools(latest),
		"sig_groups": analyzer.SigGroups(latest, 6),
		"lifelines":  sess.Lifelines(8),
		"progressions": sess.LockProgressions(),
		"predictions":  sess.PredictDeadlocks(),
	}
	s.mu.RLock()
	g := s.gcLogs[sess.ID]
	c := s.cpuSamples[sess.ID]
	s.mu.RUnlock()
	if g != nil {
		out["gc_stats"] = g.Stats()
	}
	if c != nil {
		// Join CPU sample to the latest dump's threads. We deliberately use
		// (name, nid) pairs since that's the join key on the analyzer side.
		pairs := make([]struct{ Name, NID string }, 0, len(latest.Threads))
		for _, t := range latest.Threads {
			pairs = append(pairs, struct{ Name, NID string }{t.Name, t.NID})
		}
		out["cpu_top"] = c.JoinByNID(pairs)
	}
	writeJSON(w, out)
}

func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if sess.Count() == 0 {
		http.Error(w, "session has no dumps yet", http.StatusConflict)
		return
	}
	out := findings.For(sess)
	// Pattern matches augment built-in findings.
	if s.patternReg != nil {
		dumps := sess.Dumps()
		latest := dumps[len(dumps)-1]
		for _, m := range s.patternReg.Apply(latest) {
			out = append(out, findings.Finding{
				ID:          m.ID,
				Kind:        m.Kind,
				Severity:    findings.Severity(m.Severity),
				Confidence:  m.Confidence,
				Headline:    m.Headline,
				Detail:      m.Detail,
				Remediation: m.Remediation,
				ImpactCount: len(m.Threads),
				Evidence:    append([]string{"matched threads (sample): " + samplePeek(m.Threads, 8)}),
			})
		}
		findings.Sort(out)
	}
	writeJSON(w, map[string]any{"findings": out})
}

func (s *Server) handleProgressions(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	writeJSON(w, map[string]any{"progressions": sess.LockProgressions()})
}

func (s *Server) handlePredictions(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	writeJSON(w, map[string]any{"predictions": sess.PredictDeadlocks()})
}

// handleApplyPatterns reapplies every pattern against the latest dump and
// returns the matches. Useful for "I just added a new pattern, what hits?"
// without re-uploading the dump.
func (s *Server) handleApplyPatterns(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	dumps := sess.Dumps()
	if len(dumps) == 0 {
		http.Error(w, "session has no dumps yet", http.StatusConflict)
		return
	}
	matches := s.patternReg.Apply(dumps[len(dumps)-1])
	writeJSON(w, map[string]any{"matches": matches})
}

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

func (s *Server) csrfOK(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "threaddump-analyzer"
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

func samplePeek(ts []string, n int) string {
	if len(ts) <= n {
		return strings.Join(ts, ", ")
	}
	return strings.Join(ts[:n], ", ") + fmt.Sprintf(" (+%d more)", len(ts)-n)
}
