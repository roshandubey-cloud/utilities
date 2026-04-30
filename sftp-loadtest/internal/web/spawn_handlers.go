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
	"net/http"
	"os"
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
