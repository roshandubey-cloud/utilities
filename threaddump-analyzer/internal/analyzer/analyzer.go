// Package analyzer turns a parsed Dump into structured analysis primitives:
// state histograms, deadlock cycles, contention rankings, pool classification,
// stack-signature deduplication. The findings package consumes these and
// produces the operator-facing verdicts.
//
// Everything here is pure — no I/O, no shared state — so it's trivially
// parallelisable across many dumps (the session package fans these out).
package analyzer

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

// StateHistogram is one entry per ThreadState that appears in the dump.
// Ordered by canonical state sequence so the UI gets stable rendering.
type StateHistogram []StateCount

type StateCount struct {
	State parser.ThreadState `json:"state"`
	Count int                `json:"count"`
}

// States counts the threads by Thread.State and returns the histogram in a
// stable canonical order (NEW, RUNNABLE, BLOCKED, WAITING, TIMED_WAITING,
// TERMINATED, UNKNOWN).
func States(d *parser.Dump) StateHistogram {
	counts := map[parser.ThreadState]int{}
	for _, t := range d.Threads {
		counts[t.State]++
	}
	order := []parser.ThreadState{
		parser.StateNew, parser.StateRunnable, parser.StateBlocked,
		parser.StateWaiting, parser.StateTimedWaiting,
		parser.StateTerminated, parser.StateUnknown,
	}
	out := make(StateHistogram, 0, len(order))
	for _, s := range order {
		if c := counts[s]; c > 0 {
			out = append(out, StateCount{State: s, Count: c})
		}
	}
	return out
}

// DeadlockCycle is one connected set of threads that wait on locks held by
// each other's predecessor in the cycle, forming a cycle. Each entry in the
// slice is one node in the cycle order — Threads[i] holds Locks[i] and is
// waiting on Locks[(i+1)%len].
type DeadlockCycle struct {
	Threads []string         `json:"threads"`
	Locks   []parser.LockRef `json:"locks"`
}

// Deadlocks performs a classic wait-for graph cycle search. For every
// BLOCKED / WAITING / TIMED_WAITING thread we draw an edge from "thread
// waiting" to "thread that holds the lock the waiter wants". A cycle
// in that graph IS a deadlock — same algorithm the JVM's own
// findDeadlocks() uses. Returns one entry per disjoint cycle.
//
// Complexity is O(threads + locks). On a 5000-thread dump it runs in
// single-digit milliseconds.
func Deadlocks(d *parser.Dump) []DeadlockCycle {
	// holderByLock: lock-id -> thread name that holds it.
	holderByLock := map[string]string{}
	for _, t := range d.Threads {
		for _, h := range t.Holds() {
			holderByLock[h.ID] = t.Name
		}
	}
	// waitForGraph: thread-name -> {next-thread, lock-id-being-waited-on}.
	type edge struct{ to string; lock parser.LockRef }
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

	// Tarjan-lite: visit every node, follow edges until we revisit or run
	// out. Record visited so we don't double-report the same cycle.
	const (
		unmarked = 0
		onStack  = 1
		done     = 2
	)
	mark := map[string]int{}
	var cycles []DeadlockCycle

	for start := range wait {
		if mark[start] != unmarked {
			continue
		}
		path := []string{}
		seenIdx := map[string]int{}
		cur := start
		for {
			if idx, ok := seenIdx[cur]; ok {
				// Found a cycle from idx → end of path
				cycle := path[idx:]
				locks := make([]parser.LockRef, len(cycle))
				for i, name := range cycle {
					locks[i] = wait[name].lock
				}
				cycles = append(cycles, DeadlockCycle{Threads: append([]string(nil), cycle...), Locks: locks})
				for _, n := range path {
					mark[n] = done
				}
				break
			}
			if mark[cur] != unmarked {
				for _, n := range path {
					mark[n] = done
				}
				break
			}
			seenIdx[cur] = len(path)
			path = append(path, cur)
			mark[cur] = onStack
			e, ok := wait[cur]
			if !ok {
				for _, n := range path {
					mark[n] = done
				}
				break
			}
			cur = e.to
		}
	}
	return dedupeCycles(cycles)
}

