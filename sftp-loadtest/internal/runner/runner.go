package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	mathrand "math/rand"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/generator"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/metrics"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/report"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/trackid"
)

// disablePolicy tracks per-user consecutive failures for one run and flags
// users past the threshold so dispatchers stop picking them.
type userStatus struct {
	user           string
	kind           string // "upload" or "download"
	consecutive    atomic.Int64
	totalFailed    atomic.Int64
	disabled       atomic.Bool
	disabledAtNano atomic.Int64           // 0 until disabled
	lastCode       atomic.Pointer[string]
	lastAtNano     atomic.Int64
	// lastFile captures the most recent basename associated with a failure
	// (empty when not applicable, e.g. a DIAL failure has no file). Surfaced
	// in DisabledSnapshot so the UI's disabled-users chip can show *which*
	// file the operator should investigate, not just the failure code.
	lastFile atomic.Pointer[string]
}

type disablePolicy struct {
	threshold int // 0 = never auto-disable
	up        map[string]*userStatus
	dl        map[string]*userStatus
}

func newDisablePolicy(threshold int, upload, large, download []config.UserCreds) *disablePolicy {
	if threshold < 0 {
		threshold = 0
	}
	p := &disablePolicy{threshold: threshold, up: map[string]*userStatus{}, dl: map[string]*userStatus{}}
	seen := map[string]bool{}
	for _, list := range [][]config.UserCreds{upload, large} {
		for _, u := range list {
			if seen[u.Username] {
				continue
			}
			seen[u.Username] = true
			p.up[u.Username] = &userStatus{user: u.Username, kind: "upload"}
		}
	}
	for _, u := range download {
		p.dl[u.Username] = &userStatus{user: u.Username, kind: "download"}
	}
	return p
}

func (p *disablePolicy) status(user, kind string) *userStatus {
	if p == nil {
		return nil
	}
	if kind == "download" {
		return p.dl[user]
	}
	return p.up[user]
}

func (p *disablePolicy) onSuccess(user, kind string) {
	if p == nil {
		return
	}
	s := p.status(user, kind)
	if s == nil {
		return
	}
	s.consecutive.Store(0)
}

// onFailure bumps the counter; returns true iff this call is the one that
// crossed the threshold (so callers can log once).
func (p *disablePolicy) onFailure(user, kind, code string) bool {
	return p.onFailureFor(user, kind, code, "")
}

// onFailureFor is like onFailure but also records the basename of the file
// involved in the failure (empty string when the failure has no file context,
// e.g. a DIAL error). Surfaces in the DisabledSnapshot.LastFile so the
// disabled-users chip can show which file the operator should investigate.
func (p *disablePolicy) onFailureFor(user, kind, code, basename string) bool {
	if p == nil {
		return false
	}
	s := p.status(user, kind)
	if s == nil {
		return false
	}
	s.totalFailed.Add(1)
	s.lastAtNano.Store(time.Now().UnixNano())
	codeCopy := code
	s.lastCode.Store(&codeCopy)
	if basename != "" {
		fileCopy := basename
		s.lastFile.Store(&fileCopy)
	}
	n := s.consecutive.Add(1)
	if p.threshold > 0 && n >= int64(p.threshold) {
		if s.disabled.CompareAndSwap(false, true) {
			s.disabledAtNano.Store(time.Now().UnixNano())
			return true
		}
	}
	return false
}

func (p *disablePolicy) isDisabled(user, kind string) bool {
	if p == nil {
		return false
	}
	s := p.status(user, kind)
	return s != nil && s.disabled.Load()
}

type DisabledSnapshot struct {
	User        string    `json:"user"`
	Kind        string    `json:"kind"` // "upload" or "download"
	At          time.Time `json:"at"`
	Consecutive int64     `json:"consecutive"`
	TotalFailed int64     `json:"total_failed"`
	LastCode    string    `json:"last_code"`
	LastFile    string    `json:"last_file"` // basename of the most recent file involved in a failure (may be empty for DIAL/AUTH errors)
	LastAt      time.Time `json:"last_at"`
}

func (p *disablePolicy) snapshot() []DisabledSnapshot {
	if p == nil {
		return nil
	}
	var out []DisabledSnapshot
	collect := func(m map[string]*userStatus) {
		for _, s := range m {
			if !s.disabled.Load() {
				continue
			}
			lc := ""
			if sp := s.lastCode.Load(); sp != nil {
				lc = *sp
			}
			lf := ""
			if fp := s.lastFile.Load(); fp != nil {
				lf = *fp
			}
			out = append(out, DisabledSnapshot{
				User:        s.user,
				Kind:        s.kind,
				At:          time.Unix(0, s.disabledAtNano.Load()),
				Consecutive: s.consecutive.Load(),
				TotalFailed: s.totalFailed.Load(),
				LastCode:    lc,
				LastFile:    lf,
				LastAt:      time.Unix(0, s.lastAtNano.Load()),
			})
		}
	}
	collect(p.up)
	collect(p.dl)
	return out
}

