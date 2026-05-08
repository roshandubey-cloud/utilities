// Package vault is the OS-independent encrypted secret store for
// sftp-loadtest. Used for credentials that the runner needs at fire
// time (scheduled-run passwords, bastion private keys, FTPS client
// certs) without leaving them as plaintext in JSON files on disk or
// in browser localStorage.
//
// Why not OS keychain (Keychain Services / DPAPI / libsecret)?
//   - We want a single source of truth across every platform we
//     ship: macOS Wails app, Windows Wails app, Linux Wails app,
//     CLI on a server, browser UI driving a remote worker. The
//     OS-keychain story is different on each, with no equivalent in
//     the browser. One file vault works everywhere identically and
//     the audit surface is one package, one format, one threat
//     model — no per-OS implementation drift.
//
// Cryptographic design:
//   - KDF: Argon2id (RFC 9106). Defaults: 64 MiB memory, 3 iterations,
//     parallelism 4. Tuned for ~150-300 ms unlock on a 2024 laptop
//     while pushing well above any sane offline-attack budget.
//   - AEAD: ChaCha20-Poly1305 (RFC 8439). Picked over AES-GCM for
//     constant-time software performance (no AES-NI dependency on
//     the variety of hardware operators run this on) and because
//     the IETF-mandated 96-bit nonce + 128-bit tag matches what
//     `golang.org/x/crypto/chacha20poly1305` exposes natively.
//   - Salt: 16 random bytes per vault file (regenerated on every
//     ChangePassphrase / Save).
//   - Nonce: 12 random bytes per encryption (regenerated on every
//     Save). Nonce reuse is fatal for AEAD; we never reuse the
//     same (key, nonce) pair because Save always rolls a new nonce.
//   - File magic: "SLTV" + 1-byte format version + KDF parameters
//     + salt + nonce + ciphertext. Format is forwards-compatible:
//     a future v2 vault carries a higher version byte and the
//     loader refuses what it doesn't understand instead of silent
//     misinterpretation.
//
// Atomic save: we write to <path>.tmp + fsync + rename, so a crash
// mid-write leaves either the previous good vault or no vault at
// all — never a torn write that locks the operator out.
//
// Threat model: protects against offline disclosure of the vault
// file (laptop theft, careless backup, accidental commit). Does
// NOT protect against an attacker who already has code execution
// inside the running process — once unlocked, secrets are in
// memory and reachable from a debugger / coredump. We zero-out
// derived keys and plaintext buffers on Close() but Go GC and the
// kernel page cache may still leak short-lived copies; that's
// the standard limitation for any in-process secret manager.
package vault

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// File magic — "SLTV" = "sftp-loadtest vault". Operators inspecting
// disks see something identifiable; loaders refuse anything else
// rather than guessing at a corrupted blob.
var fileMagic = [4]byte{'S', 'L', 'T', 'V'}

// Format version. Bump when the on-disk layout changes; older
// loaders will refuse the higher version cleanly.
const fileVersion byte = 1

// KDF identifier — only Argon2id today. Reserved space for future
// algorithms (scrypt, bcrypt, …) without breaking format parsing.
const kdfArgon2id byte = 1

// Default Argon2id parameters. Tuned for ~150-300 ms on a 2024
// laptop. OWASP 2023 recommendations: memory ≥ 19 MiB, t ≥ 2 — we
// run at 64 MiB and t=3 by default.
const (
	defaultMemKiB     uint32 = 64 * 1024
	defaultIterations uint32 = 3
	defaultThreads    uint8  = 4
	derivedKeyLen     uint32 = 32 // ChaCha20-Poly1305 key size
	saltSize                 = 16
)

// Errors that callers may want to type-check (e.g., to show a
// "wrong passphrase" UI vs a "vault corrupted" UI).
var (
	ErrBadMagic       = errors.New("vault: file is not a sftp-loadtest vault (bad magic)")
	ErrUnknownVersion = errors.New("vault: unsupported file version (newer than this binary)")
	ErrUnknownKDF     = errors.New("vault: unsupported KDF id")
	ErrWrongPass      = errors.New("vault: wrong passphrase or corrupted file")
	ErrShortHeader    = errors.New("vault: file truncated (header)")
	ErrEmpty          = errors.New("vault: empty passphrase rejected")
)

