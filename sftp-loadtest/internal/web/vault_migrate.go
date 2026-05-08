package web

// vault_migrate.go — bulk migration of plaintext credentials from
// schedule JSON files into the encrypted vault. The "first unlock"
// flow on the UI calls /api/vault/migrate-scan to learn which
// schedules carry plaintext + how many secrets each one has, then
// /api/vault/migrate-apply to perform the substitution: each
// plaintext is stored in the vault under a generated ref like
// `schedule:<id>/<field>`, the corresponding field in the schedule
// JSON is replaced with `$vault:schedule:<id>/<field>`, and the
// schedule file is rewritten atomically.
//
// The scan is read-only and safe to call before unlock; apply
// requires the vault unlocked + every CSV password column it
// touches to be plaintext (not already a $vault: marker).

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/vault"
)

// migrationCandidate describes one plaintext field worth moving
// into the vault. Surfaced verbatim to the UI so it can show a
// review list before the operator confirms.
type migrationCandidate struct {
	ScheduleID string `json:"schedule_id"`
	Field      string `json:"field"`            // human label: "target_password", "normal_users.csv:row3"
	Ref        string `json:"ref"`              // ref the migration would store under
}

// scanScheduleSecrets walks every schedule on disk and returns a
// list of plaintext credentials suitable for migration. Every
// candidate the UI displays is a real secret currently in the
// schedule file's body — there's no "did you already migrate
// this" guess: a $vault: marker is recognised and skipped.
func (s *Server) scanScheduleSecrets() []migrationCandidate {
	if s.schedules == nil {
		return nil
	}
	var out []migrationCandidate
	for _, sch := range s.schedules.list() {
		out = append(out, scheduleCandidates(sch)...)
	}
	return out
}

// scheduleCandidates extracts the plaintext credentials from a
// single Schedule. The function lives outside the scheduleStore
// receiver so it can be unit-tested without touching disk.
func scheduleCandidates(sch Schedule) []migrationCandidate {
	var out []migrationCandidate
	add := func(field, value string) {
		if value == "" || vault.IsRef(value) {
			return
		}
		out = append(out, migrationCandidate{
			ScheduleID: sch.ID,
			Field:      field,
			Ref:        "schedule:" + sch.ID + "/" + field,
		})
	}
	cfg := sch.Config
	add("target_password", cfg.TargetPassword)
	add("private_key_pem", cfg.PrivateKeyPEM)
	add("private_key_passphrase", cfg.PrivateKeyPassphrase)
	add("bastion_pass", cfg.BastionPass)
	add("bastion_private_key_pem", cfg.BastionPrivateKeyPEM)
	add("bastion_passphrase", cfg.BastionPassphrase)
	for kind, csvBody := range map[string]string{
		"normal_users":   cfg.NormalUsersCSV,
		"large_users":    cfg.LargeUsersCSV,
		"download_users": cfg.DownloadUsersCSV,
	} {
		out = append(out, csvCandidates(sch.ID, kind, csvBody)...)
	}
	return out
}

// csvCandidates returns one candidate per CSV row whose password
// column is plaintext (skipped when blank or already a $vault:
// marker). The CSV is parsed leniently — partial rows / odd
// quoting aren't a migration blocker; we just skip them.
func csvCandidates(scheduleID, kind, csvBody string) []migrationCandidate {
	if csvBody == "" {
		return nil
	}
	r := csv.NewReader(strings.NewReader(csvBody))
	r.FieldsPerRecord = -1
	var out []migrationCandidate
	rowNo := 0
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		rowNo++
		if len(row) < 2 {
			continue
		}
		user := row[0]
		pass := row[1]
		if pass == "" || vault.IsRef(pass) {
			continue
		}
		out = append(out, migrationCandidate{
			ScheduleID: scheduleID,
			Field:      kind + ".csv:" + user,
			Ref:        "schedule:" + scheduleID + "/" + kind + "." + user,
		})
	}
	return out
}

