// Package analyze turns a finished run's metrics into a short ordered list
// of human-readable findings. The goal is to tell the operator, in plain
// English, where the bottleneck was (network vs CPU vs concurrency vs
// server) and what to change in the next run to push past it.
//
// All inputs are values already on RunMeta — we never re-sample at analysis
// time. That keeps the function pure, easy to unit-test, and identical
// whether it runs at seal time or later from the persisted JSON.
package analyze

import (
	"fmt"
	"math"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
)

// Severity ranks each finding so the UI can colour them and so the analyser
// can sort the most actionable items to the top.
const (
	SeverityCritical = "critical"
	SeverityWarn     = "warn"
	SeverityInfo     = "info"
)

// Suggest inspects a sealed RunMeta and returns an ordered list of findings.
// Most severe first, then most actionable. Empty slice means "nothing notable
// — the run hit its target without stress".
func Suggest(m persist.RunMeta) []persist.Suggestion {
	out := []persist.Suggestion{}

	total := m.TotalFiles
	skips := m.DispatchSkips
	attempted := total + skips
	skipPct := 0.0
	if attempted > 0 {
		skipPct = float64(skips) / float64(attempted) * 100.0
	}
	failPct := 0.0
	if total > 0 {
		failPct = float64(m.FailedFiles) / float64(total) * 100.0
	}

	// --- Capacity ceiling --------------------------------------------------
	if skips > 0 {
		sev := SeverityWarn
		if skipPct >= 25 {
			sev = SeverityCritical
		}
		// Distinguish "host has CPU headroom — concurrency is the limit"
		// from "host CPU is pinned — adding concurrency won't help".
		hostHeadroom := m.PeakCPUPercent > 0 && m.PeakCPUPercent < 70
		var action string
		switch {
		case hostHeadroom:
			action = fmt.Sprintf(
				"Local host has CPU headroom (peak %.0f%%). Raise parallel_streams from %d to %d, or add more upload users, to absorb the requested rate.",
				m.PeakCPUPercent, m.ParallelStreams, suggestParallel(m),
			)
		case m.PeakCPUPercent >= 85:
			action = fmt.Sprintf(
				"Local CPU is the limit (peak %.0f%% across %d cores). Reduce files_per_minute to ~%d, or run from a host with more cores.",
				m.PeakCPUPercent, m.NumCPU, suggestFpmFromCPU(m),
			)
		default:
			action = fmt.Sprintf(
				"Increase parallel_streams (currently %d) per upload user, or add more users.",
				m.ParallelStreams,
			)
		}
		out = append(out, persist.Suggestion{
			Severity: sev,
			Title:    fmt.Sprintf("Capacity ceiling — %.1f%% of attempted files were skipped at dispatch.", skipPct),
			Detail: fmt.Sprintf(
				"%d of %d attempted files were dropped because every SSH slot for an upload user was busy when the dispatcher tried to send. The run finished below the requested files-per-minute target.",
				skips, attempted,
			),
			Action: action,
		})
	}

	// --- Failure rate ------------------------------------------------------
	if total > 0 && failPct >= 5 {
		sev := SeverityWarn
		if failPct >= 25 {
			sev = SeverityCritical
		}
		out = append(out, persist.Suggestion{
			Severity: sev,
			Title:    fmt.Sprintf("Elevated upload failure rate — %.1f%%.", failPct),
			Detail: fmt.Sprintf(
				"%d of %d uploads failed. Common causes: server-side max-startups exceeded, auth throttling, or a transient outage on the SFTP target.",
				m.FailedFiles, total,
			),
			Action: "Open the CSV and group by error_code. If most are DIAL/AUTH, lower parallel_streams; if WRITE/CLOSE, suspect network instability or server-side limits.",
		})
	}

	// --- Local network throughput vs ask ----------------------------------
	// We can compare the achieved Mbps to a coarse expected Mbps derived
	// from fpm × avg_file_size. If achieved is well below expected and the
	// run did NOT skip, the network (or server intake) — not concurrency —
	// is the limit.
	if total > 0 && skips == 0 && m.OverallMBps > 0 && m.PeakWindowMBps > 0 && m.FilesPerMinute > 0 {
		avgFileMB := float64(m.TotalBytes) / float64(total) / (1024.0 * 1024.0)
		expectedMBps := avgFileMB * float64(m.FilesPerMinute) / 60.0
		if expectedMBps > 1 && m.PeakWindowMBps < expectedMBps*0.7 {
			out = append(out, persist.Suggestion{
				Severity: SeverityWarn,
				Title:    fmt.Sprintf("Network throughput limited — peak %.1f MB/s, expected ~%.1f MB/s.", m.PeakWindowMBps, expectedMBps),
				Detail: fmt.Sprintf(
					"At %d files/min × %.1f MiB average, the run needed about %.1f MB/s sustained. Peak observed was %.1f MB/s.",
					m.FilesPerMinute, avgFileMB, expectedMBps, m.PeakWindowMBps,
				),
				Action: "Check the upstream bandwidth from this host and the inbound bandwidth at the SFTP target. Consider running the test from a host closer to the server or with more network capacity.",
			})
		}
	}

	// --- Downloads never arrived -----------------------------------------
	if m.DownloadStalled > 0 && total > 0 {
		stallPct := float64(m.DownloadStalled) / float64(total) * 100.0
		sev := SeverityWarn
		if stallPct >= 25 {
			sev = SeverityCritical
		}
		out = append(out, persist.Suggestion{
			Severity: sev,
			Title:    fmt.Sprintf("Downloads stalled — %.1f%% of uploads never matched a download.", stallPct),
			Detail: fmt.Sprintf(
				"%d of %d upload rows show download_error=DOWNLOAD_TIMEOUT_LOCAL. The download workers either could not keep up with the upload rate, or the server never routed the file to any download user's outbox.",
				m.DownloadStalled, total,
			),
			Action: fmt.Sprintf(
				"Raise download.parallel_streams (currently %d), add more download users (currently %d), or verify server-side routing places the file in an outbox the test reads from.",
				m.DownloadParallelStreams, m.DownloadUsers,
			),
		})
	}

	// --- File-descriptor pressure -----------------------------------------
	if m.PeakFDInUse > 0 && m.PeakFDInUse >= 800 {
		sev := SeverityWarn
		if m.PeakFDInUse >= 4000 {
			sev = SeverityCritical
		}
		out = append(out, persist.Suggestion{
			Severity: sev,
			Title:    fmt.Sprintf("High file-descriptor usage — peak %d.", m.PeakFDInUse),
			Detail:   "Each parallel SSH stream consumes file descriptors. macOS defaults to 256, Linux often 1024. Hitting the soft limit will cause new SSH dials to fail with 'too many open files'.",
			Action:   "Before scaling further, raise the soft limit: `ulimit -n 8192` on the shell that launches sftp-loadtest.",
		})
	}

	// --- Headroom positive note -------------------------------------------
	// If the run hit its target cleanly AND the host barely worked, tell
	// the operator they have room to push harder on the next run.
	if total > 0 && skips == 0 && m.FailedFiles == 0 && m.PeakCPUPercent > 0 && m.PeakCPUPercent < 40 {
		out = append(out, persist.Suggestion{
			Severity: SeverityInfo,
			Title:    fmt.Sprintf("Host has significant headroom — peak CPU %.0f%%, peak FD %d.", m.PeakCPUPercent, m.PeakFDInUse),
			Detail: fmt.Sprintf(
				"This host can almost certainly sustain a heavier workload than the %d files/min × %d streams just exercised.",
				m.FilesPerMinute, m.ParallelStreams,
			),
			Action: fmt.Sprintf(
				"Try doubling files_per_minute to %d, or raising parallel_streams to %d, on the next run to find your real ceiling.",
				m.FilesPerMinute*2, m.ParallelStreams*2,
			),
		})
	}

	return out
}

