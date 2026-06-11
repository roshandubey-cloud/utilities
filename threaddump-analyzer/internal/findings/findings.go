// Package findings turns analyzer + session output into the ranked,
// operator-facing verdicts the UI shows at the top of every analysis page.
//
// Findings are the entire point of this tool. Anyone can render a histogram
// and a deadlock cycle — the difference is whether the operator sees the
// answer ("HikariCP is exhausted, the holder is thread X stuck on Oracle
// network read, look at OrderService.kt:148") or has to assemble it
// themselves from raw tables.
package findings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/analyzer"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/session"
)

// Severity is intentionally coarse — operators want "fix now" vs "look at
// later", not a 1–10 scale they have to interpret. Confidence is the granular
// signal.
type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Info     Severity = "info"
)

// Finding is one ranked verdict. ImpactCount is the number of threads / files
// / requests this finding affects (used by the UI to badge "187 workers
// affected"). Evidence is a flat list of breadcrumbs that lead from the
// verdict back to the raw dump — never a black box.
type Finding struct {
	ID          string   `json:"id"`           // stable per-kind id, e.g. "deadlock-0", "pool-exhaustion-tomcat"
	Kind        string   `json:"kind"`         // "DEADLOCK", "POOL_EXHAUSTION", "HANG_SIGNATURE", "CONTENTION", "ANTI_PATTERN"
	Severity    Severity `json:"severity"`
	Confidence  int      `json:"confidence"`   // 0–100
	Headline    string   `json:"headline"`     // one-sentence summary, copy-pastable into a Jira title
	Detail      string   `json:"detail"`       // a paragraph of context — what we saw, why it matters
	Remediation string   `json:"remediation"`  // one-paragraph action — what to do next
	ImpactCount int      `json:"impact_count"`
	Evidence    []string `json:"evidence"`     // breadcrumbs, e.g. lock-id, thread names, frame signatures
}

// Severity ordering used by the ranking pass.
func sevWeight(s Severity) int {
	switch s {
	case Critical:
		return 4
	case High:
		return 3
	case Medium:
		return 2
	case Info:
		return 1
	}
	return 0
}

// Sort orders findings by severity (high first) then confidence (high first).
// The UI renders in this order without further sorting.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if sevWeight(fs[i].Severity) != sevWeight(fs[j].Severity) {
			return sevWeight(fs[i].Severity) > sevWeight(fs[j].Severity)
		}
		if fs[i].Confidence != fs[j].Confidence {
			return fs[i].Confidence > fs[j].Confidence
		}
		if fs[i].ImpactCount != fs[j].ImpactCount {
			return fs[i].ImpactCount > fs[j].ImpactCount
		}
		return fs[i].ID < fs[j].ID
	})
}

// For runs the entire findings pipeline against a (potentially multi-dump)
// session and returns the ranked verdict list. Empty session → empty result.
func For(s *session.Session) []Finding {
	dumps := s.Dumps()
	if len(dumps) == 0 {
		return nil
	}
	out := []Finding{}

	// Run per-dump findings against the most recent dump. Multi-dump
	// findings (frozen frames) run against the whole session.
	latest := dumps[len(dumps)-1]
	out = append(out, deadlockFindings(latest)...)
	out = append(out, poolExhaustionFindings(latest)...)
	out = append(out, contentionFindings(latest)...)
	if len(dumps) >= 2 {
		out = append(out, hangFindings(s)...)
	}
	out = append(out, antiPatternFindings(latest)...)
	out = append(out, dumpSummary(s))

	Sort(out)
	return out
}

func deadlockFindings(d *parser.Dump) []Finding {
	cycles := analyzer.Deadlocks(d)
	out := make([]Finding, 0, len(cycles))
	for i, c := range cycles {
		evi := []string{
			"cycle: " + strings.Join(c.Threads, " → "),
		}
		for _, l := range c.Locks {
			evi = append(evi, fmt.Sprintf("lock %s (%s)", l.ID, l.Class))
		}
		out = append(out, Finding{
			ID:          fmt.Sprintf("deadlock-%d", i),
			Kind:        "DEADLOCK",
			Severity:    Critical,
			Confidence:  100,
			Headline:    fmt.Sprintf("Deadlock cycle of %d threads", len(c.Threads)),
			Detail:      "These threads are mutually blocked: each waits for a lock held by the next, with the cycle closing on the first. The JVM will not break the cycle on its own; a thread interrupt or a kill is the only way out.",
			Remediation: "Identify the acquisition-order mismatch in the code that takes both locks. Either impose a global lock ordering or replace one lock with a try-lock + back-off. The cycle members and the lock identities are listed below; trace each thread's stack to the line that calls into the second lock.",
			ImpactCount: len(c.Threads),
			Evidence:    evi,
		})
	}
	return out
}

