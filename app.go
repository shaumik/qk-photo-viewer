package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/shaumik/qk-photo-viewer/internal/server"
)

// App exposes the culling service to the frontend over the Wails bridge.
// Images travel over the asset server's /api routes, not through here.
type App struct {
	ctx context.Context
	svc *server.Service
}

func NewApp() *App { return &App{svc: server.New()} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// PickFolder shows the native folder picker for the card's photo folder.
// Returns "" if the user cancels.
func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose your card's photo folder (e.g. DCIM/100MSDCF)",
	})
}

// OpenFolder scans a shoot folder and returns its photos in shooting order.
func (a *App) OpenFolder(dir string) ([]server.PhotoDTO, error) {
	return a.svc.OpenFolder(dir)
}

// CommitRejects moves the named photos (whole pairs) into the rejects
// folder and returns the number of files moved.
func (a *App) CommitRejects(ids []string) (int, error) {
	return a.svc.CommitRejects(ids)
}
