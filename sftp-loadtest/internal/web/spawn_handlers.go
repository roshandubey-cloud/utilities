// SSH-bootstrapped worker registry + endpoints (v0.11.0).
//
// The master holds a map of spawnedWorker keyed by id. Each entry owns a
// Tunnel from internal/sshtunnel — the SSH session that bootstraps the
// remote and the loopback listener that forwards HTTP through it. The
// cluster code consumes the tunnel via spawnedWorker.URL like any other
// HTTP-reachable worker URL.
//
// Endpoints:
//
//   POST /api/worker/spawn    — start a tunnel
//   POST /api/worker/despawn  — close a tunnel by id
//   GET  /api/worker/spawned  — list registered tunnels
//
// All three flow through the standard CSRF + body-cap + rate-limit
// middleware (see security.go's bodySizeLimits + rateLimitedPaths).

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sshtunnel"
)

type spawnedWorker struct {
	ID            string    `json:"id"`
	URL           string    `json:"url"`
	RemoteHost    string    `json:"remote_host"`
	Arch          string    `json:"arch"`
	InstallMethod string    `json:"install_method"`
	SpawnedAt     time.Time `json:"spawned_at"`
	SpawnLog      []string  `json:"log"`
	tunnel        *sshtunnel.Tunnel
}

var (
	spawnMu      sync.Mutex
	spawnedByID  = map[string]*spawnedWorker{}
	spawnCounter uint64
)

func nextSpawnID() string {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	spawnCounter++
	return fmt.Sprintf("ssh-%d-%d", time.Now().Unix(), spawnCounter)
}

type spawnReq struct {
	Host             string `json:"host"`
	Port             string `json:"port"`
	User             string `json:"user"`
	Password         string `json:"password"`
	PrivateKeyPEM    string `json:"private_key_pem"`
	Passphrase       string `json:"passphrase"`
	InstallMethod    string `json:"install_method"`
	ReleaseTag       string `json:"release_tag"`
	RemoteBinaryPath string `json:"remote_binary_path"`
	RemoteBindAddr   string `json:"remote_bind_addr"`
	// TCPOnly short-circuits handleWorkerPreflight to a credential-free
	// net.DialTimeout against host:port. Used by the wizard's Step S1
	// "Test reachability" button so the operator can verify the network
	// before they have to type the SSH user / key.
	TCPOnly bool `json:"tcp_only"`
}

type spawnResp struct {
	ID   string   `json:"id"`
	URL  string   `json:"url"`
	Arch string   `json:"arch"`
	Log  []string `json:"log"`
}

