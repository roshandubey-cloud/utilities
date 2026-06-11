// Package parser parses JVM thread-dump output into structured Thread / Lock
// objects. Supports HotSpot jstack and `jcmd Thread.print`, which share the
// same textual format. OpenJ9 javacore is out of scope for v0.1.
//
// The parser is line-oriented and forward-only — no regex backtracking, no
// full-file materialisation. A 100 MB dump streams through it in a few
// hundred milliseconds.
package parser

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ThreadState matches HotSpot's six java.lang.Thread.State values plus an
// "UNKNOWN" sentinel for threads whose state line we couldn't parse. The
// analyzer treats UNKNOWN as a low-confidence signal — never blocks findings.
type ThreadState string

const (
	StateNew          ThreadState = "NEW"
	StateRunnable     ThreadState = "RUNNABLE"
	StateBlocked      ThreadState = "BLOCKED"
	StateWaiting      ThreadState = "WAITING"
	StateTimedWaiting ThreadState = "TIMED_WAITING"
	StateTerminated   ThreadState = "TERMINATED"
	StateUnknown      ThreadState = "UNKNOWN"
)

// Frame is one line of a stack trace. Native indicates "Native Method" or
// "Compiled Method" sentinels — useful when classifying CPU-hot threads.
type Frame struct {
	Class    string `json:"class"`     // fully-qualified, e.g. "java.util.concurrent.locks.ReentrantLock"
	Method   string `json:"method"`    // method name without args
	Source   string `json:"source"`    // file:line, e.g. "ReentrantLock.java:267"
	Native   bool   `json:"native"`
	Compiled bool   `json:"compiled"`
}

// Sig returns a stable per-frame signature suitable for deduplication and
// frozen-frame detection. We use class+method+source so a function relocated
// across versions doesn't get coalesced — that would mask a real change.
func (f Frame) Sig() string { return f.Class + "." + f.Method + "@" + f.Source }

// LockRef is a reference to a monitor or AQS-style lock. ID is the hex address
// the JVM emits, e.g. "0x000000076c1ea970"; Class is "java.util.concurrent.
// locks.ReentrantLock$NonfairSync" or similar.
type LockRef struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	// Op is what the thread was doing with this lock, sourced from the
	// jstack annotation: "locked", "waiting to lock", "parking to wait for",
	// "eliminated", "waiting on". The analyzer keys deadlock detection off
	// this distinction.
	Op string `json:"op"`
}

// Thread is the parsed form of one thread block. Locks slice preserves order
// from the dump — top of stack to bottom — which matters when reconstructing
// lock-acquisition order for cycle detection.
type Thread struct {
	Name      string      `json:"name"`
	ID        int64       `json:"id"`        // # value, e.g. #143
	Daemon    bool        `json:"daemon"`
	Priority  int         `json:"priority"`  // prio=
	OSPrio    int         `json:"os_prio"`   // os_prio=
	TID       string      `json:"tid"`       // native thread id (hex)
	NID       string      `json:"nid"`       // OS-level thread id
	CPU       string      `json:"cpu,omitempty"` // cpu=...ms when present
	State     ThreadState `json:"state"`
	StateNote string      `json:"state_note,omitempty"` // e.g. "(on object monitor)"
	Frames    []Frame     `json:"frames"`
	// Locks the thread holds (Op == "locked") AND locks it's blocked / parking
	// on (Op == "waiting to lock" / "parking to wait for"). Walking the slice
	// gives both — callers filter by Op.
	Locks []LockRef `json:"locks"`
}

// Holds returns the locks this thread currently owns.
func (t Thread) Holds() []LockRef {
	out := make([]LockRef, 0, len(t.Locks))
	for _, l := range t.Locks {
		if l.Op == "locked" {
			out = append(out, l)
		}
	}
	return out
}

// WaitingOn returns the (typically zero or one) lock this thread is blocked
// or parking on. Returns the empty LockRef when not waiting on anything.
func (t Thread) WaitingOn() LockRef {
	for _, l := range t.Locks {
		if l.Op == "waiting to lock" || l.Op == "parking to wait for" || l.Op == "waiting on" {
			return l
		}
	}
	return LockRef{}
}

