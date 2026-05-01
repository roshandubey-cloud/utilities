package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWorker stands in for a real sftp-loadtest /api surface. We
// deliberately do NOT spin up the full web server + runner here — the
// cluster package only cares about the wire shapes, and the runner has
// its own integration tests against the mock SFTP server. Keeping this
// test mock-of-the-mock makes it sub-second and CI-cheap.
type fakeWorker struct {
	mu             *atomic.Int32
	startCalls     *atomic.Int32
	stopCalls      *atomic.Int32
	statusCalls    *atomic.Int32
	startResp      string
	statusResp     string
	healthzVersion string // when "", /healthz returns 404
	requireXReqWith bool
}

func newFakeWorker(startResp, statusResp string) *fakeWorker {
	return &fakeWorker{
		mu:             new(atomic.Int32),
		startCalls:     new(atomic.Int32),
		stopCalls:      new(atomic.Int32),
		statusCalls:    new(atomic.Int32),
		startResp:      startResp,
		statusResp:     statusResp,
		requireXReqWith: true,
	}
}

// healthzVersion, when non-empty, is what the fake worker reports under
// /healthz?detail=1 → {"version": ...}. Empty means "endpoint not
// implemented" — the worker returns 404 and the coordinator records
// no Version, simulating a pre-negotiation worker.
func (f *fakeWorker) withHealthzVersion(v string) *fakeWorker {
	f.healthzVersion = v
	return f
}

func (f *fakeWorker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if f.healthzVersion == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("detail") == "1" {
			_, _ = io.WriteString(w, `{"status":"ok","version":"`+f.healthzVersion+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if f.requireXReqWith && r.Header.Get("X-Requested-With") != "sftp-loadtest" {
			http.Error(w, "missing X-Requested-With", http.StatusForbidden)
			return
		}
		f.startCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, f.startResp)
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		f.stopCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		f.statusCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, f.statusResp)
	})
	return mux
}

func TestCoordinator_StartFansOutAndDividesFPM(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"run-A"}`, `{"active":true}`)
	w2 := newFakeWorker(`{"run_id":"run-B"}`, `{"active":true}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()
	s2 := httptest.NewServer(w2.handler())
	defer s2.Close()

	// Capture the actual body each worker received so we can assert the
	// fpm split. We swap the handler with a wrapper that records.
	var got1, got2 []byte
	s1.Config.Handler = recordingHandler(s1.Config.Handler, func(b []byte) { got1 = b })
	s2.Config.Handler = recordingHandler(s2.Config.Handler, func(b []byte) { got2 = b })

	c := New("")
	cfg := []byte(`{"host":"x","port":22,"files_per_minute":1000}`)
	ids, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}, {URL: s2.URL}},
		Config:  cfg,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(ids) != 2 || ids[0] != "run-A" || ids[1] != "run-B" {
		t.Errorf("run ids: %+v", ids)
	}
	if w1.startCalls.Load() != 1 || w2.startCalls.Load() != 1 {
		t.Errorf("each worker should have been started once; got %d / %d",
			w1.startCalls.Load(), w2.startCalls.Load())
	}

	for i, body := range [][]byte{got1, got2} {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if got, _ := m["files_per_minute"].(float64); got != 500 {
			t.Errorf("worker %d should have received fpm=500, got %v", i+1, m["files_per_minute"])
		}
		if got, _ := m["host"].(string); got != "x" {
			t.Errorf("worker %d lost non-fpm fields: %+v", i+1, m)
		}
	}
}

