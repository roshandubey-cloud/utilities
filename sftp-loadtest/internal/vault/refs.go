package vault

import (
	"encoding/csv"
	"strings"
)

// Ref-marker syntax for credentials that live in the encrypted
// vault. The marker is opaque to the rest of the codebase: storage
// (schedule JSON, user CSVs, exported configs) round-trips it
// verbatim; the runner / probe handler call ResolveRefs at the
// boundary where the credential is actually used. This keeps
// plaintext out of every persistence layer that historically had
// it baked in.
//
// Examples:
//   "$vault:connection:prod-edi/password"
//   "$vault:schedule:weekly-soak/target_password"
//
// Anything not starting with refPrefix is returned verbatim — so
// CSVs and JSONs that haven't migrated yet keep working.
const refPrefix = "$vault:"

// IsRef reports whether s is a vault ref marker. Cheap; runs on
// every CSV password cell at probe / start time.
func IsRef(s string) bool { return strings.HasPrefix(s, refPrefix) }

// MakeRef formats a ref marker for a given namespace + key.
// `namespace` should be one of "connection" / "schedule" /
// "preset" / "config" so the operator inspecting the vault list
// can read where each secret came from.
func MakeRef(namespace, key string) string {
	return refPrefix + namespace + ":" + key
}

// RefKey strips the marker prefix so callers can hand the inner
// key to Vault.Get / Vault.Set. Returns ("", false) when s is
// not a ref.
func RefKey(s string) (string, bool) {
	if !IsRef(s) {
		return "", false
	}
	return strings.TrimPrefix(s, refPrefix), true
}

// ResolveString swaps a single ref marker for its vault plaintext.
// Returns the input verbatim when:
//   - it isn't a ref (nothing to resolve)
//   - the vault is nil (caller hasn't unlocked yet)
//   - the ref isn't in the vault (stale config; caller's choice
//     to error or retry)
//
// The (resolved, isRef, found) return shape lets callers
// distinguish "wasn't a ref, treat as plaintext" from "was a
// ref but the vault doesn't have it" — the second case usually
// means the operator deleted the secret behind a saved entry's
// back, and the surface should surface that as a clear error
// rather than dialling out with an empty password.
func ResolveString(s string, v *Vault) (resolved string, isRef bool, found bool) {
	key, ok := RefKey(s)
	if !ok {
		return s, false, false
	}
	if v == nil {
		return s, true, false
	}
	plain, has := v.Get(key)
	if !has {
		return s, true, false
	}
	return plain, true, true
}

// ResolveCSV walks a user-list CSV (the same format
// internal/config.ParseUsersCSV consumes — `user,pass,pattern…`
// per line) and substitutes any password cell that's a vault
// ref with its plaintext. Output preserves every other field
// byte-for-byte so the runner sees an identical-shape CSV
// regardless of whether the operator typed plaintext or stored
// in the vault.
//
// Unresolved refs (vault locked, ref missing) are left intact —
// the runner / probe layer is responsible for surfacing a clear
// error in that case rather than us silently substituting empty.
func ResolveCSV(input string, v *Vault) string {
	if input == "" {
		return input
	}
	r := csv.NewReader(strings.NewReader(input))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = false

	var out strings.Builder
	w := csv.NewWriter(&out)
	for {
		row, err := r.Read()
		if err != nil {
			break // EOF or malformed — return whatever we collected; caller revalidates
		}
		if len(row) >= 2 {
			if resolved, isRef, found := ResolveString(row[1], v); isRef && found {
				row[1] = resolved
			}
		}
		_ = w.Write(row)
	}
	w.Flush()
	return out.String()
}
