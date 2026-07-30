// Package server is the culling backend behind the UI: it owns the open
// folder, serves thumbnails and previews over HTTP (the Wails asset server
// on desktop; the same handler backs phone remote sessions in milestone 5),
// and keeps a prefetch ring warm so the next frames are already in memory
// when the user presses →.
package server

import (
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"sync"

	"github.com/shaumik/qk-photo-viewer/internal/library"
	"github.com/shaumik/qk-photo-viewer/internal/preview"
)

// PhotoDTO is what the frontend sees for each shot.
type PhotoDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Pair string `json:"pair"`
}

const (
	previewCacheSize = 24  // ~2–8 MB each: bounded well under typical RAM
	thumbCacheSize   = 512 // tiny EXIF thumbnails
	prefetchAhead    = 3   // frames warmed ahead of the one on screen
	warmQueueSize    = 32
	warmWorkers      = 2
)

type Service struct {
	mu     sync.Mutex
	dir    string
	photos []library.Photo

	thumbs   *preview.Cache
	previews *preview.Cache
	warm     chan string // file paths queued for background prefetch
}

func New() *Service {
	s := &Service{
		thumbs:   preview.NewCache(thumbCacheSize),
		previews: preview.NewCache(previewCacheSize),
		warm:     make(chan string, warmQueueSize),
	}
	for i := 0; i < warmWorkers; i++ {
		go func() {
			for p := range s.warm {
				s.previews.Get(p, func() ([]byte, error) { return preview.Preview(p) })
			}
		}()
	}
	return s
}

// OpenFolder scans a shoot folder and makes it the active one.
func (s *Service) OpenFolder(dir string) ([]PhotoDTO, error) {
	photos, err := library.Scan(dir)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.dir, s.photos = dir, photos
	s.mu.Unlock()

	dtos := make([]PhotoDTO, len(photos))
	for i, p := range photos {
		dtos[i] = PhotoDTO{ID: p.ID, Name: filepath.Base(displayFile(p)), Pair: p.Pair()}
	}
	return dtos, nil
}

// CommitRejects moves the named photos (whole pairs) into the rejects
// folder and drops them from the active list.
func (s *Service) CommitRejects(ids []string) (int, error) {
	s.mu.Lock()
	if s.dir == "" {
		s.mu.Unlock()
		return 0, errors.New("no folder open")
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var rejects, keepers []library.Photo
	for _, p := range s.photos {
		if want[p.ID] {
			rejects = append(rejects, p)
		} else {
			keepers = append(keepers, p)
		}
	}
	dir := s.dir
	s.mu.Unlock()

	moved, err := library.CommitRejects(dir, rejects)
	if err != nil {
		return moved, err
	}
	s.mu.Lock()
	s.photos = keepers
	s.mu.Unlock()
	return moved, nil
}

// Handler serves the image API: /api/thumb/{id} and /api/preview/{id}.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/thumb/", s.serveImage(false))
	mux.HandleFunc("/api/preview/", s.serveImage(true))
	return mux
}

// displayFile is the file a photo is presented as: the camera JPEG when the
// pair has one (full-size, instantly usable), otherwise the RAW, whose
// embedded preview we extract.
func displayFile(p library.Photo) string {
	if p.Jpeg != "" {
		return p.Jpeg
	}
	return p.Raw
}

func (s *Service) serveImage(isPreview bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)

		s.mu.Lock()
		file := ""
		var neighbors []string
		for i, p := range s.photos {
			if p.ID != id {
				continue
			}
			file = displayFile(p)
			if isPreview { // queue the ring around the frame being viewed
				for _, j := range []int{i + 1, i + 2, i + 3, i - 1} {
					if j >= 0 && j < len(s.photos) && j-i <= prefetchAhead {
						neighbors = append(neighbors, displayFile(s.photos[j]))
					}
				}
			}
			break
		}
		s.mu.Unlock()

		if file == "" {
			http.NotFound(w, r)
			return
		}

		var data []byte
		var err error
		if isPreview {
			data, err = s.previews.Get(file, func() ([]byte, error) { return preview.Preview(file) })
			for _, nb := range neighbors {
				select {
				case s.warm <- nb:
				default: // queue full: skip, the ring catches up on the next request
				}
			}
		} else {
			data, err = s.thumbs.Get(file, func() ([]byte, error) { return preview.Thumb(file) })
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=300")
		w.Write(data)
	}
}