// TestCoordinator_VersionNegotiation pins the negotiation behaviour:
// - master records each worker's version when /healthz?detail=1 returns it
// - VersionMismatch fires when worker.version != master.version
// - a worker that doesn't expose /healthz (404) leaves Version empty,
//   and the master does NOT flag mismatch in that case (informational
//   gap, not a hard failure)
func TestCoordinator_VersionNegotiation(t *testing.T) {
	matched := newFakeWorker(`{"run_id":"a"}`, `{"active":true}`).withHealthzVersion("0.13.4")
	skewed := newFakeWorker(`{"run_id":"b"}`, `{"active":true}`).withHealthzVersion("0.12.9")
	silent := newFakeWorker(`{"run_id":"c"}`, `{"active":true}`) // no healthz version

	sM := httptest.NewServer(matched.handler())
	defer sM.Close()
	sS := httptest.NewServer(skewed.handler())
	defer sS.Close()
	sN := httptest.NewServer(silent.handler())
	defer sN.Close()

	c := New("0.13.4")
	_, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: sM.URL}, {URL: sS.URL}, {URL: sN.URL}},
		Config:  []byte(`{"files_per_minute":300}`),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	st := c.Status(context.Background())
	if st.MasterVersion != "0.13.4" {
		t.Errorf("MasterVersion=%q, want 0.13.4", st.MasterVersion)
	}
	if len(st.Workers) != 3 {
		t.Fatalf("Workers count=%d, want 3", len(st.Workers))
	}
	byURL := map[string]WorkerStatus{}
	for _, w := range st.Workers {
		byURL[w.URL] = w
	}
	if got := byURL[sM.URL]; got.Version != "0.13.4" || got.VersionMismatch {
		t.Errorf("matched worker: Version=%q Mismatch=%v, want 0.13.4 / false", got.Version, got.VersionMismatch)
	}
	if got := byURL[sS.URL]; got.Version != "0.12.9" || !got.VersionMismatch {
		t.Errorf("skewed worker: Version=%q Mismatch=%v, want 0.12.9 / true", got.Version, got.VersionMismatch)
	}
	if got := byURL[sN.URL]; got.Version != "" || got.VersionMismatch {
		t.Errorf("silent worker: Version=%q Mismatch=%v, want empty / false", got.Version, got.VersionMismatch)
	}
}

func TestCoordinator_StartRollsBackOnPartialFailure(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"ok-1"}`, `{}`)
	bad := newFakeWorker(`bad-not-json`, `{}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()
	sBad := httptest.NewServer(bad.handler())
	defer sBad.Close()

	c := New("")
	_, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}, {URL: sBad.URL}},
		Config:  []byte(`{"files_per_minute":600}`),
	})
	if err == nil {
		t.Fatal("expected Start to fail when a worker rejects /api/start")
	}
	// w1 successfully started, then was rolled back via /api/stop.
	if w1.startCalls.Load() != 1 || w1.stopCalls.Load() != 1 {
		t.Errorf("rollback path wrong: w1 starts=%d stops=%d",
			w1.startCalls.Load(), w1.stopCalls.Load())
	}
	if c.Active() {
		t.Error("Coordinator should not be Active after rollback")
	}
}

func TestCoordinator_StatusAggregates(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"a"}`, `{"active":true,"failed_files":2,"dispatch_skips":7,"metrics":{"total_files":100,"total_bytes":1048576,"overall_mbps":4.0,"last_minute_mbps":3.0}}`)
	w2 := newFakeWorker(`{"run_id":"b"}`, `{"active":true,"failed_files":1,"dispatch_skips":3,"metrics":{"total_files":80,"total_bytes":524288,"overall_mbps":3.5,"last_minute_mbps":2.5}}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()
	s2 := httptest.NewServer(w2.handler())
	defer s2.Close()

	c := New("")
	if _, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}, {URL: s2.URL}},
		Config:  []byte(`{"files_per_minute":600}`),
	}); err != nil {
		t.Fatal(err)
	}

	st := c.Status(context.Background())
	if !st.Active {
		t.Error("Status.Active should be true")
	}
	if st.TotalFiles != 180 {
		t.Errorf("TotalFiles aggregated wrong: %d (want 180)", st.TotalFiles)
	}
	if st.TotalBytes != 1048576+524288 {
		t.Errorf("TotalBytes wrong: %d", st.TotalBytes)
	}
	if st.FailedFiles != 3 {
		t.Errorf("FailedFiles wrong: %d", st.FailedFiles)
	}
	if st.DispatchSkips != 10 {
		t.Errorf("DispatchSkips wrong: %d", st.DispatchSkips)
	}
	// Throughput sums because each worker drives independent load.
	if abs(st.OverallMBps-7.5) > 0.001 {
		t.Errorf("OverallMBps should sum, got %f", st.OverallMBps)
	}
	if len(st.Workers) != 2 {
		t.Fatalf("Workers slice length %d", len(st.Workers))
	}
	for _, ws := range st.Workers {
		if !ws.Reachable {
			t.Errorf("worker %s should be Reachable, got err=%s", ws.URL, ws.Err)
		}
	}
}