func poolExhaustionFindings(d *parser.Dump) []Finding {
	pools := analyzer.Pools(d)
	out := make([]Finding, 0, 2)
	for _, p := range pools {
		// Threshold: >50% blocked OR >80% blocked+waiting in a recognised pool
		blocked := p.ByState[parser.StateBlocked]
		waiting := p.ByState[parser.StateWaiting] + p.ByState[parser.StateTimedWaiting]
		ratioBlocked := float64(blocked) / float64(p.Threads)
		ratioStuck := float64(blocked+waiting) / float64(p.Threads)
		if p.Threads < 4 {
			continue
		}
		if ratioBlocked < 0.5 && ratioStuck < 0.8 {
			continue
		}
		sev := High
		conf := 75
		if ratioBlocked >= 0.9 {
			sev = Critical
			conf = 95
		}
		out = append(out, Finding{
			ID:          "pool-exhaustion-" + p.Pool,
			Kind:        "POOL_EXHAUSTION",
			Severity:    sev,
			Confidence:  conf,
			Headline:    fmt.Sprintf("%s pool saturated: %d/%d threads stuck", p.Pool, blocked+waiting, p.Threads),
			Detail:      fmt.Sprintf("%.0f%% of the %d threads in the %s pool are BLOCKED, with another %d in WAITING/TIMED_WAITING. New work hitting this pool will queue or be rejected until existing threads return.", ratioBlocked*100, p.Threads, p.Pool, waiting),
			Remediation: "Check the contention finding below for the lock most of these threads are waiting on — that lock's holder is the root cause. If the holder itself is stuck on an upstream call (DB, downstream HTTP, etc.), increasing this pool's size won't help; fix the upstream stall.",
			ImpactCount: blocked + waiting,
			Evidence:    []string{fmt.Sprintf("%s pool size: %d", p.Pool, p.Threads), fmt.Sprintf("blocked: %d", blocked), fmt.Sprintf("waiting: %d", waiting)},
		})
	}
	return out
}

func contentionFindings(d *parser.Dump) []Finding {
	top := analyzer.TopContention(d, 5)
	out := make([]Finding, 0, len(top))
	for _, c := range top {
		if len(c.Waiters) < 3 {
			continue
		}
		holderText := c.HolderTID
		if holderText == "" {
			holderText = "(not present in this dump — likely a synchronizer, AQS-style)"
		}
		sample := c.Waiters
		if len(sample) > 5 {
			sample = sample[:5]
		}
		out = append(out, Finding{
			ID:          "contention-" + c.Lock.ID,
			Kind:        "CONTENTION",
			Severity:    sevForWaiters(len(c.Waiters)),
			Confidence:  90,
			Headline:    fmt.Sprintf("%d threads contending for one lock (%s)", len(c.Waiters), shortClass(c.Lock.Class)),
			Detail:      fmt.Sprintf("Lock %s (%s) is the single largest queue. Holder: %s. The waiters are mostly from the same pool — fix the holder and the pool clears.", c.Lock.ID, c.Lock.Class, holderText),
			Remediation: "Open the holder's stack and find the call that took the lock. Common fixes: shorten the critical section, replace a synchronized block with a ConcurrentMap, or switch to a finer-grained lock. If the holder is doing I/O while holding the lock, that's almost always the bug.",
			ImpactCount: len(c.Waiters),
			Evidence:    append([]string{fmt.Sprintf("holder: %s", holderText), "waiters (sample): " + strings.Join(sample, ", ")}, fmt.Sprintf("lock id: %s", c.Lock.ID)),
		})
	}
	return out
}

func sevForWaiters(n int) Severity {
	switch {
	case n >= 50:
		return Critical
	case n >= 10:
		return High
	default:
		return Medium
	}
}