func (p *disablePolicy) allUploadDisabled() bool {
	if p == nil || len(p.up) == 0 {
		return false
	}
	for _, s := range p.up {
		if !s.disabled.Load() {
			return false
		}
	}
	return true
}

// errCounters tracks failure counts by stable ErrorCode. Reads happen only on
// status polls (low frequency) so a plain mutex is cheaper than a sync.Map.
type errCounters struct {
	mu sync.Mutex
	by map[string]int64
}

func (e *errCounters) inc(code string) {
	if code == "" {
		return
	}
	e.mu.Lock()
	if e.by == nil {
		e.by = map[string]int64{}
	}
	e.by[code]++
	e.mu.Unlock()
}

func (e *errCounters) snapshot() map[string]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int64, len(e.by))
	for k, v := range e.by {
		out[k] = v
	}
	return out
}

// sealAllAndWriteMeta drains every still-live record into the stream CSV,
// closes the stream, and writes the run metadata JSON. Called on teardown.
// With streaming enabled during the run, the CSV file is already mostly
// populated — this just finalizes the tail.
func sealAllAndWriteMeta(r *Run, reportsDir string) error {
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		return fmt.Errorf("mkdir reports dir: %w", err)
	}
	snap := r.Metrics.Snapshot()
	eff := func(bytes int64, startTime time.Time, dur time.Duration) float64 {
		return EffectiveSpeedMBps(bytes, startTime, dur, snap)
	}
	slowMins := r.SlowdownMinutes()
	// Seal everything left — the "is final" predicate returns true for all.
	if _, err := r.Report.FlushFinalized(func(*report.FileRecord) bool { return true }, slowMins, eff); err != nil {
		log.Printf("final flush: %v", err)
	}
	if r.reportStream != nil {
		// If no rows were ever flushed (zero uploads), the file has no header.
		// Leave it as an empty file — the meta JSON still records the run
		// existed; downstream tools treat a zero-row CSV as "no activity".
		_ = r.reportStream.Close()
	}
	// Failure tally: every code in r.errCounts represents a failed file;
	// sum and subtract from total to get successes.
	var failed int64
	for _, n := range r.errCounts.snapshot() {
		failed += n
	}
	if failed > snap.TotalFiles {
		failed = snap.TotalFiles
	}
	meta := persist.RunMeta{
		ID:             r.ID,
		StartedAt:      r.StartedAt,
		StoppedAt:      time.Now(),
		TotalFiles:     snap.TotalFiles,
		TotalBytes:     snap.TotalBytes,
		OverallMBps:    snap.OverallMBps,
		FailedFiles:    failed,
		SucceededFiles: snap.TotalFiles - failed,
	}
	// Capture workload-shape from the live config so the Previous-runs
	// overview tells the user what was attempted, not just what finished.
	if r.Cfg != nil {
		meta.UploadUsers = len(r.Cfg.NormalUsers)
		meta.ParallelStreams = r.Cfg.ParallelStreams
		if r.Cfg.Normal != nil {
			meta.FilesPerMinute = r.Cfg.Normal.FilesPerMinute
		}
		if r.Cfg.Download != nil {
			meta.DownloadEnabled = true
			meta.DownloadUsers = len(r.Cfg.DownloadUsers)
			meta.DownloadParallelStreams = r.Cfg.Download.ParallelStreams
		}
	}
	for _, d := range r.DisabledUsers() {
		meta.Disabled = append(meta.Disabled, persist.DisabledUser{
			User:        d.User,
			Kind:        d.Kind,
			At:          d.At,
			Consecutive: d.Consecutive,
			TotalFailed: d.TotalFailed,
			LastCode:    d.LastCode,
			LastFile:    d.LastFile,
			LastAt:      d.LastAt,
		})
	}
	return persist.WriteMeta(reportsDir, meta)
}

// pickSize returns a random int64 in [minB, maxB]. When min==max it returns min.
func pickSize(minB, maxB int64) int64 {
	if maxB <= minB {
		return minB
	}
	span := maxB - minB + 1
	return minB + mathrand.Int63n(span)
}

