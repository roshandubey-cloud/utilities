package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Schedule represents one future run. It embeds the exact JSON shape that
// POST /api/start accepts, so triggering is just "decode + startRun".
type Schedule struct {
	ID        string    `json:"id"`
	RunAt     time.Time `json:"run_at"`
	CreatedAt time.Time `json:"created_at"`
	Note      string    `json:"note,omitempty"`
	Config    startReq  `json:"config"`

	// EveryStr (v0.15.0) is the optional repeat interval as a Go
	// time.ParseDuration string (e.g. "1h", "24h", "168h" for weekly).
	// Empty / zero means one-shot (the v0.13/v0.14 default behaviour).
	// On fire, the scheduler re-writes RunAt = RunAt + Every and keeps
	// the entry on disk. Persisted as a string so JSON files round-trip
	// cleanly between server versions.
	EveryStr string `json:"every,omitempty"`
}

// every parses EveryStr into a Duration. Returns 0 (no repeat) on
// empty or invalid values. Accepts the "Xd" extension for days
// (Go's stdlib ParseDuration tops out at hours).
func (s *Schedule) every() time.Duration {
	if s == nil || s.EveryStr == "" {
		return 0
	}
	v := s.EveryStr
	if strings.HasSuffix(v, "d") {
		// "7d" → 168h. Reuse Go's hour parser by swapping the suffix.
		if d, err := time.ParseDuration(strings.TrimSuffix(v, "d") + "h"); err == nil && d > 0 {
			return d * 24
		}
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 0
}

// scheduleStore persists Schedule entries as one JSON file per entry under
// schedulesDir. Listing is a simple ReadDir — cheap at the cadence we
// operate (tens, not thousands, of pending entries).
//
// startedAt records the wall-clock moment the scheduler came online.
// Schedules whose RunAt is *before* this time were "missed during downtime"
// and get dropped without firing on the first sweep — the user asked for
// past runs to be ignored when the server was off, not replayed in a burst
// when it comes back.
type scheduleStore struct {
	dir       string
	startedAt time.Time
	mu        sync.Mutex
}

func newScheduleStore(dir string) *scheduleStore {
	return &scheduleStore{dir: dir, startedAt: time.Now()}
}

func (ss *scheduleStore) path(id string) string {
	return filepath.Join(ss.dir, id+".json")
}

// sanitizeID makes sure the caller-supplied id can't escape the schedules
// directory. The handler only generates well-formed ids, but defense in depth.
func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Disallow separators and hidden-file prefixes.
	if strings.ContainsAny(id, `/\:`) || strings.HasPrefix(id, ".") {
		return ""
	}
	return id
}

func (ss *scheduleStore) save(s Schedule) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if err := os.MkdirAll(ss.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0o600: schedule JSON includes the full RunConfig — passwords and all.
	tmp := ss.path(s.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ss.path(s.ID))
}

func (ss *scheduleStore) delete(id string) error {
	id = sanitizeID(id)
	if id == "" {
		return fmt.Errorf("bad id")
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	err := os.Remove(ss.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// list returns schedules sorted by RunAt ascending. Errors on individual
// malformed files are logged and the file is skipped, not fatal — one bad
// file shouldn't poison the whole view.
func (ss *scheduleStore) list() []Schedule {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(ss.dir)
	if err != nil {
		return nil
	}
	out := make([]Schedule, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ss.dir, e.Name()))
		if err != nil {
			continue
		}
		var s Schedule
		if err := json.Unmarshal(data, &s); err != nil {
			log.Printf("schedule: skip malformed %s: %v", e.Name(), err)
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunAt.Before(out[j].RunAt) })
	return out
}

// POST /api/schedule — create a new schedule. Body is startReq + run_at + note.
func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.schedules == nil {
		http.Error(w, "scheduler disabled (no -schedules-dir configured)", http.StatusServiceUnavailable)
		return
	}
	// Reuse the same shape as /api/start plus run_at+note. We decode into an
	// anonymous wrapper so JSON never loses fields the Schedule struct cares about.
	var req struct {
		RunAt string   `json:"run_at"` // ISO-8601, interpreted in the server's local TZ if no offset
		Note  string   `json:"note"`
		Cfg   startReq `json:"config"`
		Every string   `json:"every"` // v0.15.0 — recurrence; "1h", "24h", "7d", or "" for one-shot
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	runAt, err := parseRunAt(req.RunAt)
	if err != nil {
		http.Error(w, "run_at: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Validate the config shape early so bad input is caught at schedule-time
	// rather than at fire-time (surprising UX).
	if _, err := buildRunConfig(req.Cfg); err != nil {
		http.Error(w, "config: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Pre-flight host-key check at schedule-time. The schedule fires hours
	// later when no operator is around to accept a TOFU prompt, so an
	// untrusted host key would silently fail the run. Mirroring handleStart's
	// pre-flight: if the key is unknown or changed, refuse to schedule and
	// surface the structured response so the UI can offer the same Accept
	// flow before the user confirms the schedule.
	if s.getHostKeyStore() != nil || s.getKnownHostsPath() != "" {
		creds := firstStartCredential(req.Cfg)
		if creds.user != "" && req.Cfg.Host != "" && req.Cfg.Port > 0 {
			// Bastion / SSH ProxyJump (v0.19.x). Same reasoning as the
			// /api/start preflight — schedules fire hours later with no
			// operator around, so the host-key consent must clear *now*
			// against the same wiring (bastion + target) the run will use.
			var preBastion *bastionPreflight
			if req.Cfg.BastionHost != "" {
				preBastion = &bastionPreflight{
					Host: req.Cfg.BastionHost, Port: req.Cfg.BastionPort,
					User: req.Cfg.BastionUser, Pass: req.Cfg.BastionPass,
					PrivateKeyPEM: req.Cfg.BastionPrivateKeyPEM, Passphrase: req.Cfg.BastionPassphrase,
				}
			}
			if pre := s.preflightHostKey(req.Cfg.Host, req.Cfg.Port, creds.user, creds.pass, nil, preBastion); pre != nil {
				writeJSON(w, pre)
				return
			}
		}
	}

	entry := Schedule{
		ID:        fmt.Sprintf("sched-%d", time.Now().UnixNano()),
		RunAt:     runAt,
		CreatedAt: time.Now(),
		Note:      strings.TrimSpace(req.Note),
		Config:    req.Cfg,
		EveryStr:  strings.TrimSpace(req.Every),
	}
	// Validate Every parses (if provided) — fail at schedule time, not
	// at fire time, so a typoed recurrence shows up immediately.
	if entry.EveryStr != "" && entry.every() == 0 {
		http.Error(w, "every: invalid duration (use '1h', '24h', '7d', etc.)", http.StatusBadRequest)
		return
	}
	if err := s.schedules.save(entry); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": entry.ID, "run_at": entry.RunAt})
}

// GET /api/schedules — list pending schedules (sorted by run_at ascending).
func (s *Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		writeJSON(w, map[string]any{"schedules": []Schedule{}})
		return
	}
	writeJSON(w, map[string]any{"schedules": s.schedules.list()})
}

// POST /api/schedule/cancel?id=… — delete a pending schedule.
func (s *Server) handleScheduleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.schedules == nil {
		http.Error(w, "scheduler disabled", http.StatusServiceUnavailable)
		return
	}
	id := r.URL.Query().Get("id")
	if sanitizeID(id) == "" {
		http.Error(w, "missing or bad id", http.StatusBadRequest)
		return
	}
	if err := s.schedules.delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"cancelled": id})
}

// parseRunAt accepts a few common shapes:
//   - "2026-04-24T10:15"        (HTML datetime-local, no offset — local TZ)
//   - "2026-04-24T10:15:30"
//   - "2026-04-24T10:15:30Z"    (RFC3339)
//   - "2026-04-24T10:15-07:00"  (RFC3339)
func parseRunAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("required")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
	}
	for _, layout := range layouts {
		// The tz-less layouts parse as UTC by default; interpret them as local
		// time so "2026-04-24T10:15" behaves the way a user expects.
		loc := time.UTC
		if layout == "2006-01-02T15:04:05" || layout == "2006-01-02T15:04" {
			loc = time.Local
		}
		t, err := time.ParseInLocation(layout, s, loc)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format")
}