// Dump is one parsed thread dump. Title is the first line of the file (which
// usually carries the JVM version + dump timestamp); Timestamp is best-effort
// extracted from that line. Threads slice preserves the dump's order.
type Dump struct {
	Title     string    `json:"title"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Threads   []Thread  `json:"threads"`
	// JNIRefs, ClassLoaderLocks etc. are captured but not exposed in v0.1.
	Raw string `json:"-"` // kept for byte-exact re-render, never serialised
}

// Parse consumes the entire stream and returns one Dump. Streams beyond a few
// hundred MB should be split by the caller — we hold the whole file in memory.
func Parse(r io.Reader) (*Dump, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // long frames happen in obfuscated stacks

	d := &Dump{}
	var (
		raw     strings.Builder
		current *Thread // the thread whose body we're currently consuming, nil between blocks
		inLockedOSY bool // inside a "Locked ownable synchronizers:" section
	)
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')

		if d.Title == "" && strings.HasPrefix(line, "Full thread dump") {
			d.Title = line
			d.Timestamp = extractTimestamp(line)
			continue
		}
		if d.Title == "" && strings.Contains(line, "thread dump") {
			d.Title = line
			d.Timestamp = extractTimestamp(line)
			continue
		}

		trimmed := strings.TrimSpace(line)

		// Thread header starts with a double-quoted name. Spans one logical
		// line but can be long; parseHeader reads the trailing tokens too.
		if strings.HasPrefix(trimmed, `"`) {
			if current != nil {
				d.Threads = append(d.Threads, *current)
			}
			t, err := parseHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %q: %w", trimLong(line), err)
			}
			current = &t
			inLockedOSY = false
			continue
		}

		if current == nil {
			// Header / preamble lines we don't recognise — keep walking until
			// the first thread header appears.
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "java.lang.Thread.State:"):
			parseStateLine(current, trimmed)
		case strings.HasPrefix(trimmed, "at "):
			if f, ok := parseAtFrame(trimmed); ok {
				current.Frames = append(current.Frames, f)
			}
		case strings.HasPrefix(trimmed, "- "):
			// "- locked", "- waiting to lock", "- parking to wait for ..."
			// Some JVMs also emit "- None" inside Locked ownable
			// synchronizers, which we want to ignore.
			if inLockedOSY && (trimmed == "- None" || trimmed == "- none") {
				continue
			}
			if l, ok := parseLockLine(trimmed); ok {
				current.Locks = append(current.Locks, l)
			}
		case strings.HasPrefix(trimmed, "Locked ownable synchronizers:"):
			inLockedOSY = true
		case trimmed == "":
			// blank line ends the thread body
			if current != nil {
				d.Threads = append(d.Threads, *current)
				current = nil
				inLockedOSY = false
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		d.Threads = append(d.Threads, *current)
	}
	d.Raw = raw.String()
	return d, nil
}

// parseHeader handles lines like:
//   "http-nio-8080-exec-23" #143 daemon prio=5 os_prio=0 cpu=1234.56ms tid=0x... nid=0x... waiting on condition [0x000000...]
func parseHeader(line string) (Thread, error) {
	t := Thread{}
	// Name is everything between the first pair of unescaped double quotes.
	if line[0] != '"' {
		return t, fmt.Errorf("header missing opening quote")
	}
	end := strings.Index(line[1:], `"`)
	if end < 0 {
		return t, fmt.Errorf("header missing closing quote")
	}
	t.Name = line[1 : 1+end]
	tail := strings.TrimSpace(line[2+end:])

	for _, tok := range strings.Fields(tail) {
		switch {
		case strings.HasPrefix(tok, "#"):
			if v, err := strconv.ParseInt(tok[1:], 10, 64); err == nil {
				t.ID = v
			}
		case tok == "daemon":
			t.Daemon = true
		case strings.HasPrefix(tok, "prio="):
			if v, err := strconv.Atoi(tok[5:]); err == nil {
				t.Priority = v
			}
		case strings.HasPrefix(tok, "os_prio="):
			if v, err := strconv.Atoi(tok[8:]); err == nil {
				t.OSPrio = v
			}
		case strings.HasPrefix(tok, "tid="):
			t.TID = tok[4:]
		case strings.HasPrefix(tok, "nid="):
			t.NID = tok[4:]
		case strings.HasPrefix(tok, "cpu="):
			t.CPU = tok[4:]
		}
	}
	// State tail words ("waiting on condition", "runnable", "in Object.wait()")
	// are informational and overridden by the java.lang.Thread.State line.
	return t, nil
}