type Run struct {
	ID        string
	StartedAt time.Time
	StartedBy string // "manual" | "schedule" — set by the web layer after Start
	Cfg       *config.RunConfig
	Metrics  *metrics.Engine
	Report   *report.Store
	Watcher  *trackid.Watcher

	// ctx governs the watcher + track-id consumer. Cancelled only during teardown.
	ctx    context.Context
	cancel context.CancelFunc

	// dispatchCtx governs new-upload dispatchers (runNormal / runLargeFile).
	// Cancelled when the user clicks Stop or the duration deadline passes.
	dispatchCtx    context.Context
	cancelDispatch context.CancelFunc

	doneCh chan struct{}
	err    atomic.Pointer[error]

	// uploadsWG tracks in-flight uploads that must finish before teardown.
	uploadsWG sync.WaitGroup

	// downloadsWG tracks in-flight downloads for graceful Stop.
	downloadsWG sync.WaitGroup

	// DispatchSkips counts ticks where the semaphore was full and we had to
	// skip an upload. Surfaces capacity bottlenecks to the UI.
	DispatchSkips atomic.Int64

	// Failure counters — visible to the UI via /api/status so operators don't
	// have to scan the record tail to see error health.
	FailedFiles atomic.Int64
	FailedBytes atomic.Int64 // partial bytes transferred before a mid-upload failure
	errCounts   errCounters  // failures broken down by ErrorCode

	// Download subsystem — pairless model.
	//
	// The tool has a list of upload users and a list of download users with
	// NO assumed mapping between them. Each download user polls its own
	// Download.Folder on the server and pulls whatever files appear there.
	// Attribution back to the originating upload row happens by BASENAME
	// (strip the "#<trackid>" suffix, look up in Report.byBasename). This
	// mirrors real platforms where the server decides routing and clients
	// just read their own mailbox.
	downloadPools     map[string]*clientPool
	DownloadCompleted atomic.Int64
	DownloadDropped   atomic.Int64 // reserved for future use (0 under poll model)
	DownloadOrphans   atomic.Int64 // files seen in an outbox that the tool didn't upload this run

	// per-user connection pools (parallel_streams clients each)
	poolMu sync.Mutex
	pools  map[string]*clientPool

	disable *disablePolicy

	// Streaming CSV writer: finalized rows are appended to disk during the
	// run and released from memory. Nil when no reports-dir is configured.
	reportStream *report.CSVStreamWriter
}

// ReportStreamPath returns the on-disk CSV being streamed (empty if not
// configured). The live-CSV HTTP handler reads from here plus a snapshot of
// the in-memory tail.
func (r *Run) ReportStreamPath() string {
	if r.reportStream == nil {
		return ""
	}
	return r.reportStream.Path()
}

func (r *Run) DisabledUsers() []DisabledSnapshot { return r.disable.snapshot() }

// poolSlot is one position in a user's connection pool. It holds the SSH
// credentials so a dead/dropped client can be lazily redialed on the next
// get(). Concurrent callers serialise through slot.mu — at worst, two
// uploaders waiting on the same slot will redial it once between them.
type poolSlot struct {
	user, pass, host string
	port             int

	mu     sync.Mutex
	client *sftpx.Client
}

// get returns a live client for this slot, redialing if needed.
func (s *poolSlot) get() (*sftpx.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	c, err := sftpx.Dial(s.host, s.port, s.user, s.pass)
	if err != nil {
		return nil, err
	}
	s.client = c
	return s.client, nil
}

// markDead closes + nils the client so the next get() redials. Called by
// callers when an SFTP operation on this slot's client fails — "most likely
// cause is a broken connection, worst case is we redial a perfectly-good
// one", which is strictly better than round-robinning dead sockets forever.
func (s *poolSlot) markDead() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
}

func (s *poolSlot) close() { s.markDead() }

type clientPool struct {
	mu    sync.Mutex
	slots []*poolSlot
	idx   int
}