// Vault is an unlocked secret store. Goroutine-safe; the mutex
// covers the in-memory map and the metadata fields. Callers should
// hold a single Vault per process and serialise Save() through it.
type Vault struct {
	mu      sync.Mutex
	path    string
	pass    []byte                // raw passphrase bytes; cleared on Close
	secrets map[string]string     // ref → secret
	created time.Time             // first-Save timestamp (audit)
	updated time.Time             // last-Save timestamp (audit)

	// Argon2id parameters used to derive the current key — re-saved
	// verbatim on Save so older vaults can be opened by a binary
	// that has tuned its defaults higher.
	memKiB     uint32
	iterations uint32
	threads    uint8
}

// payload is the JSON structure encrypted inside the file. Keeping
// it explicit (not a free-form map) means we can evolve the
// schema (audit log, derived-secret kinds) without re-encrypting
// every entry.
type payload struct {
	Version int               `json:"version"`
	Created time.Time         `json:"created"`
	Updated time.Time         `json:"updated"`
	Secrets map[string]string `json:"secrets"`
}

// Create initialises a new vault file at path with the given
// passphrase. Refuses to overwrite an existing file — callers
// that genuinely want to replace a vault must os.Remove it first.
func Create(path, passphrase string) (*Vault, error) {
	if passphrase == "" {
		return nil, ErrEmpty
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("vault: file already exists at %s", path)
	}
	v := &Vault{
		path:       path,
		pass:       []byte(passphrase),
		secrets:    map[string]string{},
		created:    time.Now().UTC(),
		updated:    time.Now().UTC(),
		memKiB:     defaultMemKiB,
		iterations: defaultIterations,
		threads:    defaultThreads,
	}
	if err := v.Save(); err != nil {
		return nil, err
	}
	return v, nil
}

// Open reads + decrypts an existing vault. Returns ErrWrongPass
// when the AEAD tag fails verification — caller MUST surface this
// as a "wrong passphrase" message and refuse retries past a small
// budget (rate-limit lives in the web layer, not here).
func Open(path, passphrase string) (*Vault, error) {
	if passphrase == "" {
		return nil, ErrEmpty
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 4+1+1+4+4+1+saltSize+chacha20poly1305.NonceSize {
		return nil, ErrShortHeader
	}

	// Parse header.
	off := 0
	if subtle.ConstantTimeCompare(raw[off:off+4], fileMagic[:]) != 1 {
		return nil, ErrBadMagic
	}
	off += 4
	if raw[off] != fileVersion {
		return nil, ErrUnknownVersion
	}
	off++
	if raw[off] != kdfArgon2id {
		return nil, ErrUnknownKDF
	}
	off++
	memKiB := binary.BigEndian.Uint32(raw[off : off+4]); off += 4
	iters := binary.BigEndian.Uint32(raw[off : off+4]); off += 4
	threads := raw[off]; off++
	salt := raw[off : off+saltSize]; off += saltSize
	nonce := raw[off : off+chacha20poly1305.NonceSize]; off += chacha20poly1305.NonceSize
	ciphertext := raw[off:]

	// Derive key + decrypt.
	key := argon2.IDKey([]byte(passphrase), salt, iters, memKiB, threads, derivedKeyLen)
	defer wipe(key)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("vault: cipher init: %w", err)
	}
	plain, err := aead.Open(nil, nonce, ciphertext, fileMagic[:])
	if err != nil {
		return nil, ErrWrongPass
	}
	defer wipe(plain)

	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		// Plaintext was somehow malformed even though AEAD verified.
		// Treat as corruption rather than wrong-pass so the operator
		// knows the file's bad, not their passphrase.
		return nil, fmt.Errorf("vault: plaintext malformed: %w", err)
	}

	v := &Vault{
		path:       path,
		pass:       []byte(passphrase),
		secrets:    p.Secrets,
		created:    p.Created,
		updated:    p.Updated,
		memKiB:     memKiB,
		iterations: iters,
		threads:    threads,
	}
	if v.secrets == nil {
		v.secrets = map[string]string{}
	}
	return v, nil
}