// handleWorkerSpawn drives a fresh sshtunnel.Spawn and registers the
// resulting tunnel. Auth is mutually exclusive in the UI but we accept
// either field on the wire; the underlying Spawn prefers PrivateKeyPEM.
//
// The local-binary path is derived server-side via os.Executable() when
// install_method=="upload" — the UI never gets to point us at an arbitrary
// local file, which would be a server-side LFI vector.
func (s *Server) handleWorkerSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req spawnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Host == "" || req.User == "" {
		http.Error(w, "host and user required", http.StatusBadRequest)
		return
	}
	if req.Password == "" && req.PrivateKeyPEM == "" {
		http.Error(w, "password or private key required", http.StatusBadRequest)
		return
	}
	method := req.InstallMethod
	if method == "" {
		method = "download"
	}

	opts := sshtunnel.SpawnOpts{
		Host:             req.Host,
		Port:             req.Port,
		User:             req.User,
		Password:         req.Password,
		PrivateKeyPEM:    req.PrivateKeyPEM,
		Passphrase:       req.Passphrase,
		InstallMethod:    method,
		ReleaseTag:       req.ReleaseTag,
		RemoteBinaryPath: req.RemoteBinaryPath,
		RemoteBindAddr:   req.RemoteBindAddr,
	}

	if method == "upload" {
		// Resolve the master's own binary path. os.Executable returns the
		// absolute path of the running process — this is what we stream.
		exe, err := os.Executable()
		if err != nil {
			http.Error(w, "could not resolve master binary: "+err.Error(), http.StatusInternalServerError)
			return
		}
		opts.LocalBinaryPath = exe
	}

	// Reuse the host-key store when configured so SSH bootstrap respects
	// the same trust decisions as the SFTP target dial path.
	if store := s.getHostKeyStore(); store != nil {
		opts.HostKey = store.StrictCallback()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	t, err := sshtunnel.Spawn(ctx, opts)
	if err != nil {
		log.Printf("spawn worker %s@%s: %v", req.User, req.Host, err)
		// Best-effort: include the partial spawn log so the UI can show
		// which step actually failed.
		var partial []string
		if t != nil {
			partial = t.SpawnLog
			_ = t.Close()
		}
		writeJSON(w, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"log":   partial,
		})
		return
	}

	id := nextSpawnID()
	entry := &spawnedWorker{
		ID:            id,
		URL:           t.LocalURL,
		RemoteHost:    req.Host,
		Arch:          t.Arch,
		InstallMethod: method,
		SpawnedAt:     time.Now(),
		SpawnLog:      t.SpawnLog,
		tunnel:        t,
	}
	spawnMu.Lock()
	spawnedByID[id] = entry
	spawnMu.Unlock()

	writeJSON(w, spawnResp{
		ID:   id,
		URL:  t.LocalURL,
		Arch: t.Arch,
		Log:  t.SpawnLog,
	})
}

// handleWorkerDespawn closes a tunnel by id. The remote process is killed
// as part of Tunnel.Close so this isn't just a localStorage cleanup.
func (s *Server) handleWorkerDespawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	spawnMu.Lock()
	entry, ok := spawnedByID[req.ID]
	if ok {
		delete(spawnedByID, req.ID)
	}
	spawnMu.Unlock()
	if !ok {
		http.Error(w, "no such spawned worker", http.StatusNotFound)
		return
	}
	if err := entry.tunnel.Close(); err != nil {
		log.Printf("despawn %s: close: %v", req.ID, err)
	}
	writeJSON(w, map[string]any{"ok": true, "id": req.ID})
}

// handleWorkerSpawnedList returns a snapshot of the registered tunnels.
// SpawnLog is included so the UI can show the bootstrap trail even after
// the modal that initiated the spawn was dismissed.
func (s *Server) handleWorkerSpawnedList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	spawnMu.Lock()
	out := make([]*spawnedWorker, 0, len(spawnedByID))
	for _, e := range spawnedByID {
		out = append(out, e)
	}
	spawnMu.Unlock()
	writeJSON(w, map[string]any{"workers": out})
}

// handleWorkerPreflight runs sshtunnel.Preflight against the same
// credentials the operator would use for /worker/spawn. No state is
// mutated on the remote — this is the "Test SSH" button's backend.
// Returns the structured PreflightResult so the UI can render a
// step-by-step verdict (reachable / arch / writable / curl / unzip).
func (s *Server) handleWorkerPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req spawnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// TCP-only mode: wizard Step S1 "Test reachability" before the
	// operator has typed any credentials. We skip auth entirely and
	// just confirm a TCP socket can be opened to host:port. Returns the
	// same {ok, reachable, log[]} shape the full preflight uses so the
	// UI renders identically.
	if req.TCPOnly {
		if req.Host == "" {
			http.Error(w, "host required", http.StatusBadRequest)
			return
		}
		port := req.Port
		if port == "" {
			port = "22"
		}
		addr := net.JoinHostPort(req.Host, port)
		t0 := time.Now()
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		latency := time.Since(t0).Milliseconds()
		out := map[string]any{
			"ok":         err == nil,
			"reachable":  err == nil,
			"latency_ms": latency,
		}
		if err != nil {
			out["error"] = err.Error()
			out["log"] = []string{
				"Probing TCP " + addr,
				"✗ tcp dial: " + err.Error(),
			}
		} else {
			_ = conn.Close()
			out["log"] = []string{
				"Probing TCP " + addr,
				fmt.Sprintf("✓ tcp dial ok in %d ms", latency),
			}
		}
		writeJSON(w, out)
		return
	}
	if req.Host == "" || req.User == "" {
		http.Error(w, "host and user required", http.StatusBadRequest)
		return
	}
	if req.Password == "" && req.PrivateKeyPEM == "" {
		http.Error(w, "password or private key required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := sshtunnel.Preflight(ctx, sshtunnel.SpawnOpts{
		Host:             req.Host,
		Port:             req.Port,
		User:             req.User,
		Password:         req.Password,
		PrivateKeyPEM:    req.PrivateKeyPEM,
		Passphrase:       req.Passphrase,
		RemoteBinaryPath: req.RemoteBinaryPath,
	})
	if err != nil {
		// Preflight returns nil error for "checks ran, some failed";
		// a non-nil error means the input was malformed.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, res)
}