// get picks a slot round-robin and lazily redials if it's dead. If the
// round-robin pick's slot is unreachable, falls through to the other slots
// in the same pool before giving up — a single dead slot doesn't stall the
// user.
func (p *clientPool) get() (*sftpx.Client, *poolSlot, error) {
	p.mu.Lock()
	n := len(p.slots)
	if n == 0 {
		p.mu.Unlock()
		return nil, nil, errors.New("pool empty")
	}
	start := p.idx
	p.idx = (p.idx + 1) % n
	p.mu.Unlock()

	var firstErr error
	for attempt := 0; attempt < n; attempt++ {
		slot := p.slots[(start+attempt)%n]
		c, err := slot.get()
		if err == nil {
			return c, slot, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, nil, fmt.Errorf("all pool slots unreachable (last: %w)", firstErr)
}

func (p *clientPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.slots {
		s.close()
	}
	p.slots = nil
}

// Start is kept for callers that don't need persistence.
func Start(parent context.Context, cfg *config.RunConfig) (*Run, error) {
	return StartWithPersist(parent, cfg, "")
}

// StartWithPersist launches a run and, when the run completes, flushes the
// final CSV report + a metadata JSON to reportsDir (empty string disables
// persistence). The files are written atomically so a partial write never
// shows up in history.
func StartWithPersist(parent context.Context, cfg *config.RunConfig, reportsDir string) (*Run, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	dispatchCtx, cancelDispatch := context.WithCancel(ctx)

	r := &Run{
		ID:             fmt.Sprintf("run-%d", time.Now().Unix()),
		StartedAt:      time.Now(),
		Cfg:            cfg,
		Metrics:        metrics.New(0.30),
		Report:         report.NewStore(),
		ctx:            ctx,
		cancel:         cancel,
		dispatchCtx:    dispatchCtx,
		cancelDispatch: cancelDispatch,
		doneCh:         make(chan struct{}),
		pools:          map[string]*clientPool{},
		downloadPools:  map[string]*clientPool{},
	}
	if cfg.MaxConsecutiveFailures > 0 {
		r.disable = newDisablePolicy(cfg.MaxConsecutiveFailures, cfg.NormalUsers, cfg.LargeFileUsers, cfg.DownloadUsers)
	}

	// Open the streaming CSV writer up front so records are sealed to disk
	// as they finalize. Keeps RAM flat on long runs.
	if reportsDir != "" {
		if err := os.MkdirAll(reportsDir, 0o700); err != nil {
			cancel()
			return nil, fmt.Errorf("mkdir reports dir: %w", err)
		}
		sw, err := report.NewCSVStreamWriter(persist.CSVPath(reportsDir, r.ID))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("open stream csv: %w", err)
		}
		r.reportStream = sw
		r.Report.SetStream(sw)
	}

	opener := func(user string) (*sftpx.Client, error) {
		pass := findPassword(cfg, user)
		return sftpx.Dial(cfg.Host, cfg.Port, user, pass)
	}
	r.Watcher = trackid.New(cfg.UploadFolder, cfg.PollInterval, cfg.TrackIDTimeout, opener)

	// Build per-user client pools (union of normal + large users).
	users := mergeUsers(cfg.NormalUsers, cfg.LargeFileUsers)
	for _, u := range users {
		pool, err := buildPool(cfg, u, cfg.ParallelStreams)
		if err != nil {
			r.teardown()
			cancel()
			return nil, fmt.Errorf("connect %s: %w", u.Username, err)
		}
		r.pools[u.Username] = pool
	}

	// Build download user pools if the download test is enabled.
	if cfg.Download != nil {
		for _, u := range cfg.DownloadUsers {
			pool, err := buildPool(cfg, u, cfg.Download.ParallelStreams)
			if err != nil {
				r.teardown()
				cancel()
				return nil, fmt.Errorf("download connect %s: %w", u.Username, err)
			}
			r.downloadPools[u.Username] = pool
		}
		// Each download user independently polls its own outbox. One worker
		// per user is plenty — the per-user client pool still gives us N
		// parallel file transfers via ParallelStreams on the worker side.
		for _, u := range cfg.DownloadUsers {
			go r.downloadWorker(ctx, u)
		}
	}

	// Watcher + track-id consumer live on the outer ctx so they keep running
	// after Stop is clicked and continue to resolve pending track-ids.
	go r.Watcher.Run(ctx)
	go r.consumeTrackIDs()

	// Background flusher: every 5s, move finalized records from memory to
	// the streaming CSV on disk. "Finalized" = failed, or (trackid resolved
	// AND download satisfied / skipped / long enough since trackid). Keeps
	// RAM flat on high-fpm long runs without losing detail.
	if r.reportStream != nil {
		go r.streamFlusher(ctx)
	}

	deadline := time.Now().Add(time.Duration(cfg.DurationHours * float64(time.Hour)))

	// Duration-deadline timer cancels the dispatchers just like Stop does.
	deadlineTimer := time.AfterFunc(time.Until(deadline), cancelDispatch)

	var wg sync.WaitGroup
	if cfg.Normal != nil {
		wg.Add(1)
		go func() { defer wg.Done(); r.runNormal(dispatchCtx, deadline) }()
	}
	if cfg.LargeFile != nil {
		wg.Add(1)
		go func() { defer wg.Done(); r.runLargeFile(dispatchCtx, deadline) }()
	}
	go func() {
		wg.Wait()                 // dispatchers stopped (user Stop or deadline)
		deadlineTimer.Stop()
		r.uploadsWG.Wait()        // let in-flight uploads finish cleanly
		// Keep watcher running to resolve remaining track-ids; wait up to timeout.
		drainDeadline := time.Now().Add(cfg.TrackIDTimeout + 5*time.Second)
		for time.Now().Before(drainDeadline) && r.Watcher.PendingCount() > 0 {
			time.Sleep(cfg.PollInterval)
		}
		// Give each download worker one last poll tick to pick up anything that
		// landed during the trackid drain, then cancel the outer ctx. Workers
		// exit their select on ctx.Done; wait for them before tearing pools down.
		time.Sleep(cfg.PollInterval + 500*time.Millisecond)
		cancel()
		r.downloadsWG.Wait()
		r.teardown()   // close SFTP pools
		if reportsDir != "" {
			if err := sealAllAndWriteMeta(r, reportsDir); err != nil {
				log.Printf("report flush: %v", err)
			}
		}
		close(r.doneCh)
	}()

	return r, nil
}

func (r *Run) Done() <-chan struct{} { return r.doneCh }

// isRecordFinal decides whether a record can be sealed to disk and freed
// from RAM. "Final" means no more mutations are expected:
//   - upload failed (ErrorCode set) → nothing more to attach
//   - upload succeeded + trackid resolved (real id or TRACKID_TIMEOUT):
//     if no download phase → final
//     if download phase and DownloadEndTime set → final
//     otherwise, final once enough time has passed since trackid was
//     detected that a download would have arrived if it was ever going to.
func (r *Run) isRecordFinal(rec *report.FileRecord) bool {
	if rec == nil {
		return false
	}
	if rec.ErrorCode != "" && rec.TrackID == "" {
		// Failed before the trackid stage was even relevant.
		return true
	}
	if rec.TrackID == "" {
		return false
	}
	// Trackid resolved (success or TIMEOUT).
	if r.Cfg.Download == nil {
		return true
	}
	if !rec.DownloadEndTime.IsZero() {
		return true
	}
	// Give downloads a grace window after the trackid landed. PollInterval
	// × 4 covers the expected round-trip; beyond that we assume it's not
	// coming and seal the row with whatever state it has.
	grace := r.Cfg.PollInterval * 4
	if grace < 20*time.Second {
		grace = 20 * time.Second
	}
	return !rec.TrackIDAt.IsZero() && time.Since(rec.TrackIDAt) > grace
}

// streamFlusher is the background goroutine that seals finalized records
// to disk on a steady cadence. Exits on ctx.Done; the teardown path calls
// sealAllAndWriteMeta afterward to catch anything still unsealed.
func (r *Run) streamFlusher(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			snap := r.Metrics.Snapshot()
			eff := func(bytes int64, startTime time.Time, dur time.Duration) float64 {
				return EffectiveSpeedMBps(bytes, startTime, dur, snap)
			}
			if _, err := r.Report.FlushFinalized(r.isRecordFinal, r.SlowdownMinutes(), eff); err != nil {
				log.Printf("stream flush: %v", err)
			}
		}
	}
}

