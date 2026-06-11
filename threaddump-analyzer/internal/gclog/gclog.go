// Package gclog parses a JVM GC log (HotSpot / OpenJDK Unified Logging
// format) into per-pause records that the session can overlay onto the
// thread-dump timeline. The goal is to answer one question cheaply: did
// the thread freeze we just detected coincide with a GC stop-the-world?
//
// Unified Logging format (Java 9+):
//
//   [2026-04-28T12:34:56.789+0000][info][gc] GC(42) Pause Young (Normal) (G1 Evacuation Pause) 256M->96M(512M) 38.2ms
//   [2026-04-28T12:34:58.789+0000][info][gc] GC(43) Pause Remark 96M->96M(512M) 12.0ms
//
// Pre-9 PrintGCDetails:
//
//   2026-04-28T12:34:56.789+0000: 12.34: [GC pause (G1 Evacuation Pause), 0.0382 secs]
//
// We accept both. Anything we can't parse is silently skipped.
package gclog

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pause is one GC pause event.
type Pause struct {
	At       time.Time     `json:"at"`
	Kind     string        `json:"kind"`     // "Young", "Mixed", "Full", "Remark", ...
	Duration time.Duration `json:"duration"`
	Detail   string        `json:"detail"`   // free-text trailing tokens for context
}

// Log is the parsed result. Pauses are returned in ascending time order so
// the overlay can binary-search for the pauses surrounding a given dump.
type Log struct {
	Pauses []Pause `json:"pauses"`
}

// Parse reads the entire stream and returns the Log. The parser is
// intentionally tolerant — invalid lines are skipped rather than producing
// an error, since GC logs frequently mix multiple log tags and we only
// care about [gc] entries.
func Parse(r io.Reader) (*Log, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := &Log{}
	for scanner.Scan() {
		line := scanner.Text()
		if p, ok := parseUnified(line); ok {
			out.Pauses = append(out.Pauses, p)
			continue
		}
		if p, ok := parseLegacy(line); ok {
			out.Pauses = append(out.Pauses, p)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out.Pauses, func(i, j int) bool { return out.Pauses[i].At.Before(out.Pauses[j].At) })
	return out, nil
}

// reUnified extracts (timestamp)…(message)(durationMs).
var reUnified = regexp.MustCompile(`^\[([0-9T:+\-\.]+)\]\[[^\]]+\]\[gc\] (.*?)(\d+(?:\.\d+)?)ms\s*$`)

func parseUnified(line string) (Pause, bool) {
	m := reUnified.FindStringSubmatch(line)
	if m == nil {
		return Pause{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, m[1])
	if err != nil {
		// Some loggers emit "+0000" without colon; try common variants.
		alt := strings.Replace(m[1], "+0000", "+00:00", 1)
		at, err = time.Parse(time.RFC3339Nano, alt)
		if err != nil {
			return Pause{}, false
		}
	}
	ms, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return Pause{}, false
	}
	body := strings.TrimSpace(m[2])
	kind := extractKind(body)
	return Pause{
		At:       at,
		Kind:     kind,
		Duration: time.Duration(ms * float64(time.Millisecond)),
		Detail:   body,
	}, true
}

// reLegacy is loose on purpose — pre-9 GC formats vary by collector.
var reLegacy = regexp.MustCompile(`^([0-9T:+\-\.]+):\s+[0-9.]+:\s+\[(GC[^\],]+)(?:.*?),?\s*([0-9.]+)\s*secs\]`)

func parseLegacy(line string) (Pause, bool) {
	m := reLegacy.FindStringSubmatch(line)
	if m == nil {
		return Pause{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, m[1])
	if err != nil {
		alt := strings.Replace(m[1], "+0000", "+00:00", 1)
		at, err = time.Parse(time.RFC3339Nano, alt)
		if err != nil {
			return Pause{}, false
		}
	}
	secs, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return Pause{}, false
	}
	return Pause{
		At:       at,
		Kind:     extractKind(m[2]),
		Duration: time.Duration(secs * float64(time.Second)),
		Detail:   strings.TrimSpace(m[2]),
	}, true
}

func extractKind(body string) string {
	low := strings.ToLower(body)
	switch {
	case strings.Contains(low, "pause full"), strings.Contains(low, "full gc"):
		return "Full"
	case strings.Contains(low, "pause mixed"):
		return "Mixed"
	case strings.Contains(low, "pause young"):
		return "Young"
	case strings.Contains(low, "pause remark"):
		return "Remark"
	case strings.Contains(low, "pause cleanup"):
		return "Cleanup"
	case strings.Contains(low, "pause initial-mark"):
		return "InitialMark"
	}
	if strings.HasPrefix(body, "GC ") {
		// pre-9 best-effort
		return strings.TrimSpace(strings.TrimPrefix(body, "GC "))
	}
	return "GC"
}

// Within returns the pauses whose start time falls inside [from, to]. Used
// by the overlay to ask "what GC happened around the time of this dump?".
func (l *Log) Within(from, to time.Time) []Pause {
	if l == nil {
		return nil
	}
	out := []Pause{}
	for _, p := range l.Pauses {
		if (p.At.Equal(from) || p.At.After(from)) && (p.At.Equal(to) || p.At.Before(to)) {
			out = append(out, p)
		}
	}
	return out
}

// Stats returns a one-line summary for the API's overlay endpoint.
type Stats struct {
	Pauses         int           `json:"pauses"`
	TotalDuration  time.Duration `json:"total_duration"`
	MaxDuration    time.Duration `json:"max_duration"`
	First          time.Time     `json:"first,omitempty"`
	Last           time.Time     `json:"last,omitempty"`
	FullCount      int           `json:"full_count"`
	MixedCount     int           `json:"mixed_count"`
	YoungCount     int           `json:"young_count"`
}

func (l *Log) Stats() Stats {
	s := Stats{}
	if l == nil || len(l.Pauses) == 0 {
		return s
	}
	s.Pauses = len(l.Pauses)
	s.First = l.Pauses[0].At
	s.Last = l.Pauses[len(l.Pauses)-1].At
	for _, p := range l.Pauses {
		s.TotalDuration += p.Duration
		if p.Duration > s.MaxDuration {
			s.MaxDuration = p.Duration
		}
		switch p.Kind {
		case "Full":
			s.FullCount++
		case "Mixed":
			s.MixedCount++
		case "Young":
			s.YoungCount++
		}
	}
	return s
}
