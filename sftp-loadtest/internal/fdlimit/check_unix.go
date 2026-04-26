//go:build !windows

// Package fdlimit raises (or warns about) the process file-descriptor soft
// limit so high-concurrency runs don't mysteriously fail with "too many open
// files". Called once at startup.
package fdlimit

import (
	"log"
	"syscall"
)

// minRecommended is the soft RLIMIT_NOFILE value we want before running. At
// 10 upload users × 4 streams + 10 download users + watcher + HTTP + misc
// we're around 50–60 FDs per run; default macOS soft limit is 256 so a
// single normal run is fine, but 100k-fpm configs with larger user counts
// start to fight the ceiling.
const minRecommended = 4096

// Check reads the current RLIMIT_NOFILE, attempts to raise Cur toward the
// hard max (capped at minRecommended), and logs either the raise or, if
// that fails, a clear warning with the remediation command.
func Check() {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return
	}
	if rl.Cur >= minRecommended {
		return
	}
	target := rl.Max
	if target > minRecommended {
		target = minRecommended
	}
	if target > rl.Cur {
		next := rl
		next.Cur = target
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &next); err == nil {
			log.Printf("file-descriptor soft limit raised from %d to %d (hard=%d, target=%d)",
				rl.Cur, target, rl.Max, minRecommended)
			return
		}
	}
	log.Printf("WARNING: file-descriptor soft limit is %d (target: %d). High-concurrency load tests may hit 'too many open files'. Raise with: ulimit -n %d",
		rl.Cur, minRecommended, minRecommended)
}