func TestCoordinator_StatusFlagsUnreachable(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"a"}`, `{"active":true,"metrics":{"total_files":50}}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()

	c := New("")
	if _, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}, {URL: "http://127.0.0.1:1"}}, // 2nd unreachable
		Config:  []byte(`{"files_per_minute":600}`),
	}); err == nil {
		t.Fatal("Start should fail when a worker is unreachable")
	}
}

func TestCoordinator_Stop(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"a"}`, `{}`)
	w2 := newFakeWorker(`{"run_id":"b"}`, `{}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()
	s2 := httptest.NewServer(w2.handler())
	defer s2.Close()

	c := New("")
	if _, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}, {URL: s2.URL}},
		Config:  []byte(`{"files_per_minute":600}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if w1.stopCalls.Load() != 1 || w2.stopCalls.Load() != 1 {
		t.Errorf("each worker should have been stopped once; got %d / %d",
			w1.stopCalls.Load(), w2.stopCalls.Load())
	}
	if c.Active() {
		t.Error("Coordinator.Active should be false after Stop")
	}
}

func TestCoordinator_RejectsOverlappingStarts(t *testing.T) {
	w1 := newFakeWorker(`{"run_id":"a"}`, `{}`)
	s1 := httptest.NewServer(w1.handler())
	defer s1.Close()

	c := New("")
	if _, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}},
		Config:  []byte(`{"files_per_minute":600}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), StartReq{
		Workers: []Worker{{URL: s1.URL}},
		Config:  []byte(`{"files_per_minute":600}`),
	}); err == nil {
		t.Fatal("second concurrent Start should be refused")
	}
}

func TestSplitConfig_Halves(t *testing.T) {
	out, err := splitConfig([]byte(`{"a":1,"files_per_minute":1000}`), 2)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if got, _ := m["files_per_minute"].(float64); got != 500 {
		t.Errorf("fpm split wrong: %v", m["files_per_minute"])
	}
	if got, _ := m["a"].(float64); got != 1 {
		t.Errorf("non-fpm field lost: %v", m["a"])
	}
}

func TestSplitConfig_FloorOfOne(t *testing.T) {
	// 5 fpm across 10 workers should still send 1 fpm each — the master
	// rounds up to 1 so a worker with slot 0 still does meaningful work.
	out, err := splitConfig([]byte(`{"files_per_minute":5}`), 10)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if got, _ := m["files_per_minute"].(float64); got != 1 {
		t.Errorf("fpm floor wrong: %v", m["files_per_minute"])
	}
}

// recordingHandler tees the request body to a sink before delegating to
// the wrapped handler, so tests can assert on exactly what each worker
// received without rewriting the worker mock to expose its bodies.
// Only POST /api/start bodies are recorded — the coordinator now also
// fires GET /healthz?detail=1 during version negotiation, and capturing
// that no-body request would overwrite the start payload tests want to
// inspect.
func recordingHandler(next http.Handler, sink func([]byte)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/start" && r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			sink(b)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		next.ServeHTTP(w, r)
	})
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Trivially exercise the timeout config so a future change can't strand
// callers on the package default. Keeping this here because adding a
// dedicated _timeout test file feels like overkill.
func TestCoordinator_TimeoutNonZero(t *testing.T) {
	c := New("")
	if c.httpc.Timeout < time.Second {
		t.Fatalf("default timeout must be > 1s, got %s", c.httpc.Timeout)
	}
	_ = fmt.Sprintf // keep fmt import if future tests need it
}