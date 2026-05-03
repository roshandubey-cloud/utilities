package quirks

import (
	"sort"
	"testing"
)

func TestLookup_DefaultIsSafeFallback(t *testing.T) {
	for _, name := range []string{"", "default", "totally-not-a-profile"} {
		p := Lookup(name)
		if len(p.SSHHostKeyAlgorithms) != 0 || len(p.SSHKeyExchanges) != 0 ||
			p.FTPDisableEPSV || p.FTPDisableMLSD || p.FTPDisableUTF8 {
			t.Fatalf("Lookup(%q): expected zero overrides, got %+v", name, p)
		}
	}
}

func TestLookup_OpenSSHLegacyEnablesSshRsa(t *testing.T) {
	p := Lookup("openssh-legacy")
	hasSshRsa := false
	for _, a := range p.SSHHostKeyAlgorithms {
		if a == "ssh-rsa" {
			hasSshRsa = true
			break
		}
	}
	if !hasSshRsa {
		t.Fatalf("openssh-legacy: expected ssh-rsa in HostKeyAlgorithms, got %v", p.SSHHostKeyAlgorithms)
	}
	hasLegacyKex := false
	for _, k := range p.SSHKeyExchanges {
		if k == "diffie-hellman-group14-sha1" {
			hasLegacyKex = true
			break
		}
	}
	if !hasLegacyKex {
		t.Fatalf("openssh-legacy: expected dh-group14-sha1 in KeyExchanges, got %v", p.SSHKeyExchanges)
	}
}

func TestLookup_FTPProfilesSetCorrectFlags(t *testing.T) {
	cases := []struct {
		name string
		want Profile
	}{
		{"ftp-no-epsv", Profile{FTPDisableEPSV: true}},
		{"ftp-no-mlsd", Profile{FTPDisableMLSD: true}},
		{"ftp-iis", Profile{FTPDisableEPSV: true, FTPDisableMLSD: true, FTPDisableUTF8: true}},
	}
	for _, tc := range cases {
		p := Lookup(tc.name)
		if p.FTPDisableEPSV != tc.want.FTPDisableEPSV ||
			p.FTPDisableMLSD != tc.want.FTPDisableMLSD ||
			p.FTPDisableUTF8 != tc.want.FTPDisableUTF8 {
			t.Errorf("%s: got EPSV=%v MLSD=%v UTF8=%v, want EPSV=%v MLSD=%v UTF8=%v",
				tc.name, p.FTPDisableEPSV, p.FTPDisableMLSD, p.FTPDisableUTF8,
				tc.want.FTPDisableEPSV, tc.want.FTPDisableMLSD, tc.want.FTPDisableUTF8)
		}
	}
}

func TestNames_StableAndIncludesDefault(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("Names() returned empty list")
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("Names() not sorted: %v", names)
	}
	hasDefault := false
	for _, n := range names {
		if n == "default" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		t.Errorf("Names() missing 'default': %v", names)
	}
}
