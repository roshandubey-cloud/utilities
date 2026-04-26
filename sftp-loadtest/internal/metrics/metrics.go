package metrics

import (
	"sort"
	"sync"
	"time"
)

const (
	bucketSize = 1 * time.Minute

	// maxSlowdowns caps the slowdowns slice. A week-long run at one
	// slowdown/minute would otherwise accumulate ~10K entries copied on every
	// 2s status poll.
	maxSlowdowns = 1000

	// maxBuckets keeps the per-minute bucket map bounded (24h window).
	// The slowdown detector only needs the previous bucket, so pruning older
	// ones is safe. Snapshot() still returns whatever remains.
	maxBuckets = 1440
)

type Engine struct {
	mu           sync.Mutex
	startAt      time.Time
	totalFiles   int64
	totalBytes   int64
	totalUploadD time.Duration
	buckets      map[int64]*bucket // key = unix-minute
	slowdowns    []Slowdown
	lastBaseline float64 // MB/s, exponential moving baseline
	slowdownPct  float64 // e.g. 0.30 for 30% drop
}

type bucket struct {
	files int64
	bytes int64
	dur   time.Duration
}

type Slowdown struct {
	At              time.Time `json:"at"`
	FilesSoFar      int64     `json:"files_so_far"`
	BytesSoFar      int64     `json:"bytes_so_far"`
	WindowMBps      float64   `json:"window_mbps"`
	BaselineMBps    float64   `json:"baseline_mbps"`
	DropPct         float64   `json:"drop_pct"`
}

type Snapshot struct {
	StartAt       time.Time   `json:"start_at"`
	Elapsed       string      `json:"elapsed"`
	TotalFiles    int64       `json:"total_files"`
	TotalBytes    int64       `json:"total_bytes"`
	OverallMBps   float64     `json:"overall_mbps"`
	WindowMBps    float64     `json:"last_minute_mbps"`
	BaselineMBps  float64     `json:"baseline_mbps"`
	Slowdowns     []Slowdown  `json:"slowdowns"`
	PerMinute     []BucketRow `json:"per_minute"`
}

type BucketRow struct {
	Minute int64   `json:"minute"`
	Files  int64   `json:"files"`
	Bytes  int64   `json:"bytes"`
	MBps   float64 `json:"mbps"`
}

func New(slowdownPct float64) *Engine {
	if slowdownPct <= 0 {
		slowdownPct = 0.30
	}
	return &Engine{
		startAt:     time.Now(),
		buckets:     map[int64]*bucket{},
		slowdownPct: slowdownPct,
	}
}

func (e *Engine) Record(at time.Time, bytes int64, dur time.Duration) {
	key := at.Unix() / 60
	e.mu.Lock()
	defer e.mu.Unlock()

	e.totalFiles++
	e.totalBytes += bytes
	e.totalUploadD += dur

	b := e.buckets[key]
	if b == nil {
		b = &bucket{}
		e.buckets[key] = b
		// When a new bucket opens, evaluate the previous completed bucket for slowdown.
		e.evalLastClosedBucketLocked(key)
		// Prune buckets older than the sliding window so map size stays flat.
		if len(e.buckets) > maxBuckets {
			cutoff := key - int64(maxBuckets)
			for k := range e.buckets {
				if k < cutoff {
					delete(e.buckets, k)
				}
			}
		}
	}
	b.files++
	b.bytes += bytes
	b.dur += dur
}

func (e *Engine) evalLastClosedBucketLocked(currentKey int64) {
	prevKey := currentKey - 1
	pb, ok := e.buckets[prevKey]
	if !ok {
		return
	}
	mbps := bytesToMBps(pb.bytes, bucketSize)
	if e.lastBaseline == 0 {
		e.lastBaseline = mbps
		return
	}
	if mbps < e.lastBaseline*(1-e.slowdownPct) {
		e.slowdowns = append(e.slowdowns, Slowdown{
			At:           time.Unix(prevKey*60, 0),
			FilesSoFar:   e.totalFiles,
			BytesSoFar:   e.totalBytes,
			WindowMBps:   mbps,
			BaselineMBps: e.lastBaseline,
			DropPct:      (e.lastBaseline - mbps) / e.lastBaseline,
		})
		// Ring-buffer: drop oldest once we exceed the cap so a long run
		// doesn't grow the slice without bound.
		if len(e.slowdowns) > maxSlowdowns {
			drop := len(e.slowdowns) - maxSlowdowns
			e.slowdowns = append(e.slowdowns[:0], e.slowdowns[drop:]...)
		}
	}
	// EMA update (alpha=0.3)
	e.lastBaseline = 0.7*e.lastBaseline + 0.3*mbps
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	elapsed := time.Since(e.startAt)
	overall := bytesToMBps(e.totalBytes, elapsed)

	keys := make([]int64, 0, len(e.buckets))
	for k := range e.buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	rows := make([]BucketRow, 0, len(keys))
	for _, k := range keys {
		b := e.buckets[k]
		rows = append(rows, BucketRow{
			Minute: k,
			Files:  b.files,
			Bytes:  b.bytes,
			MBps:   bytesToMBps(b.bytes, bucketSize),
		})
	}
	var lastWindow float64
	if len(rows) > 0 {
		lastWindow = rows[len(rows)-1].MBps
	}
	return Snapshot{
		StartAt:      e.startAt,
		Elapsed:      elapsed.Round(time.Second).String(),
		TotalFiles:   e.totalFiles,
		TotalBytes:   e.totalBytes,
		OverallMBps:  overall,
		WindowMBps:   lastWindow,
		BaselineMBps: e.lastBaseline,
		Slowdowns:    append([]Slowdown(nil), e.slowdowns...),
		PerMinute:    rows,
	}
}

func bytesToMBps(b int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(b) / (1024.0 * 1024.0) / d.Seconds()
}
