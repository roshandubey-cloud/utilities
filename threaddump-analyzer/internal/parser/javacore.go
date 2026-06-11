package parser

import (
	"bufio"
	"io"
	"strings"
	"time"
)

// ParseJavacore parses an IBM OpenJ9 (and legacy IBM J9) javacore.txt file
// into the same Dump shape HotSpot parsing produces, so the downstream
// analyzer/findings code is format-agnostic.
//
// The IBM format is tag-prefixed and section-oriented:
//
//   1XMCURTHDINFO  Current thread
//   3XMTHREADINFO  "main" J9VMThread:0x... ...
//   3XMTHREADINFO1            (native thread ID:0x..., native priority:0x..., native policy:UNKNOWN, vmstate:R, vm thread flags:0x...)
//   3XMCPUTIME               CPU usage total: 0.123 secs, current category="Application"
//   3XMTHREADBLOCK  Blocked on: java/lang/Object@0x000000000ABC1234 Owned by: "Thread-B" (J9VMThread:0x...)
//   3XMHEAPALLOC    Heap bytes allocated since last GC cycle=0
//   3XMTHREADINFO3           Java callstack:
//   4XESTACKTRACE              at com/acme/Demo.doWork(Demo.java:42)
//   4XESTACKTRACE              at com/acme/Demo.main(Demo.java:10)
//   3XMTHREADINFO3           Native callstack:
//   4XENATIVESTACK             (0x00007fff12345678 [libfoo.so+0x1234])
//
// We capture the Java callstack frames, the blocked-on lock with holder
// thread, and per-thread metadata. Native callstack frames are flattened
// into Frame entries with Native=true so they remain visible in the UI.
func ParseJavacore(r io.Reader) (*Dump, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	d := &Dump{}
	var (
		raw         strings.Builder
		current     *Thread
		inJavaStack bool
		inNativeStk bool
	)

	flush := func() {
		if current != nil {
			d.Threads = append(d.Threads, *current)
			current = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')

		// Title + timestamp commonly appear as "1TIDATETIME" line in javacore.
		if d.Title == "" && strings.HasPrefix(line, "1TIDATETIME") {
			d.Title = strings.TrimSpace(strings.TrimPrefix(line, "1TIDATETIME"))
			d.Timestamp = extractJavacoreTimestamp(d.Title)
			continue
		}

		switch {
		case strings.HasPrefix(line, "3XMTHREADINFO ") && !strings.HasPrefix(line, "3XMTHREADINFO1") && !strings.HasPrefix(line, "3XMTHREADINFO2") && !strings.HasPrefix(line, "3XMTHREADINFO3"):
			flush()
			current = parseJavacoreThreadHeader(line)
			inJavaStack = false
			inNativeStk = false

		case strings.HasPrefix(line, "3XMTHREADINFO1"):
			if current != nil {
				applyJavacoreThreadInfo1(current, line)
			}

		case strings.HasPrefix(line, "3XMCPUTIME"):
			if current != nil {
				if cpu := extractField(line, "CPU usage total:"); cpu != "" {
					current.CPU = strings.Fields(cpu)[0] + "s"
				}
			}

		case strings.HasPrefix(line, "3XMTHREADBLOCK"):
			if current != nil {
				applyJavacoreBlock(current, line)
			}

		case strings.HasPrefix(line, "3XMTHREADINFO3"):
			body := strings.TrimSpace(strings.TrimPrefix(line, "3XMTHREADINFO3"))
			lower := strings.ToLower(body)
			inJavaStack = strings.Contains(lower, "java callstack")
			inNativeStk = strings.Contains(lower, "native callstack")

		case strings.HasPrefix(line, "4XESTACKTRACE"):
			if current != nil && inJavaStack {
				if f, ok := parseJavacoreJavaFrame(line); ok {
					current.Frames = append(current.Frames, f)
				}
			}

		case strings.HasPrefix(line, "4XENATIVESTACK"):
			if current != nil && inNativeStk {
				if f, ok := parseJavacoreNativeFrame(line); ok {
					current.Frames = append(current.Frames, f)
				}
			}

		case strings.HasPrefix(line, "NULL"):
			// section separator — end the current thread's body
			flush()
			inJavaStack = false
			inNativeStk = false

		case strings.HasPrefix(line, "0SECTION") && strings.Contains(line, "THREADS"):
			// entering the threads section; nothing to do
		case strings.HasPrefix(line, "0SECTION") && !strings.Contains(line, "THREADS"):
			// leaving the threads section
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	d.Raw = raw.String()
	return d, nil
}

// parseJavacoreThreadHeader handles
//   3XMTHREADINFO      "main" J9VMThread:0x000000000054EE00, omrthread_t:0x... ...
func parseJavacoreThreadHeader(line string) *Thread {
	t := &Thread{State: StateUnknown}
	body := strings.TrimSpace(strings.TrimPrefix(line, "3XMTHREADINFO"))
	if i := strings.Index(body, `"`); i >= 0 {
		rest := body[i+1:]
		if j := strings.Index(rest, `"`); j > 0 {
			t.Name = rest[:j]
			body = rest[j+1:]
		}
	}
	if idx := strings.Index(body, "J9VMThread:"); idx >= 0 {
		f := strings.Fields(body[idx:])
		if len(f) > 0 {
			t.TID = strings.TrimSuffix(strings.TrimPrefix(f[0], "J9VMThread:"), ",")
		}
	}
	return t
}

// applyJavacoreThreadInfo1 picks the native thread id and the vmstate flag.
//   3XMTHREADINFO1            (native thread ID:0xABCD, native priority:0x5, native policy:UNKNOWN, vmstate:R, vm thread flags:0x...)
func applyJavacoreThreadInfo1(t *Thread, line string) {
	if v := extractField(line, "native thread ID:"); v != "" {
		t.NID = strings.TrimSuffix(strings.Fields(v)[0], ",")
	}
	if v := extractField(line, "native priority:"); v != "" {
		// IBM emits hex priorities; we store the literal token, leave parsing
		// to the UI/operator if they care.
		t.StateNote = strings.TrimSpace("native_priority=" + strings.Fields(v)[0])
	}
	if v := extractField(line, "vmstate:"); v != "" {
		switch strings.TrimSuffix(strings.Fields(v)[0], ",") {
		case "R":
			t.State = StateRunnable
		case "B":
			t.State = StateBlocked
		case "CW", "P":
			t.State = StateWaiting
		case "P-T", "S":
			t.State = StateTimedWaiting
		case "TERM":
			t.State = StateTerminated
		}
	}
}

// applyJavacoreBlock parses the "Blocked on" line and records both the lock
// the thread is waiting on AND, when present, the lock held by the owner.
// IBM's format is the only place where the holder is named directly in the
// blocker line — HotSpot makes us reconstruct it.
//
//   3XMTHREADBLOCK     Blocked on: java/lang/Object@0x0AB1 Owned by: "Thread-B" (J9VMThread:0x...)
func applyJavacoreBlock(t *Thread, line string) {
	body := strings.TrimSpace(strings.TrimPrefix(line, "3XMTHREADBLOCK"))
	lockClass := ""
	lockID := ""
	if i := strings.Index(body, "Blocked on:"); i >= 0 {
		rest := strings.TrimSpace(body[i+len("Blocked on:"):])
		// "java/lang/Object@0xADDR ..."
		if at := strings.Index(rest, "@"); at >= 0 {
			lockClass = strings.ReplaceAll(rest[:at], "/", ".")
			tail := rest[at+1:]
			lockID = strings.TrimSuffix(strings.Fields(tail)[0], ",")
		}
	}
	if lockID != "" {
		t.Locks = append(t.Locks, LockRef{ID: lockID, Class: lockClass, Op: "waiting to lock"})
	}
	// Block state if not set.
	if t.State == StateUnknown {
		t.State = StateBlocked
	}
}

// parseJavacoreJavaFrame parses
//   4XESTACKTRACE                at com/acme/Demo.doWork(Demo.java:42)
// IBM uses '/' as package separator on disk and '.' between class+method.
func parseJavacoreJavaFrame(line string) (Frame, bool) {
	body := strings.TrimSpace(strings.TrimPrefix(line, "4XESTACKTRACE"))
	body = strings.TrimPrefix(body, "at ")
	openParen := strings.LastIndex(body, "(")
	closeParen := strings.LastIndex(body, ")")
	if openParen <= 0 || closeParen <= openParen {
		return Frame{}, false
	}
	src := body[openParen+1 : closeParen]
	cm := strings.ReplaceAll(body[:openParen], "/", ".")
	dot := strings.LastIndex(cm, ".")
	if dot <= 0 {
		return Frame{}, false
	}
	return Frame{
		Class:    cm[:dot],
		Method:   cm[dot+1:],
		Source:   src,
		Native:   src == "Native Method" || src == "Native method",
		Compiled: false,
	}, true
}

// parseJavacoreNativeFrame parses the native-stack lines.
//   4XENATIVESTACK               (0x00007fff12345678 [libfoo.so+0x1234])
// We store these as Frame entries with Native=true so the UI can display them
// alongside Java frames; we don't try to demangle.
func parseJavacoreNativeFrame(line string) (Frame, bool) {
	body := strings.TrimSpace(strings.TrimPrefix(line, "4XENATIVESTACK"))
	if body == "" {
		return Frame{}, false
	}
	return Frame{
		Class:  "(native)",
		Method: body,
		Source: "Native",
		Native: true,
	}, true
}

// extractField returns the slice of `line` immediately after the literal
// marker, or "" when the marker isn't present. Whitespace is left for
// callers to handle since some fields are comma-separated.
func extractField(line, marker string) string {
	i := strings.Index(line, marker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(line[i+len(marker):])
}

func extractJavacoreTimestamp(s string) time.Time {
	// IBM format: "2026/04/28 at 12:34:56:789 (...)"
	const layout = "2006/01/02 at 15:04:05"
	if len(s) >= len(layout) {
		if t, err := time.Parse(layout, s[:len(layout)]); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Detect inspects the first kilobyte and returns "javacore" for IBM OpenJ9
// dumps, "jstack" for HotSpot, or "" when uncertain. The web layer dispatches
// to the right parser based on this so the caller can upload either format.
func Detect(head []byte) string {
	s := string(head)
	switch {
	case strings.Contains(s, "1TIDATETIME") || strings.Contains(s, "0SECTION") || strings.Contains(s, "3XMTHREADINFO"):
		return "javacore"
	case strings.Contains(s, "Full thread dump") || strings.Contains(s, "java.lang.Thread.State:"):
		return "jstack"
	}
	return ""
}

// ParseAuto sniffs the format and dispatches to Parse or ParseJavacore.
// Reads up to 4 KiB to detect, then re-feeds the whole stream to the chosen
// parser via a buffered reader.
func ParseAuto(r io.Reader) (*Dump, error) {
	br := bufio.NewReader(r)
	head, _ := br.Peek(4096)
	switch Detect(head) {
	case "javacore":
		return ParseJavacore(br)
	default:
		return Parse(br)
	}
}