// DownloadQueueDepth returns the total number of download jobs currently
// buffered across all per-user queues. Zero when downloads are disabled or
// fully drained.
//
// Under the pairless outbox-polling model there is no queue — downloads are
// driven by each user's own folder listings. Kept for API shape compat; always 0.
func (r *Run) DownloadQueueDepth() int { return 0 }

// IsActive reports whether the run is still dispatching or draining.
func (r *Run) IsActive() bool {
	select {
	case <-r.doneCh:
		return false
	default:
		return true
	}
}

// Stop halts new uploads. In-flight uploads finish, track-id polling continues
// until all pending IDs resolve (or TrackIDTimeout expires), then teardown runs.
func (r *Run) Stop() { r.cancelDispatch() }

// ErrorsByCode returns the live failure-count breakdown (copy — safe to
// serialize from the status handler).
func (r *Run) ErrorsByCode() map[string]int64 { return r.errCounts.snapshot() }

func (r *Run) Err() error {
	if p := r.err.Load(); p != nil {
		return *p
	}
	return nil
}

func (r *Run) consumeTrackIDs() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case res := <-r.Watcher.Results():
			r.Report.AttachTrackID(res.User, res.Basename, res.TrackID, res.DetectedAt, res.TimedOut)
			if res.TimedOut {
				r.errCounts.inc("TRACKID_TIMEOUT")
				r.disable.onFailureFor(res.User, "upload", "TRACKID_TIMEOUT", res.Basename)
			}
		}
	}
}