// parseStateLine extracts the canonical state from "java.lang.Thread.State: X"
// or "java.lang.Thread.State: X (note)".
func parseStateLine(t *Thread, line string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "java.lang.Thread.State:"))
	noteStart := strings.Index(rest, "(")
	state := rest
	note := ""
	if noteStart > 0 {
		state = strings.TrimSpace(rest[:noteStart])
		closeIdx := strings.LastIndex(rest, ")")
		if closeIdx > noteStart {
			note = strings.TrimSpace(rest[noteStart+1 : closeIdx])
		}
	}
	switch state {
	case "RUNNABLE":
		t.State = StateRunnable
	case "BLOCKED":
		t.State = StateBlocked
	case "WAITING":
		t.State = StateWaiting
	case "TIMED_WAITING":
		t.State = StateTimedWaiting
	case "NEW":
		t.State = StateNew
	case "TERMINATED":
		t.State = StateTerminated
	default:
		t.State = StateUnknown
	}
	t.StateNote = note
}

// parseAtFrame parses "at com.example.Foo.bar(Foo.java:42)" or
// "at com.example.Foo.bar(Native Method)".
func parseAtFrame(line string) (Frame, bool) {
	body := strings.TrimSpace(strings.TrimPrefix(line, "at "))
	openParen := strings.LastIndex(body, "(")
	closeParen := strings.LastIndex(body, ")")
	if openParen <= 0 || closeParen <= openParen {
		return Frame{}, false
	}
	src := body[openParen+1 : closeParen]
	classMethod := body[:openParen]
	dot := strings.LastIndex(classMethod, ".")
	if dot <= 0 {
		return Frame{}, false
	}
	return Frame{
		Class:    classMethod[:dot],
		Method:   classMethod[dot+1:],
		Source:   src,
		Native:   src == "Native Method",
		Compiled: src == "Compiled Method",
	}, true
}

// parseLockLine parses, e.g.:
//   "- locked <0x000000076c1ea970> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)"
//   "- waiting to lock <0x...> (a com.acme.Foo)"
//   "- parking to wait for <0x...> (a ...ConditionObject)"
//   "- waiting on <0x...> (a java.lang.Object)"
func parseLockLine(line string) (LockRef, bool) {
	body := strings.TrimPrefix(line, "- ")
	addrStart := strings.Index(body, "<")
	addrEnd := strings.Index(body, ">")
	if addrStart < 0 || addrEnd < addrStart {
		return LockRef{}, false
	}
	op := strings.TrimSpace(body[:addrStart])
	id := body[addrStart+1 : addrEnd]

	className := ""
	if classStart := strings.Index(body[addrEnd:], "(a "); classStart >= 0 {
		rest := body[addrEnd+classStart+3:]
		if classEnd := strings.LastIndex(rest, ")"); classEnd > 0 {
			className = rest[:classEnd]
		}
	}
	// Normalise common variants so analyzer comparisons are stable.
	switch op {
	case "eliminated":
		op = "locked" // OpenJDK's eliminated-lock optimisation note
	}
	return LockRef{ID: id, Class: className, Op: op}, true
}

// extractTimestamp pulls the date/time prefix that HotSpot prints at the top
// of every dump (e.g. "2026-04-28 12:34:56"). Returns zero time when missing.
func extractTimestamp(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if len(s) >= len(layout) {
			if t, err := time.Parse(layout, s[:len(layout)]); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func trimLong(s string) string {
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}