// Save encrypts the current state and writes it atomically. Always
// rolls a fresh nonce + salt — never reuse the same (key, nonce)
// pair. Callers that do many Sets in a row should batch and call
// Save once at the end; the round-trip cost is dominated by the
// Argon2 derivation, not the AEAD pass.
func (v *Vault) Save() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.path == "" {
		return errors.New("vault: no path configured")
	}
	if len(v.pass) == 0 {
		return ErrEmpty
	}
	v.updated = time.Now().UTC()

	plain, err := json.Marshal(payload{
		Version: 1,
		Created: v.created,
		Updated: v.updated,
		Secrets: v.secrets,
	})
	if err != nil {
		return fmt.Errorf("vault: marshal: %w", err)
	}

	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("vault: salt: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("vault: nonce: %w", err)
	}

	key := argon2.IDKey(v.pass, salt, v.iterations, v.memKiB, v.threads, derivedKeyLen)
	defer wipe(key)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return fmt.Errorf("vault: cipher init: %w", err)
	}
	// Bind the magic into the AEAD as additional data — defends
	// against a (future) attacker that splices a v2 file's body
	// into a v1 header by ensuring the AEAD tag covers the magic.
	ciphertext := aead.Seal(nil, nonce, plain, fileMagic[:])
	wipe(plain)

	// Build the on-disk blob.
	out := make([]byte, 0, 4+1+1+4+4+1+saltSize+chacha20poly1305.NonceSize+len(ciphertext))
	out = append(out, fileMagic[:]...)
	out = append(out, fileVersion)
	out = append(out, kdfArgon2id)
	be4 := make([]byte, 4)
	binary.BigEndian.PutUint32(be4, v.memKiB); out = append(out, be4...)
	binary.BigEndian.PutUint32(be4, v.iterations); out = append(out, be4...)
	out = append(out, v.threads)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	// Atomic write — tmp file + fsync + rename. Crash-safe.
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return fmt.Errorf("vault: mkdir: %w", err)
	}
	tmp := v.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("vault: open tmp: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("vault: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("vault: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("vault: close: %w", err)
	}
	if err := os.Rename(tmp, v.path); err != nil {
		return fmt.Errorf("vault: rename: %w", err)
	}
	return nil
}

// Get returns the secret for ref. Second return is false when no
// secret with that ref is stored.
func (v *Vault) Get(ref string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.secrets[ref]
	return s, ok
}

// Set stores secret under ref, replacing any existing value. Empty
// secrets are rejected — callers wanting to remove an entry must
// use Delete.
func (v *Vault) Set(ref, secret string) error {
	if ref == "" {
		return errors.New("vault: empty ref")
	}
	if secret == "" {
		return errors.New("vault: empty secret (use Delete to remove)")
	}
	v.mu.Lock()
	v.secrets[ref] = secret
	v.mu.Unlock()
	return nil
}

// Delete removes ref. No-op when ref doesn't exist; caller can
// always check Get first if they care.
func (v *Vault) Delete(ref string) {
	v.mu.Lock()
	delete(v.secrets, ref)
	v.mu.Unlock()
}

// List returns all refs sorted lexicographically. Used by migration
// + the UI's "Stored secrets" listing. Doesn't expose values.
func (v *Vault) List() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.secrets))
	for k := range v.secrets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ChangePassphrase re-encrypts the vault under a new passphrase.
// The next Save will roll a fresh salt + nonce as usual; this just
// swaps the key material the next derivation runs against.
func (v *Vault) ChangePassphrase(newPass string) error {
	if newPass == "" {
		return ErrEmpty
	}
	v.mu.Lock()
	wipe(v.pass)
	v.pass = []byte(newPass)
	v.mu.Unlock()
	return v.Save()
}

// Close zeros the in-memory secret material. Defensive — Go's GC
// will eventually reclaim these but Close lets the operator (or a
// session-timeout watcher) actively flush them sooner.
func (v *Vault) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	wipe(v.pass)
	v.pass = nil
	for k := range v.secrets {
		// String content can't be wiped in Go (immutable), but we
		// can drop the references so any future allocation reuses
		// the memory.
		delete(v.secrets, k)
	}
}

// Path returns the on-disk vault path. Useful for the UI to show
// "vault stored at: …" without exposing internals.
func (v *Vault) Path() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.path
}

// Updated returns the last-Save timestamp. Used by the UI's
// "vault last touched: …" indicator and by the migration code to
// decide whether to re-prompt for unlock after a timeout.
func (v *Vault) Updated() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.updated
}

// wipe overwrites a byte slice with zeros. Best-effort: the Go
// compiler can't prove this isn't dead code in every call site,
// but the pattern is what every Go secret manager uses for the
// same defensive reason.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
