package config

import (
	"strings"
	"testing"
)

// TestParseUsersCSV_RequiresPattern pins the upload-side contract:
// upload generators mint filenames from per-user patterns, so a row
// with no pattern column is a config error.
func TestParseUsersCSV_RequiresPattern(t *testing.T) {
	if _, err := ParseUsersCSV(strings.NewReader("u1,pp\n")); err == nil {
		t.Fatal("ParseUsersCSV must reject 2-column rows for upload users")
	}
	users, err := ParseUsersCSV(strings.NewReader("u1,pp,inv-*\nu2,pp,rec-*,doc-*\n"))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if len(users) != 2 || users[0].Username != "u1" || len(users[0].Patterns) != 1 || users[1].Patterns[1] != "doc-*" {
		t.Fatalf("unexpected parse: %+v", users)
	}
}

// TestParseDownloadUsersCSV_NoPatternRequired pins the v0.19.15 fix:
// download users have no filename pattern (the poller pulls whatever
// the server placed in the folder), so the CSV must accept
// `username,password` rows AND still tolerate legacy `username,password,*`
// rows for back-compat with saved configs.
func TestParseDownloadUsersCSV_NoPatternRequired(t *testing.T) {
	users, err := ParseDownloadUsersCSV(strings.NewReader("dl1,pp\ndl2,pp\n"))
	if err != nil {
		t.Fatalf("2-column download CSV must parse, got %v", err)
	}
	if len(users) != 2 || users[0].Username != "dl1" || users[1].Username != "dl2" {
		t.Fatalf("unexpected parse: %+v", users)
	}
	if len(users[0].Patterns) != 0 {
		t.Errorf("download user should have no patterns; got %v", users[0].Patterns)
	}
}

// TestParseDownloadUsersCSV_LegacyPatternRowsStillParse pins back-compat
// for saved configs that carry the historical `dl1,pp,*` shape — they
// must still load without error so existing dashboards and saved start
// JSONs don't break on upgrade.
func TestParseDownloadUsersCSV_LegacyPatternRowsStillParse(t *testing.T) {
	users, err := ParseDownloadUsersCSV(strings.NewReader("dl1,pp,*\ndl2,pp,*\n"))
	if err != nil {
		t.Fatalf("legacy 3-column download CSV must parse, got %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	// Patterns from the legacy column are accepted but never read by the
	// download path — capture is fine, requirement is removed.
	if users[0].Patterns[0] != "*" {
		t.Errorf("expected captured pattern '*', got %v", users[0].Patterns)
	}
}

// TestParseDownloadUsersCSV_RejectsEmptyUsername guards against a
// blank username slipping through — the SFTP/FTP login layer needs it,
// and an empty string here would fail far away from the textarea with
// a much less helpful error.
func TestParseDownloadUsersCSV_RejectsEmptyUsername(t *testing.T) {
	if _, err := ParseDownloadUsersCSV(strings.NewReader(",pp\n")); err == nil {
		t.Fatal("blank username must fail validation")
	}
}