// downloadWorker polls one download user's outbox on a ticker. For any file
// that appears (and hasn't been downloaded yet), it opens the file, streams
// it, and attaches the DownloadResult back to the originating upload row by
// stripping the "#<trackid>" suffix and matching the basename.
//
// This is the pairless model: the tool does not need to know which upload
// user sent which file. The server decides routing; we just measure what
// lands where.
func (r *Run) downloadWorker(ctx context.Context, u config.UserCreds) {
	r.downloadsWG.Add(1)
	defer r.downloadsWG.Done()

	pool := r.downloadPools[u.Username]
	if pool == nil {
		return
	}
	folder := r.Cfg.Download.Folder
	if folder == "" {
		folder = r.Cfg.UploadFolder
	}

	seen := map[string]struct{}{} // filenames this worker has already acted on
	// Poll cadence matches the trackid watcher for simplicity. The outbox is
	// usually small (files drain fast), so a full List each tick is cheap.
	interval := r.Cfg.PollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	// Self-healing list client. Dedicated to this worker (not borrowed from
	// the pool, which has no reconnect) and independent of the transfer path.
	// Pattern mirrors the track-id watcher: lazy-dial on first use, close +
	// clear on any List error so the NEXT tick redials. This keeps a
	// multi-hour run alive across SSH idle-timeouts and transient drops
	// without silently stalling this user's downloads.
	var listClient *sftpx.Client
	defer func() {
		if listClient != nil {
			listClient.Close()
		}
	}()
	ensureList := func() (*sftpx.Client, error) {
		if listClient != nil {
			return listClient, nil
		}
		c, derr := sftpx.Dial(r.Cfg.Host, r.Cfg.Port, u.Username, u.Password)
		if derr != nil {
			return nil, derr
		}
		listClient = c
		return listClient, nil
	}
	dropList := func() {
		if listClient != nil {
			listClient.Close()
			listClient = nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		if r.disable.isDisabled(u.Username, "download") {
			return
		}

		c, err := ensureList()
		if err != nil {
			r.errCounts.inc("DOWNLOAD")
			r.disable.onFailure(u.Username, "download", "DOWNLOAD")
			continue
		}

		entries, err := c.List(folder)
		if err != nil {
			r.errCounts.inc("DOWNLOAD")
			dropList()
			r.disable.onFailure(u.Username, "download", "DOWNLOAD")
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if _, done := seen[name]; done {
				continue
			}
			// Only consider files that have a trackid suffix — these are the
			// ones the server has finished routing. Pre-#trackid files aren't ready.
			hash := strings.Index(name, "#")
			if hash <= 0 {
				continue
			}
			basename := name[:hash]
			seen[name] = struct{}{}

			c, slot, err := pool.get()
			start := time.Now()
			result := report.DownloadResult{
				DownloadUser: u.Username,
				StartTime:    start,
				AvailableAt:  e.ModTime(),
			}
			if err != nil {
				result.EndTime = start
				result.Error = err.Error()
				r.errCounts.inc("DOWNLOAD")
				r.disable.onFailureFor(u.Username, "download", "DOWNLOAD", basename)
			} else {
				n, derr := c.Download(path.Join(folder, name))
				result.EndTime = time.Now()
				result.SizeBytes = n
				if derr != nil {
					slot.markDead()
					result.Error = derr.Error()
					r.errCounts.inc("DOWNLOAD")
					r.disable.onFailureFor(u.Username, "download", "DOWNLOAD", basename)
				} else {
					result.SpeedMBps = report.RawSpeedMBps(n, result.EndTime.Sub(start))
					r.disable.onSuccess(u.Username, "download")
				}
			}
			// Attribute back to the originating upload row by basename. If we
			// didn't upload this file during this run (server-side leftover),
			// count it as orphan and skip the row update.
			if ok := r.Report.AttachDownloadByBasename(basename, result); !ok {
				r.DownloadOrphans.Add(1)
			}
			r.DownloadCompleted.Add(1)
		}
	}
}

// SlowdownFile pairs an upload-row with its track-id for a given slow minute.
type SlowdownFile struct {
	Filename string `json:"filename"`
	User     string `json:"user"`
	Kind     string `json:"kind"`
	TrackID  string `json:"track_id"`
}

// EnrichedSlowdown attaches the list of files uploaded during that minute.
// Files is capped at slowdownFilesCap to keep the status payload small at high FPM;
// TotalFiles is the uncapped count for context.
type EnrichedSlowdown struct {
	metrics.Slowdown
	Files      []SlowdownFile `json:"files"`
	TotalFiles int            `json:"total_files"`
}

const slowdownFilesCap = 20

// EnrichSlowdowns returns slowdowns with the files uploaded during each slowdown minute.
// Uses the report store's byMinute index so we don't scan (or copy) every record on
// every /api/status poll — cost is O(records-in-slowdown-minutes), not O(total).
func (r *Run) EnrichSlowdowns() []EnrichedSlowdown {
	snap := r.Metrics.Snapshot()
	out := make([]EnrichedSlowdown, 0, len(snap.Slowdowns))
	for _, sd := range snap.Slowdowns {
		minute := sd.At.Unix() / 60
		recs := r.Report.RecordsInMinute(minute)
		total := len(recs)
		var files []SlowdownFile
		for _, f := range recs {
			if len(files) >= slowdownFilesCap {
				break
			}
			files = append(files, SlowdownFile{
				Filename: f.Filename,
				User:     f.User,
				Kind:     f.Kind,
				TrackID:  f.TrackID,
			})
		}
		out = append(out, EnrichedSlowdown{Slowdown: sd, Files: files, TotalFiles: total})
	}
	return out
}

// EffectiveSpeedMBps returns the truest speed value for a single record:
//   - if the per-file timing is reliable (file >= 1 MiB AND transfer >= 100 ms),
//     the raw per-file rate is returned unchanged;
//   - otherwise the rate of the minute bucket the file falls in is used,
//     because small-file timings are swamped by handshake overhead while the
//     bucket rate is an honest aggregate over wall-time;
//   - if no bucket data exists yet (very start of a run), the overall run
//     rate is used;
//   - as a last resort, the raw per-file rate is returned so the column is
//     never blank.
func EffectiveSpeedMBps(bytes int64, startTime time.Time, dur time.Duration, snap metrics.Snapshot) float64 {
	raw := report.RawSpeedMBps(bytes, dur)
	if report.IsReliablePerFileSpeed(bytes, dur) {
		return raw
	}
	if !startTime.IsZero() {
		minute := startTime.Unix() / 60
		for _, b := range snap.PerMinute {
			if b.Minute == minute && b.MBps > 0 {
				return b.MBps
			}
		}
	}
	if snap.OverallMBps > 0 {
		return snap.OverallMBps
	}
	return raw
}

// SlowdownMinutes returns a set of minute-keys flagged as slow, for CSV annotation.
func (r *Run) SlowdownMinutes() map[int64]bool {
	snap := r.Metrics.Snapshot()
	m := make(map[int64]bool, len(snap.Slowdowns))
	for _, sd := range snap.Slowdowns {
		m[sd.At.Unix()/60] = true
	}
	return m
}

// pickActiveUser advances the round-robin cursor *i and returns the next
// non-disabled upload user. Returns ok=false if every user is disabled.
func pickActiveUser(users []config.UserCreds, i *uint64, disable *disablePolicy) (config.UserCreds, bool) {
	if len(users) == 0 {
		return config.UserCreds{}, false
	}
	for tries := 0; tries < len(users); tries++ {
		u := users[int(*i%uint64(len(users)))]
		*i++
		if disable == nil || !disable.isDisabled(u.Username, "upload") {
			return u, true
		}
	}
	return config.UserCreds{}, false
}

func (r *Run) runNormal(ctx context.Context, deadline time.Time) {
	fpm := r.Cfg.Normal.FilesPerMinute
	if fpm <= 0 {
		return
	}
	users := r.Cfg.NormalUsers
	minB := int64(r.Cfg.Normal.MinSizeMB) * 1024 * 1024
	maxB := int64(r.Cfg.Normal.MaxSizeMB) * 1024 * 1024

	// Batched dispatcher: a fixed 50 ms tick is reliable across OSes. We compute
	// how many files to fire per tick (fractional), carry the remainder across
	// ticks in an accumulator. This is stable from 1 fpm up to 100K+ fpm.
	const tickInterval = 50 * time.Millisecond
	filesPerTick := float64(fpm) * float64(tickInterval) / float64(time.Minute)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	var i uint64
	var lastMinute int64 = -1
	var accumulator float64

	sem := make(chan struct{}, len(users)*r.Cfg.ParallelStreams)

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.After(deadline) {
				return
			}
			// Reset the round-robin cursor at each minute boundary.
			if m := now.Unix() / 60; m != lastMinute {
				i = 0
				lastMinute = m
			}
			accumulator += filesPerTick
			count := int(accumulator)
			accumulator -= float64(count)
			for j := 0; j < count; j++ {
				u, ok := pickActiveUser(users, &i, r.disable)
				if !ok {
					r.cancelDispatch()
					return
				}
				// Non-blocking: if all parallel slots are busy, record a skip.
				select {
				case sem <- struct{}{}:
				default:
					r.DispatchSkips.Add(1)
					continue
				}
				size := pickSize(minB, maxB)
				r.uploadsWG.Add(1)
				go func(u config.UserCreds, size int64) {
					defer r.uploadsWG.Done()
					defer func() { <-sem }()
					r.uploadOne(u, size, "normal")
				}(u, size)
			}
		}
	}
}

