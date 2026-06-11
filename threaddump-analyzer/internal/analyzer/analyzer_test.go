package analyzer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

// resolveSample finds the bundled deadlock.jstack regardless of where `go test`
// is invoked from. It walks up from CWD looking for examples/deadlock.jstack.
func resolveSample(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		p := filepath.Join(wd, "examples", "deadlock.jstack")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		wd = filepath.Dir(wd)
	}
	t.Fatalf("sample dump not found from %q", wd)
	return ""
}

func parseSample(t *testing.T) *parser.Dump {
	f, err := os.Open(resolveSample(t))
	if err != nil {
		t.Fatalf("open sample: %v", err)
	}
	defer f.Close()
	d, err := parser.Parse(f)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	return d
}

func TestParseBasicCounts(t *testing.T) {
	d := parseSample(t)
	if len(d.Threads) < 6 {
		t.Fatalf("expected >=6 threads, got %d", len(d.Threads))
	}
	gotPool := 0
	for _, th := range d.Threads {
		if Pool(th.Name) == "tomcat" {
			gotPool++
		}
	}
	if gotPool != 4 {
		t.Errorf("expected 4 tomcat threads, got %d", gotPool)
	}
}

func TestDeadlockDetected(t *testing.T) {
	d := parseSample(t)
	cycles := Deadlocks(d)
	if len(cycles) != 1 {
		t.Fatalf("expected exactly 1 deadlock cycle, got %d", len(cycles))
	}
	c := cycles[0]
	if len(c.Threads) != 2 {
		t.Fatalf("expected cycle of 2 threads, got %d (%v)", len(c.Threads), c.Threads)
	}
}

func TestTopContention(t *testing.T) {
	d := parseSample(t)
	top := TopContention(d, 5)
	if len(top) == 0 {
		t.Fatalf("expected at least one contention entry")
	}
	// The Hikari condition lock should have 4 waiters; the deadlock locks have 1 each.
	if got := len(top[0].Waiters); got != 4 {
		t.Errorf("expected first contention entry to have 4 waiters, got %d", got)
	}
}

func TestStateHistogram(t *testing.T) {
	d := parseSample(t)
	h := States(d)
	if len(h) == 0 {
		t.Fatalf("expected non-empty histogram")
	}
	saw := map[parser.ThreadState]int{}
	for _, e := range h {
		saw[e.State] = e.Count
	}
	if saw[parser.StateBlocked] < 2 {
		t.Errorf("expected at least 2 BLOCKED threads, got %d", saw[parser.StateBlocked])
	}
	if saw[parser.StateWaiting] < 4 {
		t.Errorf("expected at least 4 WAITING threads, got %d", saw[parser.StateWaiting])
	}
}