// dedupeCycles collapses rotations of the same cycle. Cycles are returned in
// their natural traversal order but a (A, B, C) cycle from start=A is the
// same incident as (B, C, A) from start=B.
func dedupeCycles(cs []DeadlockCycle) []DeadlockCycle {
	seen := map[string]bool{}
	out := make([]DeadlockCycle, 0, len(cs))
	for _, c := range cs {
		key := canonicalCycleKey(c.Threads)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func canonicalCycleKey(names []string) string {
	if len(names) == 0 {
		return ""
	}
	minIdx := 0
	for i := 1; i < len(names); i++ {
		if names[i] < names[minIdx] {
			minIdx = i
		}
	}
	rot := append(append([]string(nil), names[minIdx:]...), names[:minIdx]...)
	return strings.Join(rot, "→")
}

// Contention is the per-lock view: who holds it, how many threads are
// blocked on it. Sorted by waiters descending. Locks with zero waiters are
// omitted — they're not interesting.
type Contention struct {
	Lock      parser.LockRef `json:"lock"`
	HolderTID string         `json:"holder"` // thread name; empty when the holder's not in this dump
	Waiters   []string       `json:"waiters"`
}

// TopContention returns up to n entries of the most-contended monitors.
func TopContention(d *parser.Dump, n int) []Contention {
	holder := map[string]string{}
	for _, t := range d.Threads {
		for _, h := range t.Holds() {
			holder[h.ID] = t.Name
		}
	}
	waiters := map[string][]string{}
	lockByID := map[string]parser.LockRef{}
	for _, t := range d.Threads {
		wo := t.WaitingOn()
		if wo.ID == "" {
			continue
		}
		waiters[wo.ID] = append(waiters[wo.ID], t.Name)
		lockByID[wo.ID] = wo
	}
	out := make([]Contention, 0, len(waiters))
	for id, ws := range waiters {
		out = append(out, Contention{
			Lock:      lockByID[id],
			HolderTID: holder[id],
			Waiters:   ws,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Waiters) != len(out[j].Waiters) {
			return len(out[i].Waiters) > len(out[j].Waiters)
		}
		return out[i].Lock.ID < out[j].Lock.ID
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Pool classifies a thread by name pattern into a known runtime/framework
// pool. Returns "" for threads that don't match any known prefix — those
// are app-owned and findings render them as such.
//
// The classifier is intentionally conservative: it matches strong signals
// (Tomcat's "http-nio-", HikariCP's "HikariPool-", Netty's "nioEventLoop-")
// rather than guessing from substrings. False positives here mislead the
// findings engine; better to leave an unknown thread uncategorised than to
// mis-blame a pool.
func Pool(name string) string {
	switch {
	case strings.HasPrefix(name, "http-nio-"), strings.HasPrefix(name, "http-bio-"),
		strings.HasPrefix(name, "ajp-nio-"), strings.HasPrefix(name, "Catalina-utility-"):
		return "tomcat"
	case strings.HasPrefix(name, "qtp"):
		return "jetty"
	case strings.HasPrefix(name, "HikariPool-"):
		return "hikari"
	case strings.HasPrefix(name, "DB-pool-"), strings.HasPrefix(name, "C3P0PooledConnectionPoolManager"):
		return "c3p0"
	case strings.HasPrefix(name, "nioEventLoopGroup-"), strings.HasPrefix(name, "epollEventLoopGroup-"):
		return "netty"
	case strings.HasPrefix(name, "reactor-http-"), strings.HasPrefix(name, "reactor-tcp-"):
		return "reactor"
	case strings.HasPrefix(name, "kafka-producer-"), strings.HasPrefix(name, "kafka-consumer-"),
		strings.HasPrefix(name, "kafka-coordinator-"):
		return "kafka"
	case strings.HasPrefix(name, "default-dispatcher-"), strings.HasPrefix(name, "akka.actor.default-dispatcher"):
		return "akka"
	case strings.HasPrefix(name, "grpc-"):
		return "grpc"
	case strings.HasPrefix(name, "ForkJoinPool"), strings.HasPrefix(name, "commonPool-worker"):
		return "forkjoin"
	case strings.HasPrefix(name, "GC ") || name == "VM Thread" || name == "VM Periodic Task Thread" ||
		name == "Service Thread" || name == "C1 CompilerThread" || strings.HasPrefix(name, "C2 CompilerThread"):
		return "jvm"
	case strings.HasPrefix(name, "Reference Handler"), strings.HasPrefix(name, "Finalizer"),
		strings.HasPrefix(name, "Signal Dispatcher"):
		return "jvm-internal"
	case strings.HasPrefix(name, "scheduling-"), strings.HasPrefix(name, "TaskScheduler-"):
		return "scheduler"
	}
	return ""
}

// PoolStats groups threads by Pool() result and counts states per pool.
type PoolStats struct {
	Pool       string                       `json:"pool"`
	Threads    int                          `json:"threads"`
	ByState    map[parser.ThreadState]int   `json:"by_state"`
	BlockedPct float64                      `json:"blocked_pct"`
}

// Pools returns one entry per recognised pool, sorted by BlockedPct
// descending so the findings engine sees the most-saturated pool first.
func Pools(d *parser.Dump) []PoolStats {
	agg := map[string]*PoolStats{}
	for _, t := range d.Threads {
		p := Pool(t.Name)
		if p == "" {
			continue
		}
		ps := agg[p]
		if ps == nil {
			ps = &PoolStats{Pool: p, ByState: map[parser.ThreadState]int{}}
			agg[p] = ps
		}
		ps.Threads++
		ps.ByState[t.State]++
	}
	out := make([]PoolStats, 0, len(agg))
	for _, ps := range agg {
		if ps.Threads > 0 {
			b := ps.ByState[parser.StateBlocked]
			ps.BlockedPct = float64(b) / float64(ps.Threads) * 100.0
		}
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BlockedPct != out[j].BlockedPct {
			return out[i].BlockedPct > out[j].BlockedPct
		}
		return out[i].Pool < out[j].Pool
	})
	return out
}

// StackSig returns a stable hash of the (top-N) frames of a stack. Used by
// both the dedup view ("12 threads share the same stack") and the frozen-
// frame detector ("this thread's stack didn't change for 90 s").
//
// Empty stacks return a sentinel hash so they group together cleanly.
func StackSig(frames []parser.Frame, top int) string {
	if top <= 0 || top > len(frames) {
		top = len(frames)
	}
	h := sha1.New()
	if top == 0 {
		h.Write([]byte("(no stack)"))
	} else {
		for i := 0; i < top; i++ {
			h.Write([]byte(frames[i].Sig()))
			h.Write([]byte{'\n'})
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SigGroup is a deduped set of threads sharing the same top-N stack signature.
type SigGroup struct {
	Sig     string   `json:"sig"`
	Sample  []parser.Frame `json:"sample"` // the actual frames, for the UI
	State   parser.ThreadState `json:"state"`
	Threads []string `json:"threads"`
}

// SigGroups groups every thread in the dump by StackSig(top=topN). The
// returned slice is sorted by group size descending — the biggest cluster
// usually IS the symptom.
func SigGroups(d *parser.Dump, topN int) []SigGroup {
	m := map[string]*SigGroup{}
	for _, t := range d.Threads {
		sig := StackSig(t.Frames, topN)
		g := m[sig]
		if g == nil {
			g = &SigGroup{Sig: sig, State: t.State}
			if topN > 0 && topN <= len(t.Frames) {
				g.Sample = t.Frames[:topN]
			} else {
				g.Sample = t.Frames
			}
			m[sig] = g
		}
		g.Threads = append(g.Threads, t.Name)
	}
	out := make([]SigGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Threads) != len(out[j].Threads) {
			return len(out[i].Threads) > len(out[j].Threads)
		}
		return out[i].Sig < out[j].Sig
	})
	return out
}
