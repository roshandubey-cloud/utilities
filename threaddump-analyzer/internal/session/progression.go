package session

import (
	"sort"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

// LockProgression is one row in the "who-held-what across dumps" view —
// the multi-dump answer to "is this contention transient or stuck?". Per
// lock id, we record the holder thread name and the count of waiters in
// each dump in the session.
type LockProgression struct {
	LockID     string             `json:"lock_id"`
	LockClass  string             `json:"lock_class,omitempty"`
	Holders    []string           `json:"holders"`     // per dump; "" when lock isn't held
	WaiterCnts []int              `json:"waiter_cnts"` // per dump
	// HolderStable is true when one named thread held the lock in every dump
	// where it was held at all — the strongest signal of a stuck monitor.
	HolderStable bool `json:"holder_stable"`
	// PeakWaiters across all dumps. Used by the findings engine for impact.
	PeakWaiters int `json:"peak_waiters"`
}

// LockProgressions returns one entry per lock id observed in any dump. The
// result is sorted by PeakWaiters descending so the most-contested locks
// surface first.
func (s *Session) LockProgressions() []LockProgression {
	dumps := s.Dumps()
	if len(dumps) == 0 {
		return nil
	}
	type slot struct {
		holders []string
		waiters []int
		class   string
	}
	state := map[string]*slot{}

	for di, d := range dumps {
		// holder per lock id in this dump
		holderByID := map[string]string{}
		classByID := map[string]string{}
		for _, t := range d.Threads {
			for _, h := range t.Holds() {
				holderByID[h.ID] = t.Name
				classByID[h.ID] = h.Class
			}
		}
		waitersByID := map[string]int{}
		for _, t := range d.Threads {
			wo := t.WaitingOn()
			if wo.ID == "" {
				continue
			}
			waitersByID[wo.ID]++
			if _, ok := classByID[wo.ID]; !ok {
				classByID[wo.ID] = wo.Class
			}
		}
		// merge dump's observations into state, padding missing ids
		seen := map[string]bool{}
		for id := range holderByID {
			seen[id] = true
		}
		for id := range waitersByID {
			seen[id] = true
		}
		for id := range seen {
			sl := state[id]
			if sl == nil {
				sl = &slot{
					holders: make([]string, len(dumps)),
					waiters: make([]int, len(dumps)),
					class:   classByID[id],
				}
				state[id] = sl
			}
			sl.holders[di] = holderByID[id] // "" when not held this dump
			sl.waiters[di] = waitersByID[id]
		}
	}

	out := make([]LockProgression, 0, len(state))
	for id, sl := range state {
		peak := 0
		for _, n := range sl.waiters {
			if n > peak {
				peak = n
			}
		}
		// stability: same non-empty holder in every dump where one was named
		stable := false
		named := ""
		seenDiff := false
		for _, h := range sl.holders {
			if h == "" {
				continue
			}
			if named == "" {
				named = h
				continue
			}
			if named != h {
				seenDiff = true
				break
			}
		}
		stable = named != "" && !seenDiff
		out = append(out, LockProgression{
			LockID:       id,
			LockClass:    sl.class,
			Holders:      sl.holders,
			WaiterCnts:   sl.waiters,
			HolderStable: stable,
			PeakWaiters:  peak,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeakWaiters != out[j].PeakWaiters {
			return out[i].PeakWaiters > out[j].PeakWaiters
		}
		return out[i].LockID < out[j].LockID
	})
	return out
}

// PredictedDeadlock is a partial wait-for chain that doesn't currently form
// a cycle but would if one more edge were added. Useful early-warning for
// lock-ordering bugs that haven't yet deadlocked under live traffic.
type PredictedDeadlock struct {
	Chain []string         `json:"chain"`   // thread names along the chain
	Locks []parser.LockRef `json:"locks"`   // locks along the chain
	// Closer is the thread name whose stack contains both endpoint locks —
	// the most likely place the cycle would close. Empty when no candidate
	// could be identified.
	Closer string `json:"closer,omitempty"`
}

// PredictDeadlocks walks the wait-for graph of the latest dump and reports
// long-running chains (≥3 threads) plus any thread whose stack already
// references both endpoints' locks — that's the closer who'd seal the
// cycle in the next iteration of the bug. Always runs against the last
// dump in the session; partial-cycle prediction across dumps is interesting
// future work but not yet wired.
func (s *Session) PredictDeadlocks() []PredictedDeadlock {
	dumps := s.Dumps()
	if len(dumps) == 0 {
		return nil
	}
	d := dumps[len(dumps)-1]

	holderByLock := map[string]string{}
	for _, t := range d.Threads {
		for _, h := range t.Holds() {
			holderByLock[h.ID] = t.Name
		}
	}
	type edge struct {
		to   string
		lock parser.LockRef
	}
	wait := map[string]edge{}
	threadByName := map[string]parser.Thread{}
	for _, t := range d.Threads {
		threadByName[t.Name] = t
		wo := t.WaitingOn()
		if wo.ID == "" {
			continue
		}
		holder, ok := holderByLock[wo.ID]
		if !ok || holder == t.Name {
			continue
		}
		wait[t.Name] = edge{to: holder, lock: wo}
	}

	out := []PredictedDeadlock{}
	// Find chains of length ≥3 that DON'T cycle. Real cycles are reported
	// by Deadlocks() and excluded here.
	for start := range wait {
		visited := map[string]bool{}
		path := []string{}
		locks := []parser.LockRef{}
		cur := start
		for {
			if visited[cur] {
				// cycle — already covered by Deadlocks()
				path = nil
				break
			}
			visited[cur] = true
			path = append(path, cur)
			e, ok := wait[cur]
			if !ok {
				break
			}
			locks = append(locks, e.lock)
			cur = e.to
		}
		if len(path) >= 3 && len(locks) >= 2 {
			// look for a closer: any thread whose stack mentions both
			// endpoint locks (head waits on locks[0]; tail holds locks[last])
			startLockID := locks[0].ID
			endLockID := locks[len(locks)-1].ID
			closer := ""
			for _, t := range d.Threads {
				holdsEnd := false
				waitsStart := false
				for _, h := range t.Holds() {
					if h.ID == endLockID {
						holdsEnd = true
					}
				}
				wo := t.WaitingOn()
				if wo.ID == startLockID {
					waitsStart = true
				}
				if holdsEnd && waitsStart {
					closer = t.Name
					break
				}
			}
			out = append(out, PredictedDeadlock{
				Chain:  append([]string(nil), path...),
				Locks:  append([]parser.LockRef(nil), locks...),
				Closer: closer,
			})
		}
	}
	// Dedupe by canonical chain key (longest-only — strict-superset chains
	// are reported, shorter prefixes are dropped).
	sort.Slice(out, func(i, j int) bool { return len(out[i].Chain) > len(out[j].Chain) })
	dedup := []PredictedDeadlock{}
	seen := map[string]bool{}
	for _, p := range out {
		key := p.Chain[0]
		if seen[key] {
			continue
		}
		seen[key] = true
		dedup = append(dedup, p)
	}
	return dedup
}
