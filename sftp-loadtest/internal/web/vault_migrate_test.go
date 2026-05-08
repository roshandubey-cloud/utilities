package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postJSON is a tiny test helper shared with the other web_test
// files — sends a CSRF-headed POST with a JSON body through the
// given mux.
func postJSON(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("X-Requested-With", "sftp-loadtest")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestScheduleCandidates_FindsPlaintextSkipsRefs pins the scan
// path: every plaintext credential field on a schedule produces
// one candidate, every $vault: marker is silently skipped.
func TestScheduleCandidates_FindsPlaintextSkipsRefs(t *testing.T) {
	s := Schedule{
		ID: "weekly-soak",
		Config: startReq{
			TargetPassword:           "plaintext-pwd",
			PrivateKeyPEM:            "----- BEGIN KEY -----",
			PrivateKeyPassphrase:     "$vault:already-migrated", // skip
			BastionPass:              "bp",
			BastionPrivateKeyPEM:     "",                        // skip
			BastionPassphrase:        "bphrase",
			NormalUsersCSV:           "u1,pw1,inv-*\nu2,$vault:foo,big-*\n", // u1 plaintext, u2 already migrated
		},
	}
	got := scheduleCandidates(s)
	wantFields := map[string]bool{
		"target_password":         false,
		"private_key_pem":         false,
		"bastion_pass":            false,
		"bastion_passphrase":      false,
		"normal_users.csv:u1":     false,
	}
	for _, c := range got {
		wantFields[c.Field] = true
	}
	for f, found := range wantFields {
		if !found {
			t.Errorf("missing candidate for plaintext field %q", f)
		}
	}
	for _, c := range got {
		if c.Field == "private_key_passphrase" || c.Field == "bastion_private_key_pem" || c.Field == "normal_users.csv:u2" {
			t.Errorf("candidate produced for ref/empty field %q (should be skipped)", c.Field)
		}
	}
}

// TestApplyScheduleMigrations_MovesPlaintextToVault pins the apply
// path: each plaintext field is stored under its computed ref +
// the schedule's field is rewritten to a $vault: marker. Re-
// scanning the same schedule afterwards should produce no
// candidates (idempotency).
func TestApplyScheduleMigrations_MovesPlaintextToVault(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(dir, dir)
	defer srv.Shutdown()
	srv.SetVaultPath(dir + "/v.bin")

	// Seed: one schedule with plaintext credentials.
	srv.schedules.save(Schedule{
		ID:    "weekly",
		RunAt: time.Now().Add(7 * 24 * time.Hour), // future, so the past-RunAt sweep doesn't drop it
		Config: startReq{
			TargetPassword: "plain-target",
			BastionPass:    "plain-bastion",
			NormalUsersCSV: "u1,pw1,inv-*\nu2,pw2,big-*\n",
		},
	})

	// Unlock vault (create) so the migration has somewhere to write.
	mux := srv.Routes()
	postJSON(t, mux, "/api/vault/unlock", map[string]any{"passphrase": "p", "create": true})

	v := srv.vaultBinder.get()
	if v == nil {
		t.Fatal("expected vault unlocked")
	}
	migrated, failed := srv.applyScheduleMigrations(v)
	if migrated != 4 { // target_password + bastion_pass + 2 CSV rows
		t.Errorf("expected 4 migrations; got %d (failed=%v)", migrated, failed)
	}
	if len(failed) > 0 {
		t.Errorf("unexpected migration failures: %v", failed)
	}

	// Reload and verify schedule now carries refs.
	updated := srv.schedules.list()
	if len(updated) != 1 {
		t.Fatalf("expected 1 schedule on disk; got %d", len(updated))
	}
	cfg := updated[0].Config
	if !strings.HasPrefix(cfg.TargetPassword, "$vault:") {
		t.Errorf("target_password not rewritten to ref: %q", cfg.TargetPassword)
	}
	if !strings.HasPrefix(cfg.BastionPass, "$vault:") {
		t.Errorf("bastion_pass not rewritten to ref: %q", cfg.BastionPass)
	}
	if !strings.Contains(cfg.NormalUsersCSV, "$vault:") {
		t.Errorf("normal_users_csv not rewritten: %q", cfg.NormalUsersCSV)
	}

	// Re-scan: zero candidates remain.
	if again := srv.scanScheduleSecrets(); len(again) != 0 {
		t.Errorf("re-scan after migration should be empty; got %d", len(again))
	}

	// Vault should hold the moved plaintext under retrievable refs.
	if got, ok := v.Get("schedule:weekly/target_password"); !ok || got != "plain-target" {
		t.Errorf("vault missing target_password; got=%q ok=%v", got, ok)
	}
	if got, ok := v.Get("schedule:weekly/normal_users.u1"); !ok || got != "pw1" {
		t.Errorf("vault missing CSV row; got=%q ok=%v", got, ok)
	}
}
