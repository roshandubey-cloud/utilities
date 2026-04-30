// sftp-loadtest desktop edition.
//
// Architecture: this is the same Go server that powers the CLI/server SKU,
// served through a Wails native window instead of a TCP listener. There is
// no allocated UI port — the embedded webview talks to the in-process
// http.Handler returned by web.Server.Routes() via Wails' AssetServer
// pipeline.
//
// Reusing srv.Routes() verbatim is deliberate: it guarantees byte-identical
// behaviour between the two SKUs. Every feature added to internal/web/
// shows up in both, and there is no second frontend to keep in sync.
package main

import (
	_ "embed"
	"log"
	"os"
	"path/filepath"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/fdlimit"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Embedded brand bitmap — same source as Wails uses for macOS .icns and
// Windows .ico generation, plus passed to Linux at runtime so the GTK
// window manager renders the correct icon in the taskbar / window list.
//go:embed build/appicon.png
var appIcon []byte

func main() {
	fdlimit.Check()

	dataDir, err := desktopDataDir()
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}
	reportsDir := filepath.Join(dataDir, "reports")
	schedulesDir := filepath.Join(dataDir, "schedules")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		log.Fatalf("create reports dir: %v", err)
	}
	if err := os.MkdirAll(schedulesDir, 0o700); err != nil {
		log.Fatalf("create schedules dir: %v", err)
	}

	knownHostsPath, err := ensureKnownHosts()
	if err != nil {
		log.Printf("known_hosts: %v — host-key verification will be unavailable until resolved", err)
	} else if err := sftpx.UseKnownHosts(knownHostsPath); err != nil {
		log.Printf("load known_hosts (%s): %v — first-connect TOFU still works via the UI checkbox", knownHostsPath, err)
	} else {
		log.Printf("ssh host-key verification: known_hosts=%s", knownHostsPath)
	}

	srv := web.NewServer(reportsDir, schedulesDir)
	defer srv.Shutdown()
	srv.SetKnownHostsPath(knownHostsPath)

	// Same security middleware envelope as the CLI default (no -auth-user,
	// no TLS): CSRF + rate-limit + body-size cap. SecurityHeaders and
	// BasicAuth are intentionally skipped — there is no public surface.
	stack := web.BodySizeLimit(srv.Routes())
	stack = web.CSRFGuard(stack)
	stack = web.RateLimit(stack)

	app := NewApp(srv, reportsDir)

	err = wails.Run(&options.App{
		Title:     "SFTP Load Test",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		// Frameless explicitly false so Wails always renders the OS-native
		// title bar with minimize / maximize / close. Earlier desktop bundles
		// shipped on some Windows machines without these affordances; keeping
		// this opt-in setting documented prevents a regression.
		Frameless:    false,
		DisableResize: false,
		AssetServer: &assetserver.Options{
			// No embedded asset bundle — the existing internal/web static
			// FS is served via srv.Routes() through Handler. Keeps the
			// frontend single-sourced.
			Handler: stack,
		},
		// Background matches the workbench dark canvas (--canvas: #0d0e12)
		// so the briefly-empty webview before the page loads doesn't flash
		// the system default. Light-theme users see a one-frame dark
		// flash on first load, which is preferable to a flash of newspaper
		// cream now that the design system is workbench-first.
		BackgroundColour: &options.RGBA{R: 13, G: 14, B: 18, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			// HiddenInset gives a transparent titlebar with traffic
			// lights inset from the window edge, full-size content
			// underneath. Combined with the workbench topbar this
			// produces the unified-titlebar look every native macOS
			// productivity app uses (VS Code, Tower, Linear).
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{
				Title:   "SFTP Load Test",
				Message: "SFTP load testing tool — desktop edition.\nMIT licensed.\nhttps://github.com/roshandubey-cloud/utilities",
				Icon:    appIcon,
			},
		},
		Windows: &windows.Options{
			// Workbench look on Windows: dark frame matches the canvas,
			// keep the close/minimize/maximize triplet visible.
			DisableWindowIcon: false,
			// WebView keeps an opaque body so text rendering doesn't
			// suffer under dark theme; the OS-level Mica/Acrylic
			// backdrop only renders BEHIND the window — see Translucent.
			WebviewIsTransparent: false,
			WindowIsTranslucent:  true,
			Theme:                windows.Dark,
			// Mica gives Win11 a unified blurred chrome that adopts the
			// app background — produces the same "single titlebar +
			// shell" look the macOS HiddenInset titlebar provides on
			// the Mac side. Win10 falls back to flat dark gracefully.
			BackdropType: windows.Mica,
			// CustomTheme paints the OS titlebar in the exact canvas
			// colour so there's no seam where OS chrome ends and our
			// shell topbar begins. Win expects BGR-packed int32
			// (0x00BBGGRR). --canvas = #0d0e12 → R=0x0d, G=0x0e, B=0x12
			// → BGR 0x120e0d.
			CustomTheme: &windows.ThemeSettings{
				DarkModeTitleBar:           0x120e0d,
				DarkModeTitleBarInactive:   0x120e0d,
				DarkModeTitleText:          0xeae8e0, // text-primary-ish, BGR
				DarkModeTitleTextInactive:  0x808080,
				DarkModeBorder:             0x120e0d,
				DarkModeBorderInactive:     0x120e0d,
				LightModeTitleBar:          0xf9f7f6, // matches light --canvas (~#f6f7f9 BGR)
				LightModeTitleBarInactive:  0xf9f7f6,
				LightModeTitleText:         0x2a170f,
				LightModeTitleTextInactive: 0x808080,
				LightModeBorder:            0xf9f7f6,
				LightModeBorderInactive:    0xf9f7f6,
			},
		},
		Linux: &linux.Options{
			Icon: appIcon,
		},
	})
	if err != nil {
		log.Fatalf("wails: %v", err)
	}
}

// desktopDataDir returns the per-user directory where reports and schedules
// live. Falls back to a hidden dir in the user's home if UserConfigDir fails.
func desktopDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		base = filepath.Join(home, ".sftp-loadtest")
	}
	return filepath.Join(base, "sftp-loadtest"), nil
}

// ensureKnownHosts returns the user's OpenSSH known_hosts path, creating an
// empty file at ~/.ssh/known_hosts if neither directory nor file exist yet.
// The TOFU callback in internal/sftpx will append captured server keys here
// when the user ticks "Auto-add server key on first connect" in the probe UI.
func ensureKnownHosts() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(sshDir, "known_hosts")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return "", err
		}
	}
	return path, nil
}