// suggestParallel proposes a new parallel_streams that should absorb the
// observed skip rate. Rounded up; capped to a sensible maximum so we don't
// recommend something the local host can't sustain.
func suggestParallel(m persist.RunMeta) int {
	cur := m.ParallelStreams
	if cur < 1 {
		cur = 1
	}
	skipRatio := 0.0
	attempted := m.TotalFiles + m.DispatchSkips
	if attempted > 0 {
		skipRatio = float64(m.DispatchSkips) / float64(attempted)
	}
	// Need enough extra slots to clear the skipped fraction with a small
	// safety margin (× 1.2). Round up.
	scaled := float64(cur) * (1.0 + skipRatio*1.2)
	next := int(math.Ceil(scaled))
	if next <= cur {
		next = cur + 1
	}
	if next > 16 {
		next = 16
	}
	return next
}

// suggestFpmFromCPU back-calculates a sustainable files-per-minute from
// observed peak CPU. Assumes CPU usage scales roughly linearly with fpm
// (it doesn't, perfectly — but it's a reasonable first cut).
func suggestFpmFromCPU(m persist.RunMeta) int {
	if m.FilesPerMinute <= 0 || m.PeakCPUPercent <= 0 {
		return 0
	}
	target := 70.0 // aim for 70% so the host has headroom
	scale := target / m.PeakCPUPercent
	out := int(float64(m.FilesPerMinute) * scale)
	if out < 1 {
		out = 1
	}
	return out
}
