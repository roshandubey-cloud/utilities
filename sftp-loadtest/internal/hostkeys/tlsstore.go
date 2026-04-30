// tlsstore.go — TOFU fingerprint store for FTPS server certificates.
//
// Mirrors the SSH host-key Store but keyed off a TLS leaf certificate's
// SHA-256 fingerprint. The runtime keeps the in-memory map authoritative;
// the JSON file is the persistence layer. Same operator workflow as SSH:
// first probe captures the fingerprint, the UI prompts, accept appends.
//
// Kept in the same package so the web layer can reuse the existing
// HostKeyStore plumbing (path discovery, JSON envelope, /api/hostkeys
// endpoint family) for FTPS too.

package hostkeys

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// TLSEntry is one trusted FTPS server's leaf certificate.
type TLSEntry struct {
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint"` // "SHA256:<hex>" — matches the protocol.Fingerprint format
	Subject     string    `json:"subject,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	AddedAt     time.Time `json:"added_at"`
}

type tlsFileFormat struct {
	Version int        `json:"version"`
	Hosts   []TLSEntry `json:"hosts"`
}

// Sentinel errors mirror the SSH store so callers can use the same switch.
var (
	ErrUnknownTLSHost = errors.New("ftps cert not trusted; user consent required")
	ErrTLSCertChanged = errors.New("ftps cert has changed; user consent required to overwrite")
)

// TLSVerifyError carries fingerprints back to the caller without forcing a
// second store lookup.
type TLSVerifyError struct {
	Err         error
	Host        string
	Port        int
	Fingerprint string
	Previous    string
}

func (e *TLSVerifyError) Error() string { return e.Err.Error() }
func (e *TLSVerifyError) Unwrap() error { return e.Err }

// TLSStore holds TLS-cert fingerprints for trusted FTPS servers.
type TLSStore struct {
	path string
	mu   sync.RWMutex
	// keyed by canonical "host:port".
	entries map[string]TLSEntry
}

// OpenTLS loads the store from path. Missing file = empty store.
func OpenTLS(path string) (*TLSStore, error) {
	s := &TLSStore{path: path, entries: map[string]TLSEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return s, nil
	}
	var f tlsFileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, e := range f.Hosts {
		s.entries[hostKey(e.Host, e.Port)] = e
	}
	return s, nil
}

func (s *TLSStore) Path() string { return s.path }

// List returns a stable, sorted snapshot.
func (s *TLSStore) List() []TLSEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TLSEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// Verify returns nil iff the presented cert fingerprint matches a stored
// entry for (host, port). Returns *TLSVerifyError wrapping ErrUnknownTLSHost
// or ErrTLSCertChanged otherwise.
func (s *TLSStore) Verify(host string, port int, presented *x509.Certificate) error {
	if presented == nil {
		return errors.New("ftps verify: nil cert")
	}
	fp := tlsFingerprint(presented)
	s.mu.RLock()
	stored, ok := s.entries[hostKey(host, port)]
	s.mu.RUnlock()
	if !ok {
		return &TLSVerifyError{Err: ErrUnknownTLSHost, Host: host, Port: port, Fingerprint: fp}
	}
	if stored.Fingerprint != fp {
		return &TLSVerifyError{Err: ErrTLSCertChanged, Host: host, Port: port, Fingerprint: fp, Previous: stored.Fingerprint}
	}
	return nil
}

// Add stores (overwrites) the trust entry for (host, port).
func (s *TLSStore) Add(host string, port int, cert *x509.Certificate) error {
	if cert == nil {
		return errors.New("ftps add: nil cert")
	}
	entry := TLSEntry{
		Host:        host,
		Port:        port,
		Fingerprint: tlsFingerprint(cert),
		Subject:     cert.Subject.String(),
		Issuer:      cert.Issuer.String(),
		NotAfter:    cert.NotAfter,
		AddedAt:     time.Now().UTC(),
	}
	s.mu.Lock()
	s.entries[hostKey(host, port)] = entry
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// Remove drops the trust entry. Returns true if an entry was actually removed.
func (s *TLSStore) Remove(host string, port int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hostKey(host, port)
	if _, ok := s.entries[key]; !ok {
		return false, nil
	}
	delete(s.entries, key)
	return true, s.saveLocked()
}

func (s *TLSStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *TLSStore) saveLocked() error {
	out := tlsFileFormat{Version: 1}
	for _, e := range s.entries {
		out.Hosts = append(out.Hosts, e)
	}
	sort.Slice(out.Hosts, func(i, j int) bool {
		if out.Hosts[i].Host != out.Hosts[j].Host {
			return out.Hosts[i].Host < out.Hosts[j].Host
		}
		return out.Hosts[i].Port < out.Hosts[j].Port
	})
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// tlsFingerprint returns "SHA256:" + hex(DER) — matches protocol.Fingerprint.
func tlsFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + hex.EncodeToString(sum[:])
}
