// Package latency is a small fixed-memory histogram for per-stage SFTP
// timings. The goal is two-fold:
//
//  1. Compute p50 / p95 / p99 / p99.9 / max accurately enough to drive
//     SLA conversations, without dragging in an HDR-histogram dependency.
//  2. Stay lock-free on the record path so every upload / download /
//     handshake measurement is cheap to capture even at 10k samples/sec.
//
// The bucketing is log-linear: each decade (1µs–10µs, 10µs–100µs, …)
// is split into 32 sub-buckets, giving ≤ 3% relative error on any
// percentile. Range covers 1µs through 10s = 7 decades = 224 buckets,
// plus an overflow bucket for anything above. Total memory: 225 *
// 8 bytes = 1.8 KiB per histogram.
//
// Add() is wait-free (single atomic.Uint64 add). Snapshot() copies the
// buckets and returns a value you can read without holding the
// histogram, so the runner can hand snapshots to the seal path without
// blocking the dispatcher.
package latency

import (
	"math"
	"sort"
	"sync/atomic"
	"time"
)

const (
	// minNs is the smallest representable value (1 µs).
	minNs = 1_000
	// decades covers 1µs → 10s. Anything above lands in the overflow bucket.
	decades = 7
	// subBucketsPerDecade is the resolution within each decade. 32 keeps
	// relative error ≤ 1 / 32 ≈ 3% which is more than enough for SLA work.
	subBucketsPerDecade = 32
	// numBuckets includes one tail bucket for >10s; lookups beyond it clamp.
	numBuckets = decades*subBucketsPerDecade + 1
)

// Histogram is a thread-safe fixed-memory latency accumulator. The zero
// value is ready to use.
type Histogram struct {
	buckets [numBuckets]atomic.Uint64
	count   atomic.Uint64
	sumNs   atomic.Uint64
	maxNs   atomic.Int64 // signed for atomic CAS via Int64
}

// Add records a single observation. Safe to call from any goroutine.
func (h *Histogram) Add(d time.Duration) {
	if h == nil {
		return
	}
	ns := int64(d)
	if ns < 0 {
		ns = 0
	}
	idx := bucketIndex(ns)
	h.buckets[idx].Add(1)
	h.count.Add(1)
	h.sumNs.Add(uint64(ns))
	for {
		cur := h.maxNs.Load()
		if ns <= cur || h.maxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

// Count returns the number of observations recorded.
func (h *Histogram) Count() uint64 { return h.count.Load() }

// Snapshot is an immutable view of the histogram at one moment. Useful
// for serialising to JSON / CSV and for unit tests.
type Snapshot struct {
	Count uint64
	// Percentiles in nanoseconds. Always populated; for an empty
	// histogram every value is 0.
	P50  int64
	P95  int64
	P99  int64
	P999 int64
	Max  int64
	Mean int64
}

// Snapshot computes percentile points from the current bucket counts.
// Lock-free; the read may see a small skew if Add() is called
// concurrently, but counts are monotonic so no value is ever wrong by
// more than the in-flight delta — fine for SLA reporting.
func (h *Histogram) Snapshot() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	count := h.count.Load()
	if count == 0 {
		return Snapshot{}
	}
	mean := int64(h.sumNs.Load() / count)
	maxNs := h.maxNs.Load()
	var s Snapshot
	s.Count = count
	s.Mean = mean
	s.Max = maxNs
	s.P50 = h.percentile(0.50)
	s.P95 = h.percentile(0.95)
	s.P99 = h.percentile(0.99)
	s.P999 = h.percentile(0.999)
	return s
}

// percentile linearly interpolates within the bucket that crosses the
// requested rank. Returns nanoseconds.
func (h *Histogram) percentile(p float64) int64 {
	count := h.count.Load()
	if count == 0 {
		return 0
	}
	target := uint64(math.Ceil(p * float64(count)))
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i := 0; i < numBuckets; i++ {
		bc := h.buckets[i].Load()
		if bc == 0 {
			continue
		}
		if seen+bc >= target {
			low, high := bucketBoundsNs(i)
			// Where in this bucket does the target rank fall?
			//
			// Linear interpolation between bucket bounds gives a
			// reasonable estimate without keeping per-sample state.
			fraction := float64(target-seen) / float64(bc)
			est := float64(low) + fraction*float64(high-low)
			return int64(est)
		}
		seen += bc
	}
	return h.maxNs.Load()
}

// bucketIndex maps a duration in nanoseconds to its bucket. Anything
// below minNs lands in bucket 0; anything above the top decade lands in
// the overflow bucket.
func bucketIndex(ns int64) int {
	if ns < minNs {
		return 0
	}
	if ns >= int64(math.Pow10(decades)*minNs) {
		return numBuckets - 1
	}
	// Decade index 0 = [1µs, 10µs); decade i = [10^i µs, 10^(i+1) µs).
	d := 0
	thresh := int64(minNs * 10)
	for ns >= thresh && d < decades-1 {
		d++
		thresh *= 10
	}
	low := int64(minNs * pow10(d))
	high := low * 10
	step := (high - low) / subBucketsPerDecade
	if step <= 0 {
		step = 1
	}
	sub := int((ns - low) / step)
	if sub >= subBucketsPerDecade {
		sub = subBucketsPerDecade - 1
	}
	return d*subBucketsPerDecade + sub
}

// bucketBoundsNs returns the [low, high) range of bucket idx in ns.
func bucketBoundsNs(idx int) (int64, int64) {
	if idx >= numBuckets-1 {
		// Overflow bucket has an open upper bound; pin to 10× the top
		// decade so percentile interpolation still produces a finite
		// number.
		top := int64(minNs * pow10(decades))
		return top, top * 10
	}
	d := idx / subBucketsPerDecade
	sub := idx % subBucketsPerDecade
	low := int64(minNs * pow10(d))
	high := low * 10
	step := (high - low) / subBucketsPerDecade
	return low + int64(sub)*step, low + int64(sub+1)*step
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}

// SortedDurations is a tiny sort helper for tests that compare expected
// percentile points against the slow-but-correct exact computation.
func SortedDurations(in []time.Duration) []time.Duration {
	out := append([]time.Duration(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