// scheduleTicker runs the background sweep. Ticks every 15s — cheap enough
// not to matter at idle (stat-dir, maybe a few tiny file reads), frequent
// enough that a schedule set for "+30 seconds" fires within one interval.
// Stops cleanly when stop channel closes (so tests + shutdown don't leak it).
func (s *Server) scheduleTicker(stop <-chan struct{}) {
	if s.schedules == nil {
		return
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	// Run one sweep immediately in case a schedule was overdue at startup.
	s.scheduleSweep()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.scheduleSweep()
		}
	}
}

func (s *Server) scheduleSweep() {
	now := time.Now()
	list := s.schedules.list()
	for _, sch := range list {
		// Missed-during-downtime: the schedule's RunAt fell before the
		// scheduler came online. Drop it silently — the user doesn't want a
		// week of stale schedules replayed in a burst when the server
		// restarts. Schedules that became overdue *while* the scheduler was
		// running (e.g., waiting for an active run to end) are kept — those
		// reflect intentional queueing, not missed windows.
		if sch.RunAt.Before(s.schedules.startedAt) {
			log.Printf("schedule %s skipped: due %s was before scheduler startup %s",
				sch.ID, sch.RunAt.Format(time.RFC3339), s.schedules.startedAt.Format(time.RFC3339))
			_ = s.schedules.delete(sch.ID)
			continue
		}
		if now.Before(sch.RunAt) {
			continue
		}
		// If a run is already active, leave the schedule alone and retry on
		// the next tick. That means a scheduled run can slip by up to one
		// tick past the end of a preceding run — acceptable.
		if s.activeRun() != nil {
			return
		}
		if _, err := s.startRun(sch.Config, "schedule"); err != nil {
			log.Printf("schedule %s fire failed: %v — dropping", sch.ID, err)
			_ = s.schedules.delete(sch.ID)
			return
		}
		log.Printf("schedule %s fired at %s (was due %s)", sch.ID, now.Format(time.RFC3339), sch.RunAt.Format(time.RFC3339))
		// v0.15.0 — recurring schedules. When Every is set, advance
		// RunAt by Every and persist the updated schedule instead of
		// deleting. If RunAt + Every is still in the past (e.g. a
		// "every 1h" schedule that fired late by 3 hours), advance to
		// the next future tick so the run doesn't immediately re-fire.
		if every := sch.every(); every > 0 {
			next := sch.RunAt.Add(every)
			for !next.After(now) {
				next = next.Add(every)
			}
			sch.RunAt = next
			if err := s.schedules.save(sch); err != nil {
				log.Printf("schedule %s recurrence write failed: %v — dropping", sch.ID, err)
				_ = s.schedules.delete(sch.ID)
			} else {
				log.Printf("schedule %s recurrence: next fire at %s", sch.ID, next.Format(time.RFC3339))
			}
		} else {
			// One-shot: delete after firing. At-most-once semantics
			// stay intact; we never retry a permanently-bad config.
			_ = s.schedules.delete(sch.ID)
		}
		// Only fire one schedule per sweep — let the next tick pick up the
		// next one, and don't pile up if the server was off for a week.
		return
	}
}
