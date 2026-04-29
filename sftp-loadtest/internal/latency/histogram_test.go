package latency

import (
	"math/rand"
	"testing"
	"time"
)

func TestHistogram_Empty(t *testing.T) {
	var h Histogram
	s := h.Snapshot()
	if s.Count != 0 || s.P50 != 0 || s.Max != 0 {
		t.Errorf("empty snapshot must be zero, got %+v", s)
	}
}

func TestHistogram_Single(t *testing.T) {
	var h Histogram
	h.Add(5 * time.Millisecond)
	s := h.Snapshot()
	if s.Count != 1 {
		t.Errorf("count=%d", s.Count)
	}
	if s.Max != int64(5*time.Millisecond) {
		t.Errorf("max=%d", s.Max)
	}
	// P50/P95/P99 should all land in the bucket containing 5 ms; allow ±5%.
	for _, p := range []int64{s.P50, s.P95, s.P99, s.P999} {
		if p < int64(4750*time.Microsecond) || p > int64(5250*time.Microsecond) {
			t.Errorf("percentile %d outside ±5%% of 5 ms", p)
		}
	}
}

// TestHistogram_PercentileAccuracy_Uniform records a wide spread of
// values and checks that estimated percentiles are within the bucket
// resolution we promise (≤ 3% error for the in-range buckets).
func TestHistogram_PercentileAccuracy_Uniform(t *testing.T) {
	var h Histogram
	rng := rand.New(rand.NewSource(42))
	const n = 10_000
	for i := 0; i < n; i++ {
		// Uniform 1ms..100ms.
		d := time.Duration(rng.Intn(99_000)+1_000) * time.Microsecond
		h.Add(d)
	}
	s := h.Snapshot()
	// True P50 ≈ 50.5 ms, P95 ≈ 95.05 ms, P99 ≈ 99.01 ms.
	expect := []struct {
		name   string
		got    int64
		want   time.Duration
		tolPct float64
	}{
		{"p50", s.P50, 50_500 * time.Microsecond, 5},
		{"p95", s.P95, 95_050 * time.Microsecond, 5},
		{"p99", s.P99, 99_010 * time.Microsecond, 5},
	}
	for _, e := range expect {
		gotD := time.Duration(e.got)
		errPct := float64(gotD-e.want) / float64(e.want) * 100
		if errPct < 0 {
			errPct = -errPct
		}
		if errPct > e.tolPct {
			t.Errorf("%s: got %s, want ~%s (err %.2f%% > tol %.0f%%)", e.name, gotD, e.want, errPct, e.tolPct)
		}
	}
}

func TestHistogram_OverflowBucketHandlesHugeValues(t *testing.T) {
	var h Histogram
	h.Add(60 * time.Second) // way above the top decade (10s)
	s := h.Snapshot()
	if s.Count != 1 {
		t.Fatalf("count=%d", s.Count)
	}
	if s.Max != int64(60*time.Second) {
		t.Errorf("max should track the raw value, got %d", s.Max)
	}
}

func TestHistogram_Concurrent_AddsRaceFree(t *testing.T) {
	// Smoke for -race: 8 goroutines hammer Add simultaneously.
	var h Histogram
	const per = 5_000
	const workers = 8
	done := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < per; i++ {
				h.Add(time.Duration(i) * time.Microsecond)
			}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	if got := h.Count(); got != workers*per {
		t.Errorf("count=%d, want %d", got, workers*per)
	}
}