// hangFindings is the marquee multi-dump finding. Threads whose top-N stack
// hasn't moved across all the supplied dumps are very strong evidence the
// process is making no forward progress — far more reliable than a single
// dump can ever be on its own.
func hangFindings(s *session.Session) []Finding {
	dumps := s.Dumps()
	if len(dumps) < 2 {
		return nil
	}
	frozen := s.FrozenThreads(len(dumps), 8) // frozen across every dump
	if len(frozen) == 0 {
		return nil
	}
	// Group frozen threads by their final stack signature so we say
	// "47 Tomcat workers stuck in jdbc.read" rather than 47 separate findings.
	groups := map[string][]session.ThreadLifeline{}
	for _, l := range frozen {
		key := ""
		if len(l.StackSigs) > 0 {
			key = l.StackSigs[len(l.StackSigs)-1]
		}
		groups[key] = append(groups[key], l)
	}
	out := make([]Finding, 0, len(groups))
	idx := 0
	for sig, ls := range groups {
		if sig == "" || len(ls) == 0 {
			continue
		}
		names := make([]string, 0, len(ls))
		pools := map[string]int{}
		for _, l := range ls {
			names = append(names, l.Key.Name)
			if l.Pool != "" {
				pools[l.Pool]++
			}
		}
		poolLabel := "mixed"
		if len(pools) == 1 {
			for p := range pools {
				poolLabel = p
			}
		}
		sample := names
		if len(sample) > 8 {
			sample = sample[:8]
		}
		out = append(out, Finding{
			ID:          fmt.Sprintf("hang-%d", idx),
			Kind:        "HANG_SIGNATURE",
			Severity:    High,
			Confidence:  85 + min(len(dumps)*3, 14), // 85→99 as we see more dumps
			Headline:    fmt.Sprintf("%d %s threads frozen across all %d dumps", len(ls), poolLabel, len(dumps)),
			Detail:      "These threads have an identical top-8 stack in every dump uploaded for this session. They are making no forward progress.",
			Remediation: "Examine the shared stack tip. If it's a network read (JDBC, HTTP), the upstream service is the problem, not your JVM. If it's a synchronized block or AQS park, find the holder via the contention finding above.",
			ImpactCount: len(ls),
			Evidence:    append([]string{"signature: " + sig, "sample: " + strings.Join(sample, ", ")}, fmt.Sprintf("dumps observed: %d", len(dumps))),
		})
		idx++
	}
	return out
}

// antiPatternFindings encodes a small library of known-bad patterns the
// findings engine can spot on a single dump. v0.1 ships with the obvious ones;
// the catalog grows in subsequent releases and via the future user-defined DSL.
func antiPatternFindings(d *parser.Dump) []Finding {
	out := []Finding{}

	// finaliser-queue clog: many threads stuck in
	// java.lang.ref.Finalizer.runFinalizer
	finalCount := 0
	for _, t := range d.Threads {
		for _, f := range t.Frames {
			if f.Class == "java.lang.ref.Finalizer" && f.Method == "runFinalizer" {
				finalCount++
				break
			}
		}
	}
	if finalCount > 4 {
		out = append(out, Finding{
			ID:          "anti-finalizer-clog",
			Kind:        "ANTI_PATTERN",
			Severity:    High,
			Confidence:  75,
			Headline:    fmt.Sprintf("Finalizer queue clog: %d threads in runFinalizer", finalCount),
			Detail:      "When the finaliser queue grows faster than the single finaliser thread can drain it, GC stalls and memory pressure builds. The classic cause is heavy use of objects with non-trivial finalize() — typically legacy I/O wrappers.",
			Remediation: "Audit the codebase for finalize() overrides; replace with explicit Closeable + try-with-resources or Cleaner. Don't tune the queue — fix the source.",
			ImpactCount: finalCount,
		})
	}

	// classloading lock contention: blocked threads with ClassLoader.loadClass
	clCount := 0
	for _, t := range d.Threads {
		if t.State != parser.StateBlocked {
			continue
		}
		for _, f := range t.Frames {
			if strings.HasSuffix(f.Class, "ClassLoader") && f.Method == "loadClass" {
				clCount++
				break
			}
		}
	}
	if clCount > 5 {
		out = append(out, Finding{
			ID:          "anti-classloading-contention",
			Kind:        "ANTI_PATTERN",
			Severity:    Medium,
			Confidence:  70,
			Headline:    fmt.Sprintf("Class-loading contention: %d threads BLOCKED in loadClass", clCount),
			Detail:      "Multiple threads contending for the classloader monitor is usually a startup-time symptom — many workers warming up at once. If it persists, look for first-touch lazy initialisation in hot paths.",
			Remediation: "Pre-warm classes at startup (eager init in main) or migrate to a parallel classloader. Spring's lazy-init=true under heavy load is a frequent offender.",
			ImpactCount: clCount,
		})
	}

	return out
}

// dumpSummary is always emitted, lowest severity. It anchors the operator
// with the basics — total thread count, state mix, dumps present — so they
// know the analysis ran cleanly.
func dumpSummary(s *session.Session) Finding {
	dumps := s.Dumps()
	latest := dumps[len(dumps)-1]
	hist := analyzer.States(latest)
	evi := []string{fmt.Sprintf("dumps in session: %d", len(dumps)), fmt.Sprintf("threads in latest: %d", len(latest.Threads))}
	for _, h := range hist {
		evi = append(evi, fmt.Sprintf("%s: %d", h.State, h.Count))
	}
	return Finding{
		ID:          "summary",
		Kind:        "SUMMARY",
		Severity:    Info,
		Confidence:  100,
		Headline:    fmt.Sprintf("%d threads in latest dump, %d dump(s) in session", len(latest.Threads), len(dumps)),
		Detail:      "Baseline figures for orientation. The findings above this one are the items worth acting on.",
		ImpactCount: len(latest.Threads),
		Evidence:    evi,
	}
}

func shortClass(c string) string {
	if i := strings.LastIndex(c, "."); i >= 0 {
		return c[i+1:]
	}
	return c
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
