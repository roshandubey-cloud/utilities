package main

import (
	"context"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"
)

// App is the Wails binding object. The desktop SKU intentionally keeps this
// thin: the entire UI talks to the existing /api/* handler tree via the
// in-process AssetServer.Handler, not via Wails IPC. The methods here are
// reserved for native-only affordances (file dialogs, menu hooks, About box
// helpers) that aren't expressible over HTTP.
type App struct {
	ctx context.Context
	srv *web.Server
}

func NewApp(srv *web.Server) *App {
	return &App{srv: srv}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
	// web.Server.Shutdown is called from main via defer; nothing to do here yet.
}
