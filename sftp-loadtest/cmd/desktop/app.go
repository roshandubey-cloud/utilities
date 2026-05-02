package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"
)

// App is the Wails binding object. The desktop SKU intentionally keeps this
// thin: the entire UI talks to the existing /api/* handler tree via the
// in-process AssetServer.Handler, not via Wails IPC. The methods here are
// reserved for native-only affordances (file dialogs, menu hooks) that
// aren't expressible over HTTP.
type App struct {
	ctx        context.Context
	srv        *web.Server
	reportsDir string
}

func NewApp(srv *web.Server, reportsDir string) *App {
	return &App{srv: srv, reportsDir: reportsDir}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
	// web.Server.Shutdown is called from main via defer; nothing to do here yet.
}

// SaveRunCsv prompts the user for a destination path and copies the run's
// on-disk CSV there. The webview can't render Content-Disposition saves the
// way a real browser does, so the desktop SKU bypasses HTTP entirely and
// works directly with the persisted file.
//
// The frontend (external.js) detects this binding and calls it from clicks
// on /api/report.csv links. Errors are returned as plain strings so the
// frontend can surface them via toast.
func (a *App) SaveRunCsv(runID string) string {
	if runID == "" {
		return "missing run id"
	}
	if a.reportsDir == "" {
		return "no reports directory configured"
	}
	src := persist.CSVPath(a.reportsDir, runID)
	if _, err := os.Stat(src); err != nil {
		return fmt.Sprintf("report not found: %s", err)
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save run CSV",
		DefaultFilename: filepath.Base(src),
		Filters: []runtime.FileFilter{
			{DisplayName: "CSV (*.csv)", Pattern: "*.csv"},
		},
	})
	if err != nil {
		return err.Error()
	}
	if dest == "" {
		return "" // user cancelled
	}
	if err := copyFile(src, dest); err != nil {
		return err.Error()
	}
	return ""
}

// PickDirectory opens the OS-native folder picker and returns the chosen
// absolute path. Empty string when the user cancels. Used by the v0.14
// source/sink Browse buttons to spare operators from hand-typing paths.
func (a *App) PickDirectory(title string) string {
	if title == "" {
		title = "Choose folder"
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
	})
	if err != nil {
		return ""
	}
	return dir
}

// PickFiles opens the OS-native multi-file picker and returns absolute
// paths joined by "\n" so the legacy.js consumer can append directly to
// the textarea. Empty string when the user cancels.
func (a *App) PickFiles(title string) string {
	if title == "" {
		title = "Choose files"
	}
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	out := paths[0]
	for _, p := range paths[1:] {
		out += "\n" + p
	}
	return out
}

func copyFile(src, dest string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer df.Close()
	if _, err := io.Copy(df, sf); err != nil {
		return err
	}
	return df.Close()
}
