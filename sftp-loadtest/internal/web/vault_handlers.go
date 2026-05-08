package web

// vault_handlers.go — REST + session surface for the OS-independent
// secret store (internal/vault). Wired by Server.Routes() so both
// the desktop Wails app and the headless worker expose the same
// endpoints. Secrets never leave the server in plaintext via these
// endpoints — Get returns whether a ref exists, not its value, and
// the runner pulls plaintext via the in-process Vault directly when
// it's about to use a credential.
//
// Flow:
//   1. /api/vault/status → tells the UI whether a vault file
//      exists at the configured path and whether it's currently
//      unlocked in this process.
//   2. /api/vault/unlock {passphrase} → opens (or, when create=true,
//      creates) the vault. Establishes the in-process Vault.
//   3. /api/vault/lock → forgets the passphrase + zeros key
//      material; subsequent /set / /get / /list fail with 403.
//   4. /api/vault/set {ref, secret} → stores a secret under ref.
//   5. /api/vault/list → returns refs (no values) so the UI can
//      show "you have these stored secrets" without ever
//      proxying plaintext through the browser.
//   6. /api/vault/delete {ref} → removes a secret.
//   7. /api/vault/change-passphrase {new_passphrase} → re-encrypts
//      under a fresh passphrase.
//
// All mutation routes go through the existing CSRFGuard +
// BodySizeLimit middleware (Server.Routes() wraps this whole mux).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/vault"
)

// vaultBinder owns the single in-process Vault. The Server keeps a
// pointer to it; nil means "locked" (no in-memory secrets reachable).
// The mutex covers the pointer swap so a concurrent /lock during a
// /set can't observe a torn state.
//
// Auto-lock-on-idle (v0.20.0): every successful Get / Set / Delete
// / List / Status-while-unlocked bumps lastTouch. A background
// goroutine (started in NewServer's path that invokes
// startVaultIdleSweep) flips the binder to locked when
// time.Since(lastTouch) > idleTimeout. Default 15 minutes; can be
// disabled by passing 0.
type vaultBinder struct {
	mu          sync.Mutex
	v           *vault.Vault
	lastTouch   time.Time
	idleTimeout time.Duration
}

func (b *vaultBinder) get() *vault.Vault {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v != nil {
		b.lastTouch = time.Now()
	}
	return b.v
}

func (b *vaultBinder) set(v *vault.Vault) {
	b.mu.Lock()
	old := b.v
	b.v = v
	if v != nil {
		b.lastTouch = time.Now()
	}
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (b *vaultBinder) clear() {
	b.set(nil)
}

// shouldAutoLock returns true when an unlocked vault has been idle
// past the configured timeout. The sweeper goroutine consults this
// every minute.
func (b *vaultBinder) shouldAutoLock(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v == nil || b.idleTimeout <= 0 {
		return false
	}
	return now.Sub(b.lastTouch) > b.idleTimeout
}

// SetVaultIdleTimeout configures auto-lock-on-idle. 0 disables;
// any positive duration starts the sweeper goroutine. Called from
// main.go (CLI) and cmd/desktop/main.go (Wails) — defaults are
// chosen at the call site rather than baked in here so server-mode
// (single-tenant CLI worker) can pick a longer timeout than the
// shared-laptop desktop app.
func (s *Server) SetVaultIdleTimeout(d time.Duration) {
	s.vaultBinder.mu.Lock()
	s.vaultBinder.idleTimeout = d
	s.vaultBinder.mu.Unlock()
	if d > 0 {
		s.startVaultIdleSweepOnce()
	}
}

var vaultSweeperStarted sync.Once

func (s *Server) startVaultIdleSweepOnce() {
	vaultSweeperStarted.Do(func() {
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-s.stopCh:
					return
				case now := <-t.C:
					if s.vaultBinder.shouldAutoLock(now) {
						s.vaultBinder.clear()
					}
				}
			}
		}()
	})
}

// handleVaultStatus reports whether the file exists + whether
// we have an unlocked instance in memory. The UI reads this on
// every page load to decide whether to prompt for a passphrase.
func (s *Server) handleVaultStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := s.vaultPath
	exists := false
	if path != "" {
		if _, err := osStat(path); err == nil {
			exists = true
		}
	}
	v := s.vaultBinder.get()
	resp := map[string]any{
		"path":      path,
		"exists":    exists,
		"unlocked":  v != nil,
	}
	if v != nil {
		resp["updated"] = v.Updated()
		resp["count"] = len(v.List())
	}
	writeJSON(w, resp)
}