// workerProbeResult is what /api/worker/probe returns. Same shape as
// the generic upstream healthz so the UI doesn't need a per-source
// adapter — manual URL workers and SSH-bootstrapped workers both
// produce this.
type workerProbeResult struct {
	OK          bool   `json:"ok"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	Active      bool   `json:"active"`
	ActiveRunID string `json:"active_run_id,omitempty"`
	UptimeSec   int64  `json:"uptime_sec,omitempty"`
	Latency     int64  `json:"latency_ms"`
	Error       string `json:"error,omitempty"`
}

// handleWorkerProbe pings a worker's /healthz from the master. CORS
// blocks the browser from doing this directly (workers run on
// different origins), so the master proxies the check. Used by the
// sidebar Workers section to render a live status LED per row.
//
// Body: {"url": "http://host:port", "auth_user": "...", "auth_pass": "..."}.
// Auth fields optional — only forwarded when the operator configured
// HTTP basic auth on the worker.
func (s *Server) handleWorkerProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL      string `json:"url"`
		AuthUser string `json:"auth_user"`
		AuthPass string `json:"auth_pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	out := workerProbeResult{URL: req.URL}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	target := strings.TrimRight(req.URL, "/") + "/healthz?detail=1"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, out)
		return
	}
	httpReq.Header.Set("X-Requested-With", "sftp-loadtest")
	if req.AuthUser != "" {
		httpReq.SetBasicAuth(req.AuthUser, req.AuthPass)
	}
	t0 := time.Now()
	resp, err := workerHTTPClient.Do(httpReq)
	out.Latency = time.Since(t0).Milliseconds()
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, out)
		return
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode
	out.OK = resp.StatusCode == http.StatusOK
	if !out.OK {
		out.Error = "worker returned HTTP " + resp.Status
		writeJSON(w, out)
		return
	}
	var body struct {
		Status      string `json:"status"`
		ActiveRun   bool   `json:"active_run"`
		ActiveRunID string `json:"active_run_id"`
		UptimeSec   int64  `json:"uptime_sec"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	out.Active = body.ActiveRun
	out.ActiveRunID = body.ActiveRunID
	out.UptimeSec = body.UptimeSec
	writeJSON(w, out)
}

// workerHTTPClient is the master-side HTTP client used for worker
// probes. Short timeouts so an unresponsive worker doesn't hold the
// sidebar poll loop hostage.
var workerHTTPClient = &http.Client{Timeout: 5 * time.Second}

// closeAllSpawned tears down every registered tunnel. Called from
// Server.Shutdown so the master never leaves an orphan SSH session
// behind on a clean exit.
func closeAllSpawned() {
	spawnMu.Lock()
	entries := make([]*spawnedWorker, 0, len(spawnedByID))
	for _, e := range spawnedByID {
		entries = append(entries, e)
	}
	spawnedByID = map[string]*spawnedWorker{}
	spawnMu.Unlock()
	for _, e := range entries {
		_ = e.tunnel.Close()
	}
}
