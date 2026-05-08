package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// vaultHTTPRoundTrip pins the full HTTP surface for the secret
// vault: status → unlock(create) → set → list → status (count
// reflects writes) → lock → set (forbidden when locked) →
// unlock → list (data survived re-open).
//
// Pre-fix this single test would have caught any breakage in the
// glue between the HTTP layer and internal/vault: argument
// parsing, the binder swap, the locked-state guard, and that
// /set actually persists by re-reading after a lock cycle.
func TestVaultHTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(dir, "")
	defer srv.Shutdown()
	srv.SetVaultPath(filepath.Join(dir, "v.bin"))
	mux := srv.Routes()

	post := func(path string, body any) *httptest.ResponseRecorder {
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
		req.Header.Set("X-Requested-With", "sftp-loadtest")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Requested-With", "sftp-loadtest")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// 1. Status pre-create — file missing, locked.
	r := get("/api/vault/status")
	if r.Code != 200 {
		t.Fatalf("status: %d body=%s", r.Code, r.Body.String())
	}
	var status map[string]any
	json.Unmarshal(r.Body.Bytes(), &status)
	if status["exists"] != false || status["unlocked"] != false {
		t.Fatalf("expected exists=false unlocked=false; got %+v", status)
	}

	// 2. Unlock without create flag — should 404 because file
	//    doesn't exist.
	r = post("/api/vault/unlock", map[string]any{"passphrase": "p"})
	if r.Code != http.StatusNotFound {
		t.Fatalf("unlock without create flag: expected 404, got %d", r.Code)
	}

	// 3. Unlock with create=true → 200, vault initialised.
	r = post("/api/vault/unlock", map[string]any{"passphrase": "p", "create": true})
	if r.Code != 200 {
		t.Fatalf("unlock+create: %d body=%s", r.Code, r.Body.String())
	}

	// 4. Set two refs.
	r = post("/api/vault/set", map[string]any{"ref": "schedule:abc/password", "secret": "hunter2"})
	if r.Code != 200 {
		t.Fatalf("set 1: %d body=%s", r.Code, r.Body.String())
	}
	r = post("/api/vault/set", map[string]any{"ref": "connection:foo/private_key", "secret": "----- KEY -----"})
	if r.Code != 200 {
		t.Fatalf("set 2: %d", r.Code)
	}

	// 5. Status now reports unlocked + count=2.
	r = get("/api/vault/status")
	json.Unmarshal(r.Body.Bytes(), &status)
	if status["unlocked"] != true {
		t.Fatalf("expected unlocked=true; got %+v", status)
	}
	if cnt, _ := status["count"].(float64); cnt != 2 {
		t.Fatalf("expected count=2 after two sets; got %v", status["count"])
	}

	// 6. List returns refs only (no plaintext leak).
	r = get("/api/vault/list")
	var listResp struct{ Refs []string `json:"refs"` }
	json.Unmarshal(r.Body.Bytes(), &listResp)
	if len(listResp.Refs) != 2 {
		t.Fatalf("expected 2 refs; got %v", listResp.Refs)
	}
	for _, ref := range listResp.Refs {
		if ref == "hunter2" || ref == "----- KEY -----" {
			t.Fatalf("/list leaked a plaintext value into refs!")
		}
	}

	// 7. Lock → /set is forbidden.
	r = post("/api/vault/lock", nil)
	if r.Code != 200 {
		t.Fatalf("lock: %d", r.Code)
	}
	r = post("/api/vault/set", map[string]any{"ref": "x", "secret": "y"})
	if r.Code != http.StatusForbidden {
		t.Fatalf("set after lock should 403; got %d", r.Code)
	}
	r = get("/api/vault/list")
	if r.Code != http.StatusForbidden {
		t.Fatalf("list after lock should 403; got %d", r.Code)
	}

	// 8. Wrong passphrase → 403 ErrWrongPass.
	r = post("/api/vault/unlock", map[string]any{"passphrase": "wrong"})
	if r.Code != http.StatusForbidden {
		t.Fatalf("wrong pass should 403; got %d body=%s", r.Code, r.Body.String())
	}

	// 9. Right passphrase → reopen and confirm both refs persisted.
	r = post("/api/vault/unlock", map[string]any{"passphrase": "p"})
	if r.Code != 200 {
		t.Fatalf("re-unlock: %d", r.Code)
	}
	r = get("/api/vault/list")
	json.Unmarshal(r.Body.Bytes(), &listResp)
	if len(listResp.Refs) != 2 {
		t.Fatalf("after re-unlock expected 2 refs; got %v", listResp.Refs)
	}

	// 9b. /get returns the plaintext when unlocked; 404 for unknown
	//     ref; 403 when locked. This is the path the saved-
	//     connection apply flow uses to populate the password
	//     field, so it MUST stay strict.
	r = post("/api/vault/get", map[string]any{"ref": "schedule:abc/password"})
	if r.Code != 200 {
		t.Fatalf("/get unlocked existing: expected 200, got %d", r.Code)
	}
	var getResp struct{ Secret string `json:"secret"` }
	json.Unmarshal(r.Body.Bytes(), &getResp)
	if getResp.Secret != "hunter2" {
		t.Fatalf("/get returned wrong plaintext: %q", getResp.Secret)
	}
	r = post("/api/vault/get", map[string]any{"ref": "does-not-exist"})
	if r.Code != http.StatusNotFound {
		t.Fatalf("/get unknown ref: expected 404, got %d", r.Code)
	}
	post("/api/vault/lock", nil)
	r = post("/api/vault/get", map[string]any{"ref": "schedule:abc/password"})
	if r.Code != http.StatusForbidden {
		t.Fatalf("/get when locked: expected 403, got %d", r.Code)
	}
	// Re-unlock for the remaining steps.
	post("/api/vault/unlock", map[string]any{"passphrase": "p"})
	r = get("/api/vault/list")
	json.Unmarshal(r.Body.Bytes(), &listResp)

	// 10. Delete one ref + verify count drops.
	r = post("/api/vault/delete", map[string]any{"ref": listResp.Refs[0]})
	if r.Code != 200 {
		t.Fatalf("delete: %d", r.Code)
	}
	r = get("/api/vault/status")
	json.Unmarshal(r.Body.Bytes(), &status)
	if cnt, _ := status["count"].(float64); cnt != 1 {
		t.Fatalf("after delete expected count=1; got %v", status["count"])
	}
}
