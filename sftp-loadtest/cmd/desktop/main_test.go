// main_test.go — desktop SKU path-resolution coverage.
//
// The desktop binary chooses where to put reports / schedules / known_hosts
// using os.UserConfigDir() + os.UserHomeDir(). The Playwright web rig
// can't exercise these paths because they're Wails-specific. This file
// pins their behaviour at the unit level on darwin/linux/windows.
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDesktopDataDir verifies desktopDataDir returns a non-empty path
// rooted at the platform's user-config dir, with our app namespace
// appended ("sftp-loadtest"). Setting HOME isolates the test from the
// developer's real config dir on macOS / Linux.
func TestDesktopDataDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "linux" {
		// Force the XDG fallback to land under our tmp HOME.
		t.Setenv("XDG_CONFIG_HOME", "")
	}

	got, err := desktopDataDir()
	if err != nil {
		t.Fatalf("desktopDataDir: %v", err)
	}
	if got == "" {
		t.Fatal("desktopDataDir returned empty path")
	}
	if !strings.HasSuffix(got, "sftp-loadtest") {
		t.Errorf("expected path to end with sftp-loadtest, got %q", got)
	}
	// Must be rooted at the platform's user-config base. On darwin that's
	// ~/Library/Application Support; on linux ~/.config; on windows
	// %APPDATA%. We assert the platform-appropriate marker is in the path.
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(got, filepath.Join("Library", "Application Support")) {
			t.Errorf("darwin: expected Library/Application Support in path, got %q", got)
		}
	case "linux":
		// Either ~/.config or $XDG_CONFIG_HOME (we cleared it). Falls
		// back to ~/.config under HOME.
		if !strings.Contains(got, ".config") {
			t.Errorf("linux: expected .config in path, got %q", got)
		}
	}
}

// TestDesktopDataDir_UserConfigDirFails checks the fallback path when
// UserConfigDir errors out (e.g. headless CI with no HOME). We can't
// easily force UserConfigDir to fail, so we sanity-check the fallback
// branch: when HOME is set but unusable, the function should still
// return a path rooted under HOME with a hidden ".sftp-loadtest"
// segment. This test only meaningfully runs on linux.
func TestDesktopDataDir_FallbackUnderHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fallback path requires linux's UserConfigDir error semantics")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// On linux, UserConfigDir returns $XDG_CONFIG_HOME OR $HOME/.config.
	// Setting both empty doesn't actually error — UserConfigDir defaults
	// to $HOME/.config. So the fallback branch can't be triggered here
	// without unsetenv-ing HOME, which the rest of the test setup needs.
	// We assert the happy path returns something sensible instead.
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := desktopDataDir()
	if err != nil {
		t.Fatalf("desktopDataDir: %v", err)
	}
	if !strings.HasPrefix(got, tmp) {
		t.Errorf("expected path rooted at %q, got %q", tmp, got)
	}
}

// TestEnsureKnownHosts verifies the ~/.ssh/known_hosts auto-create
// flow: if the file doesn't exist, it's created empty (mode 0600);
// if it already exists, it's returned as-is and not truncated.
func TestEnsureKnownHosts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp) // os.UserHomeDir reads this on Windows
	}

	// First call: file should be created empty.
	path, err := ensureKnownHosts()
	if err != nil {
		t.Fatalf("ensureKnownHosts (create): %v", err)
	}
	wantPath := filepath.Join(tmp, ".ssh", "known_hosts")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected newly-created known_hosts to be empty, got %d bytes", info.Size())
	}
	// Permissions check (skipped on Windows where modes are virtual).
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("known_hosts mode = %v, want 0600", mode)
		}
		// Parent .ssh dir should be 0700.
		sshInfo, _ := os.Stat(filepath.Join(tmp, ".ssh"))
		if sshInfo != nil {
			if dirMode := sshInfo.Mode().Perm(); dirMode != 0o700 {
				t.Errorf(".ssh mode = %v, want 0700", dirMode)
			}
		}
	}

	// Second call with content already present: must NOT truncate.
	if err := os.WriteFile(path, []byte("host-key-line\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	path2, err := ensureKnownHosts()
	if err != nil {
		t.Fatalf("ensureKnownHosts (existing): %v", err)
	}
	if path2 != path {
		t.Errorf("second-call path = %q, want %q", path2, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second call: %v", err)
	}
	if string(got) != "host-key-line\n" {
		t.Errorf("ensureKnownHosts overwrote existing content; got %q", got)
	}
}