// applyScheduleMigrations runs the actual migration for every
// candidate: stores the plaintext in the vault under its computed
// ref + rewrites the schedule JSON to substitute the marker.
// Returns the count of successfully migrated entries (caller
// surfaces this as a toast) plus any per-candidate errors.
func (s *Server) applyScheduleMigrations(v *vault.Vault) (migrated int, failed []string) {
	if s.schedules == nil || v == nil {
		return 0, nil
	}
	for _, sch := range s.schedules.list() {
		changed := false
		cfg := sch.Config

		moveScalar := func(value string, refField string) string {
			if value == "" || vault.IsRef(value) {
				return value
			}
			refKey := "schedule:" + sch.ID + "/" + refField
			if err := v.Set(refKey, value); err != nil {
				failed = append(failed, sch.ID+"/"+refField+": "+err.Error())
				return value
			}
			migrated++
			changed = true
			return makeRefMarker(refKey)
		}
		cfg.TargetPassword = moveScalar(cfg.TargetPassword, "target_password")
		cfg.PrivateKeyPEM = moveScalar(cfg.PrivateKeyPEM, "private_key_pem")
		cfg.PrivateKeyPassphrase = moveScalar(cfg.PrivateKeyPassphrase, "private_key_passphrase")
		cfg.BastionPass = moveScalar(cfg.BastionPass, "bastion_pass")
		cfg.BastionPrivateKeyPEM = moveScalar(cfg.BastionPrivateKeyPEM, "bastion_private_key_pem")
		cfg.BastionPassphrase = moveScalar(cfg.BastionPassphrase, "bastion_passphrase")
		cfg.NormalUsersCSV = migrateCSV(cfg.NormalUsersCSV, sch.ID, "normal_users", v, &migrated, &changed, &failed)
		cfg.LargeUsersCSV = migrateCSV(cfg.LargeUsersCSV, sch.ID, "large_users", v, &migrated, &changed, &failed)
		cfg.DownloadUsersCSV = migrateCSV(cfg.DownloadUsersCSV, sch.ID, "download_users", v, &migrated, &changed, &failed)

		if changed {
			sch.Config = cfg
			if err := s.schedules.save(sch); err != nil {
				failed = append(failed, sch.ID+": save: "+err.Error())
			}
		}
	}
	if migrated > 0 {
		_ = v.Save()
	}
	return migrated, failed
}

// makeRefMarker is a thin wrapper that always returns the canonical
// `$vault:<refkey>` form. The refKey is pre-built (e.g.
// "schedule:abc/target_password") so we use MakeRef with an empty
// namespace and the full refKey as the key. Centralised so a
// future format change (different prefix, base64 etc.) lands in
// one place.
func makeRefMarker(refKey string) string {
	// MakeRef("schedule", "abc/x") → "$vault:schedule:abc/x".
	// We've pre-built the colon-joined refKey, so split once.
	for i := 0; i < len(refKey); i++ {
		if refKey[i] == ':' {
			return vault.MakeRef(refKey[:i], refKey[i+1:])
		}
	}
	return vault.MakeRef("", refKey)
}

// migrateCSV walks a CSV body, swaps every plaintext password
// column for a $vault: marker and stores the plaintext in the
// vault under the matching ref. Bumps `migrated`, sets `changed`
// when any row actually moved, and appends to `failed` on Set
// errors. Returns the rewritten CSV.
func migrateCSV(body, scheduleID, kind string, v *vault.Vault,
	migrated *int, changed *bool, failed *[]string) string {
	if body == "" {
		return body
	}
	r := csv.NewReader(strings.NewReader(body))
	r.FieldsPerRecord = -1
	var out strings.Builder
	w := csv.NewWriter(&out)
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		if len(row) >= 2 && row[1] != "" && !vault.IsRef(row[1]) {
			ref := "schedule:" + scheduleID + "/" + kind + "." + row[0]
			if serr := v.Set(ref, row[1]); serr != nil {
				*failed = append(*failed, ref+": "+serr.Error())
			} else {
				row[1] = makeRefMarker(ref)
				*migrated++
				*changed = true
			}
		}
		_ = w.Write(row)
	}
	w.Flush()
	return out.String()
}

// handleVaultMigrateScan reports candidates without changing
// anything. Safe to call when the vault is locked — the operator
// reviews the list and decides whether to unlock + apply.
func (s *Server) handleVaultMigrateScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	candidates := s.scanScheduleSecrets()
	writeJSON(w, map[string]any{"candidates": candidates, "count": len(candidates)})
}

// handleVaultMigrateApply runs the migration. Requires the vault
// already unlocked — we don't take the passphrase here, the
// operator unlocked it via /api/vault/unlock first.
func (s *Server) handleVaultMigrateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault locked", http.StatusForbidden)
		return
	}
	migrated, failed := s.applyScheduleMigrations(v)
	resp := map[string]any{"migrated": migrated, "failed": failed}
	if len(failed) > 0 {
		resp["partial"] = true
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
