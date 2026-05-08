package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fastVault returns Argon2 parameters that finish in ~10 ms so the
// test suite stays under the per-test budget. Production defaults
// (64 MiB / 3 iterations) take ~150-300 ms per derivation, which
// would balloon the suite once we have ~10 round-trip tests.
func fastVault(t *testing.T, path, pass string) *Vault {
	t.Helper()
	v, err := Create(path, pass)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	v.memKiB = 1024 // 1 MiB — minimum that keeps the KDF meaningful
	v.iterations = 1
	v.threads = 1
	if err := v.Save(); err != nil {
		t.Fatalf("re-save with fast params: %v", err)
	}
	return v
}

// TestRoundTrip pins the happy path: create, set, save, close,
// reopen, get — the secret survives a process restart.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "correct horse battery staple")
	if err := v.Set("schedule:abc/password", "hunter2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Set("connection:foo/private_key", "-----BEGIN OPENSSH PRIVATE KEY-----\n…"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	v.Close()

	v2, err := Open(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := v2.Get("schedule:abc/password"); !ok || got != "hunter2" {
		t.Fatalf("get: got=%q ok=%v want=hunter2", got, ok)
	}
	if got, _ := v2.Get("connection:foo/private_key"); !strings.HasPrefix(got, "-----BEGIN") {
		t.Fatalf("private key did not survive round-trip: %q", got)
	}
}

// TestWrongPassphrase pins the AEAD-verify path. Opening with the
// wrong passphrase returns ErrWrongPass (not "corrupted file" —
// the UI distinguishes these to drive different recovery flows).
func TestWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "rightpass")
	v.Set("k", "secret")
	v.Save()
	v.Close()

	if _, err := Open(path, "wrongpass"); err != ErrWrongPass {
		t.Fatalf("expected ErrWrongPass, got %v", err)
	}
}

// TestEmptyPassRejected pins the input-validation path. Empty
// passphrase is rejected before the KDF runs — accepting it would
// derive a deterministic key from an empty seed, which is the
// classic "encrypted with no key" footgun.
func TestEmptyPassRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(filepath.Join(dir, "v.bin"), ""); err != ErrEmpty {
		t.Errorf("Create(\"\") expected ErrEmpty, got %v", err)
	}
	if _, err := Open(filepath.Join(dir, "missing.bin"), ""); err != ErrEmpty {
		t.Errorf("Open(\"\") expected ErrEmpty, got %v", err)
	}
}

// TestBadMagic pins the format-detection path. Loading a file
// that isn't a vault returns ErrBadMagic — we never blindly try
// to decrypt arbitrary bytes.
func TestBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "imposter.bin")
	if err := os.WriteFile(path, []byte("ZZZZ\x01\x01\x00\x00\x00\x40\x00\x00\x00\x03\x04"+strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "anything"); err != ErrBadMagic {
		t.Errorf("expected ErrBadMagic, got %v", err)
	}
}

// TestDelete pins the removal path: deleting a ref means subsequent
// Get returns false and the persisted vault no longer carries it.
func TestDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "p")
	v.Set("a", "1")
	v.Set("b", "2")
	v.Delete("a")
	if _, ok := v.Get("a"); ok {
		t.Errorf("delete didn't remove from in-memory map")
	}
	v.Save()
	v.Close()

	v2, err := Open(path, "p")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := v2.Get("a"); ok {
		t.Errorf("delete didn't persist")
	}
	if got, _ := v2.Get("b"); got != "2" {
		t.Errorf("delete clobbered other entries; b=%q", got)
	}
}

// TestNonceFreshness pins the most safety-critical AEAD invariant:
// every Save MUST roll a fresh nonce + salt. Reusing a nonce under
// the same key breaks ChaCha20-Poly1305 confidentiality entirely.
// We snapshot two save buffers from the same vault and confirm the
// 16-byte salt + 12-byte nonce regions differ.
func TestNonceFreshness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "p")
	v.Set("k", "secret")

	v.Save()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v.Save()
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Header is: 4 magic + 1 version + 1 kdf + 4 mem + 4 iter + 1 thr = 15 bytes
	// then 16 salt + 12 nonce.
	if bytes.Equal(first[15:15+16+12], second[15:15+16+12]) {
		t.Fatal("salt+nonce repeated between Saves — nonce reuse breaks AEAD")
	}
}

// TestList pins the metadata-only listing path. List() returns
// refs without exposing values, so the UI can render "you have
// these N stored secrets" without having to handle plaintext in
// the browser.
func TestList(t *testing.T) {
	dir := t.TempDir()
	v := fastVault(t, filepath.Join(dir, "v.bin"), "p")
	v.Set("alpha", "1")
	v.Set("bravo", "2")
	v.Set("charlie", "3")
	got := v.List()
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("List length: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestChangePassphrase pins the rotation path: existing secrets
// are preserved, the new passphrase opens, the old does not.
func TestChangePassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "old")
	v.Set("k", "secret")
	v.Save()
	if err := v.ChangePassphrase("new"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	v.Close()

	if _, err := Open(path, "old"); err != ErrWrongPass {
		t.Errorf("old passphrase still works after rotation: %v", err)
	}
	v2, err := Open(path, "new")
	if err != nil {
		t.Fatalf("new passphrase failed: %v", err)
	}
	if got, _ := v2.Get("k"); got != "secret" {
		t.Errorf("rotation lost data; got %q", got)
	}
}

// TestAtomicSave pins the crash-safety path. After a normal Save
// the .tmp file should not linger — only the canonical path.
func TestAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.bin")
	v := fastVault(t, path, "p")
	v.Set("k", "secret")
	v.Save()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file should not exist after a clean Save: %v", err)
	}
}
