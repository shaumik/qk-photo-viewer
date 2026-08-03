package main

import (
	"context"
	"encoding/base64"
	"io/fs"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/shaumik/qk-photo-viewer/internal/remote"
	"github.com/shaumik/qk-photo-viewer/internal/server"
)

// App exposes the culling service to the frontend over the Wails bridge.
// Images travel over the asset server's /api routes, not through here.
type App struct {
	ctx    context.Context
	svc    *server.Service
	assets fs.FS // frontend files, shared with phone remote sessions
	remote *remote.Server
}

func NewApp(assets fs.FS) *App {
	return &App{svc: server.New(), assets: assets}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// State changes reach the desktop webview as Wails runtime events;
	// phone sessions get the same stream over SSE.
	a.svc.SetNotify(func(e server.Event) {
		runtime.EventsEmit(ctx, "qk", e)
	})
}

// PickFolder shows the native folder picker for the card's photo folder.
// Returns "" if the user cancels.
func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose your card's photo folder (e.g. DCIM/100MSDCF)",
	})
}

// OpenFolder scans a shoot folder and returns its photos in shooting order.
func (a *App) OpenFolder(dir string) (server.OpenResult, error) {
	return a.svc.OpenFolder(dir)
}

// Rescan re-reads the current folder after a card comes back.
func (a *App) Rescan() (server.OpenResult, error) {
	return a.svc.Rescan()
}

// FolderPresent reports whether the open folder is still reachable.
func (a *App) FolderPresent() bool {
	return a.svc.FolderPresent()
}

// SetReject marks or unmarks a photo; every connected screen hears about it.
func (a *App) SetReject(id string, rejected bool) error {
	return a.svc.SetReject(id, rejected)
}

// CommitRejects moves the named photos (whole pairs) to the Trash, or the
// on-card rejects folder where Trash isn't possible. Per-file failures are
// reported in the result, not fatal.
func (a *App) CommitRejects(ids []string) (server.CommitResult, error) {
	return a.svc.CommitRejects(ids)
}

// RemoteInfo describes the phone remote session for the QR sheet.
type RemoteInfo struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
	QR      string `json:"qr"` // PNG data URL
}

// StartRemote begins (or reports the already-running) LAN session and
// returns the URL plus a QR code for the phone to scan.
func (a *App) StartRemote() (RemoteInfo, error) {
	if a.remote == nil {
		rs, err := remote.Start(a.svc.Handler(), a.assets)
		if err != nil {
			return RemoteInfo{}, err
		}
		a.remote = rs
	}
	png, err := qrcode.Encode(a.remote.URL, qrcode.Medium, 512)
	if err != nil {
		return RemoteInfo{}, err
	}
	return RemoteInfo{
		Running: true,
		URL:     a.remote.URL,
		QR:      "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	}, nil
}

// OpenMapURL opens a photo's location in the default browser. Restricted
// to the map hosts the info panel links to.
func (a *App) OpenMapURL(url string) {
	if strings.HasPrefix(url, "https://maps.apple.com/") ||
		strings.HasPrefix(url, "https://www.google.com/maps") {
		runtime.BrowserOpenURL(a.ctx, url)
	}
}

// StopRemote ends the LAN session; connected phones lose access.
func (a *App) StopRemote() error {
	if a.remote != nil {
		err := a.remote.Stop()
		a.remote = nil
		return err
	}
	return nil
}