func (r *Run) runLargeFile(ctx context.Context, deadline time.Time) {
	every := time.Duration(r.Cfg.LargeFile.IntervalMinutes) * time.Minute
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	unitBytes := int64(1024 * 1024)
	if r.Cfg.LargeFile.Unit == "GB" {
		unitBytes = 1024 * 1024 * 1024
	}
	minB := int64(r.Cfg.LargeFile.MinSize) * unitBytes
	maxB := int64(r.Cfg.LargeFile.MaxSize) * unitBytes
	users := r.Cfg.LargeFileUsers
	var i uint64

	fire := func() bool {
		u, ok := pickActiveUser(users, &i, r.disable)
		if !ok {
			r.cancelDispatch()
			return false
		}
		size := pickSize(minB, maxB)
		r.uploadsWG.Add(1)
		go func(u config.UserCreds, size int64) {
			defer r.uploadsWG.Done()
			r.uploadOne(u, size, "large")
		}(u, size)
		return true
	}

	// fire one immediately at start
	if len(users) > 0 && time.Now().Before(deadline) {
		if !fire() {
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				return
			}
			if !fire() {
				return
			}
		}
	}
}

// stageToCode maps the sftpx.Upload stage to our stable ErrorCode enum.
func stageToCode(stage string) string {
	switch stage {
	case "create":
		return "CREATE"
	case "write":
		return "WRITE"
	case "close":
		return "CLOSE"
	default:
		return "UNKNOWN"
	}
}

