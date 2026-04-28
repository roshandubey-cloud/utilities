package hostkeys

import (
	"fmt"
	"net"

	"golang.org/x/crypto/ssh"
)

// CapturedKey is what CaptureCallback / TOFUCallback feed back to their
// caller via the "captured" closure. Previous is empty for first-time
// hosts; populated (with the SHA-256 of the previously-trusted key) when
// the presented key differs from one already trusted.
type CapturedKey struct {
	Host        string
	Port        int
	Fingerprint string
	Previous    string
	Changed     bool
}

// StrictCallback returns an ssh.HostKeyCallback that consults the store
// and refuses any unknown or changed key. This is the default callback
// installed at boot — runs use it for every Dial.
func (s *Store) StrictCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return s.Verify(hostname, remote, key)
	}
}

// TOFUCallback returns a callback that:
//   - allows the dial when the (host, port) is already trusted with this key,
//   - on a key-changed mismatch refuses with ErrKeyChanged (no auto-fix),
//   - on a never-seen host ADDS the key to the store and allows the dial.
//
// captured (when non-nil) is invoked on the freshly-added path so the
// caller can echo the fingerprint back to the UI. Used by /api/probe in
// trust-on-first-use mode.
func (s *Store) TOFUCallback(captured func(CapturedKey)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := s.Verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ve *VerifyError
		if !asVerifyError(err, &ve) {
			return err
		}
		if ve.Err == ErrKeyChanged {
			// MITM signal — never auto-fix.
			return err
		}
		// Unknown host: persist + accept.
		if addErr := s.Add(ve.Host, ve.Port, key, "ui-tofu"); addErr != nil {
			return fmt.Errorf("persist trusted host key: %w", addErr)
		}
		if captured != nil {
			captured(CapturedKey{
				Host:        ve.Host,
				Port:        ve.Port,
				Fingerprint: ssh.FingerprintSHA256(key),
			})
		}
		return nil
	}
}

// CaptureCallback returns a callback that strict-checks AND, on either
// failure case, calls the captured closure with both fingerprints (for
// changed keys) before returning the sentinel error. The store is NOT
// mutated — the UI prompts the operator and re-issues the request with
// explicit consent (TOFU for unknown, "remove + TOFU" for changed).
func (s *Store) CaptureCallback(captured func(CapturedKey)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := s.Verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ve *VerifyError
		if !asVerifyError(err, &ve) {
			return err
		}
		if captured != nil {
			captured(CapturedKey{
				Host:        ve.Host,
				Port:        ve.Port,
				Fingerprint: ve.Fingerprint,
				Previous:    ve.Previous,
				Changed:     ve.Err == ErrKeyChanged,
			})
		}
		return err
	}
}

// asVerifyError is a thin errors.As shim — kept inline to avoid pulling in
// errors here just for the alias.
func asVerifyError(err error, target **VerifyError) bool {
	for cur := err; cur != nil; {
		if ve, ok := cur.(*VerifyError); ok {
			*target = ve
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