// handleVaultUnlock opens the vault (creating it on demand when
// the create flag is set + the file doesn't exist).
func (s *Server) handleVaultUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.vaultPath == "" {
		http.Error(w, "vault path not configured", http.StatusInternalServerError)
		return
	}
	var body struct {
		Passphrase string `json:"passphrase"`
		Create     bool   `json:"create,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Passphrase == "" {
		http.Error(w, "empty passphrase", http.StatusBadRequest)
		return
	}

	// File missing + create flag set → Create. Otherwise → Open.
	_, statErr := osStat(s.vaultPath)
	var v *vault.Vault
	var err error
	if statErr != nil {
		if !body.Create {
			http.Error(w, "vault file does not exist; pass create=true to initialise", http.StatusNotFound)
			return
		}
		v, err = vault.Create(s.vaultPath, body.Passphrase)
	} else {
		v, err = vault.Open(s.vaultPath, body.Passphrase)
	}
	if err != nil {
		// Map known errors to HTTP statuses so the UI can branch.
		switch {
		case errors.Is(err, vault.ErrWrongPass):
			http.Error(w, "wrong passphrase", http.StatusForbidden)
		case errors.Is(err, vault.ErrBadMagic), errors.Is(err, vault.ErrUnknownVersion), errors.Is(err, vault.ErrUnknownKDF), errors.Is(err, vault.ErrShortHeader):
			http.Error(w, "vault file corrupted: "+err.Error(), http.StatusUnprocessableEntity)
		default:
			http.Error(w, fmt.Sprintf("unlock failed: %s", err.Error()), http.StatusInternalServerError)
		}
		return
	}
	s.vaultBinder.set(v)
	writeJSON(w, map[string]any{"ok": true, "count": len(v.List())})
}

// handleVaultLock zeros the in-memory key material + forgets the
// pointer. The next request that needs a secret will require an
// unlock round-trip.
func (s *Server) handleVaultLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.vaultBinder.clear()
	writeJSON(w, map[string]any{"ok": true})
}

// handleVaultSet stores a secret under a named ref.
func (s *Server) handleVaultSet(w http.ResponseWriter, r *http.Request) {
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	var body struct {
		Ref    string `json:"ref"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Ref == "" || body.Secret == "" {
		http.Error(w, "ref + secret required", http.StatusBadRequest)
		return
	}
	if err := v.Set(body.Ref, body.Secret); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := v.Save(); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleVaultGet returns the plaintext for a single ref. Available
// ONLY when the vault is unlocked + the request carries the
// standard CSRF token. Used by the saved-connection / saved-config
// flow to populate the form field when the operator picks an entry
// whose password lives in the vault. Threat model is identical to
// the operator pasting into the password field directly — same
// trust boundary (loopback HTTP in Wails, optionally remote HTTPS
// for headless workers).
func (s *Server) handleVaultGet(w http.ResponseWriter, r *http.Request) {
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	secret, ok := v.Get(body.Ref)
	if !ok {
		http.Error(w, "ref not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"secret": secret})
}

// handleVaultDelete removes a secret. Idempotent.
func (s *Server) handleVaultDelete(w http.ResponseWriter, r *http.Request) {
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	v.Delete(body.Ref)
	if err := v.Save(); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleVaultList returns the refs of every stored secret —
// values stay server-side, so the browser can render a "stored
// secrets" overview without ever holding plaintext.
func (s *Server) handleVaultList(w http.ResponseWriter, r *http.Request) {
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	writeJSON(w, map[string]any{"refs": v.List()})
}

// handleVaultChangePassphrase rotates the master key. The vault
// must already be unlocked (we don't accept old passphrase via
// HTTP — the operator already proved knowledge to /unlock).
func (s *Server) handleVaultChangePassphrase(w http.ResponseWriter, r *http.Request) {
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	var body struct {
		NewPassphrase string `json:"new_passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.NewPassphrase == "" {
		http.Error(w, "empty passphrase", http.StatusBadRequest)
		return
	}
	if err := v.ChangePassphrase(body.NewPassphrase); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// osStat is a tiny wrapper used so the handlers don't import os
// directly — keeps the test seam tidy when we mock filesystem in
// the future. Currently a thin pass-through.
func osStat(path string) (any, error) {
	return osStatImpl(path)
}
