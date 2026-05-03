// Package quirks ships a small registry of named "this server is weird in
// this specific way" profiles. v0.16.0 introduced this so operators don't
// have to guess a working dial config for legacy SSH servers (still
// running ssh-rsa) or non-RFC-3659 FTP servers (IIS-style installs that
// reject EPSV / MLSD / UTF-8 NOOP).
//
// A profile is just a small struct of overrides — no behaviour. Dial-path
// code (protocol.go for FTP, sftpx for SSH) reads the active profile via
// Lookup() and applies the relevant fields to the underlying client config.
//
// Adding a new profile:
//  1. add an entry to profileTable below
//  2. surface the name in Names() — that's what the UI dropdown reads
//  3. wire any new override field into the dial-path consumer
package quirks

import (
	"sort"

	"golang.org/x/crypto/ssh"
)

// Profile is a flat bag of dial-time overrides. Zero values mean "library
// default" everywhere — a Profile{Name:"default"} is intentionally a no-op
// so the registry always has a safe fallback.
type Profile struct {
	Name string

	// SSH-side overrides — applied to ssh.ClientConfig.HostKeyAlgorithms
	// and ssh.ClientConfig.Config.KeyExchanges respectively. Nil leaves
	// the library defaults in place.
	SSHHostKeyAlgorithms []string
	SSHKeyExchanges      []string

	// FTP / FTPS overrides — appended as jlaffaye DialOptions. Booleans
	// are off by default (i.e. EPSV / MLSD / UTF-8 stay enabled, which
	// is the modern norm).
	FTPDisableEPSV bool
	FTPDisableMLSD bool
	FTPDisableUTF8 bool
}

// profileTable holds every named profile we ship. Keep this list short
// and well-justified — a dropdown of 40 profiles is worse UX than 5
// profiles plus an "advanced" escape hatch.
var profileTable = map[string]Profile{
	"default": {Name: "default"},

	// openssh-legacy enables the algorithms older sshd installs still
	// require: ssh-rsa host keys (no rsa-sha2-* support) and the
	// diffie-hellman-group14-sha1 KEX. Modern algorithms stay first
	// in the list so a server that supports both still negotiates
	// the modern path.
	"openssh-legacy": {
		Name: "openssh-legacy",
		SSHHostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoRSA, // "ssh-rsa" — legacy fallback
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
		},
		SSHKeyExchanges: []string{
			"curve25519-sha256@libssh.org",
			"curve25519-sha256",
			"ecdh-sha2-nistp256",
			"ecdh-sha2-nistp384",
			"ecdh-sha2-nistp521",
			"diffie-hellman-group14-sha256",
			"diffie-hellman-group14-sha1", // legacy
		},
	},

	// ftp-no-epsv forces PASV instead of EPSV. Some load balancers and
	// firewalls drop EPSV responses; this is the first thing to try
	// when the control channel works but data transfers hang.
	"ftp-no-epsv": {Name: "ftp-no-epsv", FTPDisableEPSV: true},

	// ftp-no-mlsd disables MLSD listings (forces LIST). Some legacy
	// FTP installs return a malformed response to MLSD that jlaffaye's
	// parser chokes on.
	"ftp-no-mlsd": {Name: "ftp-no-mlsd", FTPDisableMLSD: true},

	// ftp-iis bundles the most common IIS-FTP quirks: no EPSV, no
	// MLSD, no UTF-8 NOOP. Saves operators picking three profiles.
	"ftp-iis": {
		Name:           "ftp-iis",
		FTPDisableEPSV: true,
		FTPDisableMLSD: true,
		FTPDisableUTF8: true,
	},
}

// Lookup returns the named profile or the default ("") profile when name
// is empty / unknown. Never returns nil — the dial path can always
// dereference the result.
func Lookup(name string) Profile {
	if name == "" {
		return profileTable["default"]
	}
	if p, ok := profileTable[name]; ok {
		return p
	}
	return profileTable["default"]
}

// Names returns the registered profile names in stable order. Used by
// the UI dropdown and the /api/quirks endpoint.
func Names() []string {
	out := make([]string, 0, len(profileTable))
	for name := range profileTable {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