func (r *Run) uploadOne(u config.UserCreds, size int64, kind string) {
	// pick pattern round-robin-ish using nano clock
	pat := u.Patterns[int(time.Now().UnixNano())%len(u.Patterns)]
	name := generator.NameFromPattern(pat)
	remote := path.Join(r.Cfg.UploadFolder, name)

	// Content type only applies to the "normal" load today; large-file uploads
	// stay on fast binary bytes since they're stress-testing throughput.
	content := generator.ContentBinary
	if kind == "normal" && r.Cfg.Normal != nil && r.Cfg.Normal.ContentType != "" {
		content = r.Cfg.Normal.ContentType
	}

	start := time.Now()
	rec := report.FileRecord{
		User:         u.Username,
		Kind:         kind,
		Filename:     name,
		StartTime:    start,
		ExpectedSize: size,
	}

	recordFailure := func(rec report.FileRecord, code, msg string, bytesSent int64) {
		rec.ErrorCode = code
		rec.Error = msg
		rec.Incomplete = true
		rec.SizeBytes = bytesSent
		if rec.EndTime.IsZero() {
			rec.EndTime = time.Now()
		}
		r.Report.AddUpload(rec)
		r.FailedFiles.Add(1)
		r.FailedBytes.Add(bytesSent)
		r.errCounts.inc(code)
		// Pass the basename so the disabled-users chip can show *which* file
		// was the most recent victim — saves operators digging through the
		// per-file report for context.
		r.disable.onFailureFor(u.Username, "upload", code, rec.Filename)
	}

	// Panic recovery: a crash in pkg/sftp, the generator, or the pool would
	// otherwise kill this goroutine with no record. We want every attempted
	// upload accounted for — successful OR catastrophic.
	defer func() {
		if pr := recover(); pr != nil {
			recordFailure(rec, "PANIC", fmt.Sprintf("panic: %v", pr), 0)
		}
	}()

	pool := r.pools[u.Username]
	if pool == nil {
		recordFailure(rec, "POOL_EMPTY", "no connection pool for user", 0)
		return
	}
	c, slot, err := pool.get()
	if err != nil {
		recordFailure(rec, "POOL_EMPTY", err.Error(), 0)
		return
	}
	n, stage, err := c.Upload(remote, generator.FastReader(size, content))
	end := time.Now()
	rec.EndTime = end
	if err != nil {
		// Assume the SFTP connection is suspect and mark the slot dead; the
		// next uploader hitting this slot will redial. Worst-case we redial
		// a healthy slot on a spurious transient error — cheap.
		slot.markDead()
		recordFailure(rec, stageToCode(stage), err.Error(), n)
		return
	}
	rec.SizeBytes = n
	dur := end.Sub(start)
	rec.SpeedMBps = report.RawSpeedMBps(n, dur)
	r.Report.AddUpload(rec)
	r.Metrics.Record(end, n, dur)
	r.Watcher.Register(u.Username, name)
	r.disable.onSuccess(u.Username, "upload")
}

func (r *Run) teardown() {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	for _, p := range r.pools {
		p.closeAll()
	}
	r.pools = map[string]*clientPool{}
	for _, p := range r.downloadPools {
		p.closeAll()
	}
	r.downloadPools = map[string]*clientPool{}
}

// buildPool creates a user's connection pool. Each slot is dialed once at
// build time to fail-fast on bad credentials, but the slot remembers those
// creds so a mid-run drop can be self-healed without operator intervention.
func buildPool(cfg *config.RunConfig, u config.UserCreds, size int) (*clientPool, error) {
	if size < 1 {
		size = 1
	}
	p := &clientPool{}
	for i := 0; i < size; i++ {
		slot := &poolSlot{
			user: u.Username,
			pass: u.Password,
			host: cfg.Host,
			port: cfg.Port,
		}
		c, err := sftpx.Dial(cfg.Host, cfg.Port, u.Username, u.Password)
		if err != nil {
			p.closeAll()
			return nil, err
		}
		slot.client = c
		p.slots = append(p.slots, slot)
	}
	return p, nil
}

func mergeUsers(a, b []config.UserCreds) []config.UserCreds {
	seen := map[string]bool{}
	var out []config.UserCreds
	for _, list := range [][]config.UserCreds{a, b} {
		for _, u := range list {
			if seen[u.Username] {
				continue
			}
			seen[u.Username] = true
			out = append(out, u)
		}
	}
	return out
}

func findPassword(cfg *config.RunConfig, user string) string {
	for _, list := range [][]config.UserCreds{cfg.NormalUsers, cfg.LargeFileUsers, cfg.DownloadUsers} {
		for _, u := range list {
			if u.Username == user {
				return u.Password
			}
		}
	}
	return ""
}
