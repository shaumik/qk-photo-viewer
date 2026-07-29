package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/shaumik/qk-photo-viewer/internal/library"
)

// App is the Wails-bound backend the frontend talks to.
type App struct {
	ctx    context.Context
	dir    string
	photos []library.Photo
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// OpenFolder scans a shoot folder (e.g. the card's DCIM/100MSDCF) and
// returns its photos, RAW+JPEG pairs merged, in shooting order.
func (a *App) OpenFolder(dir string) ([]library.Photo, error) {
	photos, err := library.Scan(dir)
	if err != nil {
		return nil, err
	}
	a.dir, a.photos = dir, photos
	return photos, nil
}

// CommitRejects moves the named photos (whole pairs) into the rejects folder
// and returns the number of files moved.
func (a *App) CommitRejects(ids []string) (int, error) {
	if a.dir == "" {
		return 0, errors.New("no folder open")
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var rejects, keepers []library.Photo
	for _, p := range a.photos {
		if want[p.ID] {
			rejects = append(rejects, p)
		} else {
			keepers = append(keepers, p)
		}
	}
	moved, err := library.CommitRejects(a.dir, rejects)
	if err != nil {
		return moved, err
	}
	a.photos = keepers
	return moved, nil
}

// Thumbnail and Preview return image data for a photo. Milestone 2: served
// from the ARW's embedded JPEG preview (no RAW decode) with a prefetch ring.
func (a *App) Thumbnail(id string) (string, error) {
	return "", fmt.Errorf("thumbnail pipeline lands in milestone 2 (id %s)", id)
}

func (a *App) Preview(id string) (string, error) {
	return "", fmt.Errorf("preview pipeline lands in milestone 2 (id %s)", id)
}
