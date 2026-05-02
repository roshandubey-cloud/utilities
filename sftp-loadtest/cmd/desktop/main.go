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
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostkeys"
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

	srv := web.NewServer(reportsDir, schedulesDir)
	defer srv.Shutdown()

	// SSH host-key trust store — UI-managed JSON at <dataDir>/hosts.json,
	// the same shape the CLI's `default` branch uses. Opened BEFORE any
	// fallback to ~/.ssh/known_hosts so the desktop UI's Trust panel
	// reads/writes a store the operator can manage in-app instead of
	// pointing them at a file. If the store fails to open we degrade to
	// file mode (legacy behaviour) so trust is still honoured even when
	// the user's home dir is on a read-only volume.
	hostKeyStorePath := filepath.Join(dataDir, "hosts.json")
	store, oerr := hostkeys.Open(hostKeyStorePath)
	if oerr != nil {
		log.Printf("open host-key store %s: %v — falling back to file mode", hostKeyStorePath, oerr)
	} else if err := store.Save(); err != nil {
		log.Printf("init host-key store %s: %v — falling back to file mode", hostKeyStorePath, err)
	} else {
		srv.SetHostKeyStore(store)
		sftpx.SetHostKeyCallback(store.StrictCallback())
		log.Printf("ssh host-key verification: trust store=%s (managed via UI)", hostKeyStorePath)
	}

	// File-mode fallback. Only takes effect when the store wiring above
	// failed; we still ensure ~/.ssh/known_hosts exists so terminal `ssh`
	// keeps working alongside the app.
	knownHostsPath, err := ensureKnownHosts()
	if err != nil {
		log.Printf("known_hosts: %v — host-key verification will be unavailable until resolved", err)
	} else if srv.HostKeyStoreActive() {
		// Store mode is live. Don't bind the file-mode callback (which
		// would make the trust panel read/write the OpenSSH file). The
		// known_hosts file is left alone so terminal ssh still works.
		log.Printf("ssh host-key verification: known_hosts=%s (kept for terminal ssh; UI uses the JSON store)", knownHostsPath)
	} else if err := sftpx.UseKnownHosts(knownHostsPath); err != nil {
		log.Printf("load known_hosts (%s): %v — first-connect TOFU still works via the UI checkbox", knownHostsPath, err)
	} else {
		srv.SetKnownHostsPath(knownHostsPath)
		log.Printf("ssh host-key verification: known_hosts=%s (file-mode fallback)", knownHostsPath)
	}

	// FTPS leaf-cert TOFU store — sibling of the SSH host-key setup.
	// Lives under the same desktop data dir so the operator's UI-managed
	// trust list survives across app launches.
	tlsStorePath := filepath.Join(dataDir, "tls-hosts.json")
	if tlsStore, terr := hostkeys.OpenTLS(tlsStorePath); terr == nil {
		if err := tlsStore.Save(); err != nil {
			log.Printf("init tls trust store %s: %v — FTPS cert TOFU disabled", tlsStorePath, err)
		} else {
			srv.SetTLSStore(tlsStore)
			log.Printf("ftps cert verification: trust store=%s", tlsStorePath)
		}
	} else {
		log.Printf("open tls trust store %s: %v — FTPS cert TOFU disabled", tlsStorePath, terr)
	}

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
			// Keep the OS-native title bar with its built-in close /
			// minimise / maximise triplet rendered by the system — the
			// previous combo of WindowIsTranslucent + Mica + CustomTheme
			// painted over the system control glyphs and left the
			// window with no visible way to quit. The in-app CSS polish
			// (Mica-style gloss inside the WebView) carries the visual
			// language without us touching the OS chrome.
			DisableWindowIcon:    false,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			// System dark theme for the title bar so it doesn't render
			// bright white above our dark shell; the system still owns
			// the close / minimise / maximise buttons and renders them
			// with full visible contrast.
			Theme: windows.Dark,
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
