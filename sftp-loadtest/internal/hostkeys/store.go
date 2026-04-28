// Package hostkeys is the tool-managed SSH host-key trust store.
//
// The default deployment used to depend on an OpenSSH-format known_hosts
// file under the user's config dir. That was fragile: an operator (or any
// other process running as the same user) could edit it while the tool
// was offline and the runtime would silently start trusting whatever was
// in there. There was also no UI surface for listing or removing trusted
// hosts — the "all SSH state lives in the UI" promise quietly leaked into
// "all SSH state lives in a text file you should not touch".
//
// This package replaces that with a JSON file owned by the tool. The
// in-memory map is the source of truth at runtime; the file is the
// persistence layer rebuilt by atomic write after every change. The
// /api/hostkeys/* HTTP surface is the only supported way to add or
// remove entries — any out-of-band edit to hosts.json gets overwritten
// the next time a probe completes.
package hostkeys

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Entry is one trusted host:port + its ssh public key. KeyBlob is the
// serialised ssh.PublicKey (same wire format OpenSSH writes after the
// key-type token); we match against this byte-for-byte at probe time so
// the trust decision is cryptographic, not fingerprint-based.
type Entry struct {
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	KeyType     string    `json:"key_type"`
	KeyBlobB64  string    `json:"key_blob_b64"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"added_at"`
	AddedBy     string    `json:"added_by,omitempty"`
}

// fileFormat is the on-disk JSON envelope.
type fileFormat struct {
	Version int     `json:"version"`
	Hosts   []Entry `json:"hosts"`
}

// Sentinel errors returned by Verify.
var (
	// ErrUnknownHost — the (host, port) is not in the store.
	ErrUnknownHost = errors.New("host key not trusted; user consent required")
	// ErrKeyChanged — host is in the store but the presented key does not match.
	ErrKeyChanged = errors.New("host key has changed; user consent required to overwrite")
)

// VerifyError is returned by Verify on either of the two sentinel cases
// so the caller can surface fingerprints to the UI without reaching back
// into the store.
type VerifyError struct {
	Err         error
	Host        string
	Port        int
	Fingerprint string // fingerprint of the presented key
	Previous    string // fingerprint already trusted (empty for ErrUnknownHost)
}

func (e *VerifyError) Error() string { return e.Err.Error() }
func (e *VerifyError) Unwrap() error { return e.Err }

// Store is the thread-safe trust store.
type Store struct {
	path string
	mu   sync.RWMutex
	// keyed by canonical "host:port"; multiple entries per host (e.g. RSA + Ed25519
	// rotation overlap) are collapsed to "the most recent one wins" — the UI lets
	// the operator delete and re-add to switch.
	entries map[string]Entry
}

// Open loads the store from path. Missing file is treated as an empty
// store; callers should always Save() at least once after Open() to
// materialise the file with the right perms. A malformed file is a hard
// error so we don't silently lose trust state.
func Open(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]Entry{}}
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
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, e := range f.Hosts {
		s.entries[hostKey(e.Host, e.Port)] = e
	}
	return s, nil
}

// Path returns the on-disk location backing this store.
func (s *Store) Path() string { return s.path }

// List returns every trusted entry in stable order (by host then port).
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.entries))
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

// Verify is the strict check. It returns nil when the (host, port) is
// trusted AND the presented key matches the stored bytes; *VerifyError
// wrapping ErrUnknownHost when the host has never been seen; *VerifyError
// wrapping ErrKeyChanged when a different key is already trusted.
//
// hostname is the OpenSSH-format string the ssh package passes in
// ("host" or "host:port") — Verify normalises it.
func (s *Store) Verify(hostname string, _ net.Addr, presented ssh.PublicKey) error {
	host, port := splitHostPort(hostname)
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[hostKey(host, port)]
	if !ok {
		return &VerifyError{
			Err:         ErrUnknownHost,
			Host:        host,
			Port:        port,
			Fingerprint: ssh.FingerprintSHA256(presented),
		}
	}
	stored, err := base64.StdEncoding.DecodeString(e.KeyBlobB64)
	if err != nil {
		// Corrupt entry — refuse rather than panic.
		return fmt.Errorf("trust store entry for %s is corrupt: %w", hostname, err)
	}
	if string(stored) == string(presented.Marshal()) {
		return nil
	}
	return &VerifyError{
		Err:         ErrKeyChanged,
		Host:        host,
		Port:        port,
		Fingerprint: ssh.FingerprintSHA256(presented),
		Previous:    e.Fingerprint,
	}
}

// Add records (or replaces) the trusted key for (host, port) and persists.
// addedBy is a free-text source tag ("ui-tofu", "ui-manual", "import") so
// the UI can show how the entry got there.
func (s *Store) Add(host string, port int, key ssh.PublicKey, addedBy string) error {
	if host == "" {
		return errors.New("hostkeys.Add: empty host")
	}
	if port == 0 {
		port = 22
	}
	e := Entry{
		Host:        host,
		Port:        port,
		KeyType:     key.Type(),
		KeyBlobB64:  base64.StdEncoding.EncodeToString(key.Marshal()),
		Fingerprint: ssh.FingerprintSHA256(key),
		AddedAt:     time.Now().UTC(),
		AddedBy:     addedBy,
	}
	s.mu.Lock()
	s.entries[hostKey(host, port)] = e
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// Remove deletes (host, port). Returns true if an entry existed.
func (s *Store) Remove(host string, port int) (bool, error) {
	if port == 0 {
		port = 22
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := hostKey(host, port)
	if _, ok := s.entries[k]; !ok {
		return false, nil
	}
	delete(s.entries, k)
	return true, s.saveLocked()
}

// Save flushes the in-memory state to disk. Useful after Open() to
// materialise the file at first run with our 0o600 perms.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked must be called with s.mu held in write mode.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	out := fileFormat{Version: 1, Hosts: make([]Entry, 0, len(s.entries))}
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
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// hostKey is the canonical map key. Hostnames are lowercased; port is
// always present. IPv6 literals keep their bracketed form on the wire
// but we strip the brackets for the map key.
func hostKey(host string, port int) string {
	if port == 0 {
		port = 22
	}
	return strings.ToLower(strings.Trim(host, "[]")) + ":" + strconv.Itoa(port)
}

// splitHostPort parses the hostname string the ssh package hands to a
// HostKeyCallback. That string is "host:port" for non-default ports and
// just "host" for port 22; bracketed IPv6 forms are also possible.
func splitHostPort(hostname string) (string, int) {
	hostname = strings.TrimSpace(hostname)
	if h, p, err := net.SplitHostPort(hostname); err == nil {
		port, _ := strconv.Atoi(p)
		if port == 0 {
			port = 22
		}
		return strings.Trim(h, "[]"), port
	}
	return strings.Trim(hostname, "[]"), 22
}
