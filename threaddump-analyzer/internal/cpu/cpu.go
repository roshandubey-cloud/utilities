// Package cpu parses `top -H -p <pid>` and `pidstat -t -p <pid>` output into
// a per-thread CPU% table that can be joined to a thread dump via NID (the
// OS-level thread id printed by jstack).
//
// Without this, "which thread is hot?" is guesswork — jstack shows stacks
// but not utilisation. With it, the analyzer ranks threads by real CPU% and
// surfaces the actual CPU-bound culprits, not just the deeply-stacked ones.
//
// Inputs are line-oriented text grabbed by the operator at roughly the same
// time as the thread dump. We're tolerant: any header layout works, we look
// for a column whose header contains "%CPU" and another containing "PID" or
// "TID", and that's enough.
package cpu

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// ThreadCPU is one parsed row.
type ThreadCPU struct {
	NID     string  `json:"nid"`     // OS-level thread id, decimal (matches jstack's nid=0x... after hex conversion)
	Percent float64 `json:"percent"` // %CPU as reported
	Command string  `json:"command,omitempty"`
}

// Sample is a collection of per-thread CPU readings parsed from one upload.
type Sample struct {
	Source  string      `json:"source"` // "top -H" / "pidstat" / "unknown"
	Threads []ThreadCPU `json:"threads"`
}

// Parse sniffs the input format and dispatches to the right backend. Both
// `top -H` and `pidstat -t` produce header lines we can recognise.
func Parse(r io.Reader) (*Sample, error) {
	all, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := string(all)
	switch {
	case strings.Contains(text, "%CPU") && strings.Contains(text, "PID"):
		return parseTop(text), nil
	case strings.Contains(text, "pidstat") || strings.Contains(text, "TID"):
		return parsePidstat(text), nil
	default:
		// Best-effort: try top first, then pidstat.
		if s := parseTop(text); len(s.Threads) > 0 {
			return s, nil
		}
		return parsePidstat(text), nil
	}
}

// parseTop walks `top -H` output. The header line containing "PID" and
// "%CPU" defines column positions; we use whitespace-split, which is robust
// for top's spacing. Threads beyond what we identified as "Threads" rows
// (anything after the header) get accumulated.
func parseTop(text string) *Sample {
	s := &Sample{Source: "top -H"}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var pidCol, cpuCol, cmdCol int = -1, -1, -1
	headerSeen := false
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if !headerSeen {
			for i, f := range fields {
				switch f {
				case "PID":
					pidCol = i
				case "%CPU":
					cpuCol = i
				case "COMMAND":
					cmdCol = i
				}
			}
			if pidCol >= 0 && cpuCol >= 0 {
				headerSeen = true
			}
			continue
		}
		if len(fields) <= cpuCol || len(fields) <= pidCol {
			continue
		}
		pct, err := strconv.ParseFloat(fields[cpuCol], 64)
		if err != nil {
			continue
		}
		cmd := ""
		if cmdCol >= 0 && cmdCol < len(fields) {
			cmd = strings.Join(fields[cmdCol:], " ")
		}
		s.Threads = append(s.Threads, ThreadCPU{
			NID:     fields[pidCol], // top's PID column in -H mode is the TID
			Percent: pct,
			Command: cmd,
		})
	}
	return s
}

// parsePidstat walks `pidstat -t -p <pid> 1 N` output. Each block has its
// own header; we just look for "TID" and "%CPU" on any line. Decimal commas
// vs dots vary by locale — we accept either.
func parsePidstat(text string) *Sample {
	s := &Sample{Source: "pidstat"}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var tidCol, cpuCol, cmdCol int = -1, -1, -1
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// Re-detect header anytime we see one — pidstat reprints with -p.
		hasTID, hasCPU := false, false
		for i, f := range fields {
			switch f {
			case "TID":
				tidCol = i
				hasTID = true
			case "%CPU":
				cpuCol = i
				hasCPU = true
			case "Command":
				cmdCol = i
			}
		}
		if hasTID && hasCPU {
			continue
		}
		if tidCol < 0 || cpuCol < 0 {
			continue
		}
		if len(fields) <= cpuCol || len(fields) <= tidCol {
			continue
		}
		// Skip the per-process aggregate row (TID column is "-")
		if fields[tidCol] == "-" || fields[tidCol] == "" {
			continue
		}
		raw := strings.Replace(fields[cpuCol], ",", ".", 1)
		pct, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		cmd := ""
		if cmdCol >= 0 && cmdCol < len(fields) {
			cmd = strings.Join(fields[cmdCol:], " ")
		}
		s.Threads = append(s.Threads, ThreadCPU{
			NID:     fields[tidCol],
			Percent: pct,
			Command: cmd,
		})
	}
	return s
}

// JoinByNID joins the CPU sample to a list of (name, nid) pairs and returns
// the per-thread percentage, with name preserved. NIDs are matched on
// numeric value — jstack prints them in hex with "0x" prefix; we accept
// either form on either side.
type Joined struct {
	Name    string  `json:"name"`
	NID     string  `json:"nid"`
	Percent float64 `json:"percent"`
}

// JoinByNID converts each thread's hex nid to decimal and looks it up in
// the sample. Returns Joined rows for matched threads, sorted by %CPU desc.
func (s *Sample) JoinByNID(threads []struct{ Name, NID string }) []Joined {
	if s == nil || len(s.Threads) == 0 {
		return nil
	}
	byDec := map[string]float64{}
	for _, t := range s.Threads {
		byDec[normaliseNID(t.NID)] = t.Percent
	}
	out := []Joined{}
	for _, t := range threads {
		pct, ok := byDec[normaliseNID(t.NID)]
		if !ok {
			continue
		}
		out = append(out, Joined{Name: t.Name, NID: t.NID, Percent: pct})
	}
	// sort desc
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Percent > out[i].Percent {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// normaliseNID returns a decimal representation regardless of input base.
// Tolerant: returns the original string when parsing fails so unknown
// values are still distinguishable (they just won't match anything).
func normaliseNID(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if v, err := strconv.ParseInt(s[2:], 16, 64); err == nil {
			return strconv.FormatInt(v, 10)
		}
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	return s
}
