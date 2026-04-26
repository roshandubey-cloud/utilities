package proc

import (
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

type Stats struct {
	HeapMB     float64 `json:"heap_mb"`
	SysMB      float64 `json:"sys_mb"`
	CPUPercent float64 `json:"cpu_percent"`
	Goroutines int     `json:"goroutines"`
	NumCPU     int     `json:"num_cpu"`
	CPUSecs    float64 `json:"cpu_seconds_total"`
	// Live FD-in-use count (via /dev/fd on unix). -1 = unknown / windows.
	// FD-in-use is the single most useful "am I about to hit a wall"
	// signal during a high-concurrency run, so it lives in the cheap
	// per-poll sample alongside CPU and heap.
	FDInUse int64 `json:"fd_in_use"`
}

// Monitor reads process-level resource usage using only Go's standard library,
// so it works on Windows, Linux and macOS without extra deps.
type Monitor struct {
	mu      sync.Mutex
	lastCPU float64
	lastAt  time.Time
	numCPU  int
}

func New() *Monitor {
	return &Monitor{numCPU: runtime.NumCPU()}
}

// Sample returns a fresh reading. CPU percent is the delta since the prior call
// divided by wall time, normalized to total machine cores (so 100% means every
// core is fully busy, not just one).
func (m *Monitor) Sample() Stats {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	cpuSec := readCPUSeconds()

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var cpuPct float64
	if !m.lastAt.IsZero() {
		wall := now.Sub(m.lastAt).Seconds()
		delta := cpuSec - m.lastCPU
		if wall > 0 && delta > 0 {
			cpuPct = (delta / wall) * 100.0 / float64(m.numCPU)
		}
	}
	m.lastAt = now
	m.lastCPU = cpuSec

	return Stats{
		HeapMB:     float64(mem.HeapAlloc) / (1024 * 1024),
		SysMB:      float64(mem.Sys) / (1024 * 1024),
		CPUPercent: cpuPct,
		Goroutines: runtime.NumGoroutine(),
		NumCPU:     m.numCPU,
		CPUSecs:    cpuSec,
		FDInUse:    fdInUse(),
	}
}

// readCPUSeconds returns cumulative CPU time (user + system) attributed to this
// Go process, using the runtime/metrics "/cpu/classes/total:cpu-seconds" series
// available since Go 1.20.
func readCPUSeconds() float64 {
	samples := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindFloat64 {
		return samples[0].Value.Float64()
	}
	return 0
}
