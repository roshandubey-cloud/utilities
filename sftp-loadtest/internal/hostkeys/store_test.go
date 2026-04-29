package hostkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// makeKey returns a fresh ed25519 ssh.PublicKey for tests. ed25519 is the
// cheapest to generate and the same wire format you would see in
// production known_hosts lines.
func makeKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestStore_OpenEmpty_ListZero(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("fresh store List = %d, want 0", len(got))
	}
}

func TestStore_AddAndRoundTripPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, _ := Open(path)
	if err := s.Add("example.com", 22, makeKey(t), "ui-tofu"); err != nil {
		t.Fatal(err)
	}
	// Re-open from disk — entries must survive.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.List(); len(got) != 1 || got[0].Host != "example.com" {
		t.Errorf("round-trip lost entry: %+v", got)
	}
}

func TestStore_VerifyMatch(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	k := makeKey(t)
	_ = s.Add("h.example.com", 22, k, "test")
	if err := s.Verify("h.example.com:22", nil, k); err != nil {
		t.Errorf("Verify of stored key should be nil, got %v", err)
	}
}

func TestStore_VerifyUnknownHost(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	err := s.Verify("nope.example.com:22", nil, makeKey(t))
	if !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("want ErrUnknownHost, got %v", err)
	}
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatal("expected *VerifyError envelope")
	}
	if ve.Host != "nope.example.com" || ve.Port != 22 {
		t.Errorf("VerifyError host/port wrong: %+v", ve)
	}
	if ve.Fingerprint == "" {
		t.Error("VerifyError must carry the presented-key fingerprint so the UI can show it")
	}
}

func TestStore_VerifyKeyChanged(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	k1 := makeKey(t)
	k2 := makeKey(t)
	_ = s.Add("h", 22, k1, "test")
	err := s.Verify("h:22", nil, k2)
	if !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("want ErrKeyChanged when a different key is presented, got %v", err)
	}
	var ve *VerifyError
	_ = errors.As(err, &ve)
	if ve.Previous == "" || ve.Fingerprint == "" || ve.Previous == ve.Fingerprint {
		t.Errorf("VerifyError must carry both previous and new fingerprints (distinct), got %+v", ve)
	}
}

func TestStore_Remove(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	_ = s.Add("h", 22, makeKey(t), "test")
	removed, err := s.Remove("h", 22)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("Remove should report removed=true on a hit")
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("expected empty list after remove, got %+v", got)
	}
	// Second remove is a no-op.
	removed, _ = s.Remove("h", 22)
	if removed {
		t.Error("Remove must be idempotent — second call returns removed=false")
	}
}

func TestStore_HostKeyNormalisation(t *testing.T) {
	// IPv6 brackets and case differences must collapse to the same key.
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err := s.Add("Host.Example.COM", 22, makeKey(t), "test"); err != nil {
		t.Fatal(err)
	}
	// Same host with different casing — Verify should match against the
	// stored key (well, against the same key because we'll pass it back).
	// What we want to confirm is that ANOTHER ADD with different casing
	// does not duplicate the entry.
	_ = s.Add("host.example.com", 22, makeKey(t), "test")
	if got := s.List(); len(got) != 1 {
		t.Errorf("hostnames must collapse case-insensitively, got %d entries", len(got))
	}
}

func TestStore_TOFUCallback_AddsOnce(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	k := makeKey(t)
	cb := s.TOFUCallback(nil)
	if err := cb("h:22", nil, k); err != nil {
		t.Fatalf("first dial via TOFU should succeed, got %v", err)
	}
	// Second time: same key, already trusted.
	if err := cb("h:22", nil, k); err != nil {
		t.Fatalf("second dial of already-trusted host should pass, got %v", err)
	}
	// Third time: DIFFERENT key → must refuse, not auto-overwrite.
	k2 := makeKey(t)
	if err := cb("h:22", nil, k2); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("TOFU must refuse a changed key (only a new host gets auto-trusted), got %v", err)
	}
}

func TestStore_CaptureCallback_NeverMutates(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "hosts.json"))
	var captured CapturedKey
	cb := s.CaptureCallback(func(c CapturedKey) { captured = c })
	err := cb("new.example.com:22", nil, makeKey(t))
	if !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("CaptureCallback must surface ErrUnknownHost, not auto-add; got %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("CaptureCallback must NOT add the key — that's TOFU's job. List had %d entries", len(got))
	}
	if captured.Fingerprint == "" {
		t.Error("captured closure must receive the fingerprint so the UI can prompt")
	}
}
