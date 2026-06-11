// Package session holds the multi-dump analysis state. The single-dump
// analyzer answers "what's happening right now?"; the session is what makes
// this tool meaningfully ahead of jstack pretty-printers — it answers "what's
// been happening, and is it stuck?"
//
// A session is one logical incident. The operator uploads N dumps spaced
// seconds-to-minutes apart; we maintain per-thread continuity across them
// and surface the threads (and frames) that didn't move.
package session

import (
	"sort"
	"sync"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/analyzer"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

// Session is the in-memory state for one analysis incident. Thread-safe; the
// HTTP handlers take it as a value and use the methods directly.
type Session struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	mu    sync.RWMutex
	dumps []*parser.Dump
}

func New(id, label string) *Session { return &Session{ID: id, Label: label} }

// AddDump records a parsed dump. Dumps are kept in upload order, which is
// assumed to be chronological. (If a corporate ingestor uploads them out of
// order, sort externally before calling AddDump.)
func (s *Session) AddDump(d *parser.Dump) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dumps = append(s.dumps, d)
}

func (s *Session) Dumps() []*parser.Dump {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*parser.Dump, len(s.dumps))
	copy(out, s.dumps)
	return out
}

func (s *Session) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.dumps)
}

// ThreadKey identifies a logical thread across snapshots. We use name+tid
// because Java thread names can be reused (the name "main" usually isn't, but
// "pool-1-thread-3" gets recycled). When tid is available it pins identity;
// when it's not, name alone is the best we have.
type ThreadKey struct {
	Name string `json:"name"`
	TID  string `json:"tid"`
}

// ThreadLifeline is the per-thread state and stack signature across every
// dump in the session. Length == session.Count(). Entries are nil for dumps
// in which the thread doesn't appear (gone or not yet alive).
type ThreadLifeline struct {
	Key      ThreadKey         `json:"key"`
	Pool     string            `json:"pool"`
	States   []*parser.ThreadState `json:"states"`
	StackSigs []string          `json:"stack_sigs"` // empty string when thread absent
	// SignatureRuns counts the longest stretch the StackSig stayed constant.
	// 1 means the stack moved every dump; N means it was frozen across all
	// N dumps in a row. The hang detector picks high values out.
	SignatureRunMax int `json:"signature_run_max"`
}

// Lifelines computes per-thread continuity across the whole session. Top-N
// frames is configurable; 8 is the default — deep enough to ignore framework
// noise at the bottom, shallow enough that small stack moves don't mask a
// frozen call site.
func (s *Session) Lifelines(topFrames int) []ThreadLifeline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.dumps) == 0 {
		return nil
	}
	if topFrames <= 0 {
		topFrames = 8
	}
	idx := map[ThreadKey]*ThreadLifeline{}
	for di, d := range s.dumps {
		for _, t := range d.Threads {
			k := ThreadKey{Name: t.Name, TID: t.TID}
			l := idx[k]
			if l == nil {
				l = &ThreadLifeline{
					Key:       k,
					Pool:      analyzer.Pool(t.Name),
					States:    make([]*parser.ThreadState, len(s.dumps)),
					StackSigs: make([]string, len(s.dumps)),
				}
				idx[k] = l
			}
			st := t.State
			l.States[di] = &st
			l.StackSigs[di] = analyzer.StackSig(t.Frames, topFrames)
		}
	}
	out := make([]ThreadLifeline, 0, len(idx))
	for _, l := range idx {
		// Walk the StackSigs slice and compute the longest run of identical
		// non-empty signatures. That's our "frozen for N dumps in a row"
		// metric — the headline number for hang detection.
		run := 0
		best := 0
		var prev string
		for _, s := range l.StackSigs {
			if s == "" {
				run = 0
				prev = ""
				continue
			}
			if s == prev {
				run++
			} else {
				run = 1
				prev = s
			}
			if run > best {
				best = run
			}
		}
		l.SignatureRunMax = best
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SignatureRunMax != out[j].SignatureRunMax {
			return out[i].SignatureRunMax > out[j].SignatureRunMax
		}
		return out[i].Key.Name < out[j].Key.Name
	})
	return out
}

// FrozenThreads filters Lifelines() to threads whose top-N stack was identical
// across at least minRun consecutive dumps. minRun=2 catches anything stuck
// for two snapshots in a row; minRun=session.Count() catches threads frozen
// the entire incident.
func (s *Session) FrozenThreads(minRun, topFrames int) []ThreadLifeline {
	all := s.Lifelines(topFrames)
	out := make([]ThreadLifeline, 0)
	for _, l := range all {
		if l.SignatureRunMax >= minRun {
			out = append(out, l)
		}
	}
	return out
}
