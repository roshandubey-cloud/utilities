package vault

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRef(t *testing.T) {
	if !IsRef("$vault:connection:foo/password") {
		t.Errorf("IsRef should accept the canonical form")
	}
	if IsRef("plain-password") {
		t.Errorf("IsRef false-positive on plaintext")
	}
	if IsRef("") {
		t.Errorf("IsRef false-positive on empty")
	}
}

func TestMakeRefAndKey(t *testing.T) {
	r := MakeRef("connection", "prod-edi/password")
	if r != "$vault:connection:prod-edi/password" {
		t.Errorf("MakeRef shape changed: %q", r)
	}
	k, ok := RefKey(r)
	if !ok || k != "connection:prod-edi/password" {
		t.Errorf("RefKey: got (%q, %v); want (connection:prod-edi/password, true)", k, ok)
	}
}

func TestResolveString_Plaintext(t *testing.T) {
	got, isRef, found := ResolveString("hunter2", nil)
	if got != "hunter2" || isRef || found {
		t.Errorf("plaintext misclassified: got=%q isRef=%v found=%v", got, isRef, found)
	}
}

func TestResolveString_RefHit(t *testing.T) {
	v := newFastVault(t, "p")
	v.Set("connection:foo/password", "hunter2")
	got, isRef, found := ResolveString("$vault:connection:foo/password", v)
	if got != "hunter2" || !isRef || !found {
		t.Errorf("ref miss: got=%q isRef=%v found=%v", got, isRef, found)
	}
}

func TestResolveString_RefMissingFromVault(t *testing.T) {
	v := newFastVault(t, "p")
	got, isRef, found := ResolveString("$vault:connection:bar/password", v)
	if isRef != true || found != false {
		t.Errorf("missing ref classification: isRef=%v found=%v", isRef, found)
	}
	// Returned input verbatim so caller can surface a clear error
	// instead of silently substituting empty.
	if got != "$vault:connection:bar/password" {
		t.Errorf("missing ref should return input verbatim; got %q", got)
	}
}

func TestResolveString_VaultNil(t *testing.T) {
	got, isRef, found := ResolveString("$vault:x", nil)
	if !isRef || found {
		t.Errorf("nil vault classification: isRef=%v found=%v", isRef, found)
	}
	if got != "$vault:x" {
		t.Errorf("nil vault should return input verbatim; got %q", got)
	}
}

func TestResolveCSV_MixedPlaintextAndRefs(t *testing.T) {
	v := newFastVault(t, "p")
	v.Set("connection:u1/password", "secret1")
	v.Set("connection:u2/password", "secret2")
	in := "u1,$vault:connection:u1/password,inv-*\nu2,$vault:connection:u2/password,big-*\nu3,plaintext,doc-*\n"
	out := ResolveCSV(in, v)
	if !strings.Contains(out, "u1,secret1,inv-*") {
		t.Errorf("u1 row not resolved: %q", out)
	}
	if !strings.Contains(out, "u2,secret2,big-*") {
		t.Errorf("u2 row not resolved: %q", out)
	}
	if !strings.Contains(out, "u3,plaintext,doc-*") {
		t.Errorf("u3 plaintext mangled: %q", out)
	}
}

func TestResolveCSV_LockedVaultLeavesRefIntact(t *testing.T) {
	in := "u1,$vault:connection:u1/password,inv-*\n"
	out := ResolveCSV(in, nil)
	if !strings.Contains(out, "$vault:connection:u1/password") {
		t.Errorf("locked vault should leave ref intact for boundary error reporting; got %q", out)
	}
}

// newFastVault is a tiny copy of vault_test.go's helper so refs_test
// stays self-contained.
func newFastVault(t *testing.T, pass string) *Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := Create(filepath.Join(dir, "v.bin"), pass)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	v.memKiB = 1024
	v.iterations = 1
	v.threads = 1
	v.Save()
	return v
}
