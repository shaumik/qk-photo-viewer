// Package server is the culling backend behind the UI: it owns the open
// folder, serves thumbnails and previews over HTTP (the Wails asset server
// on desktop; the same handler backs phone remote sessions in milestone 5),
// and keeps a prefetch ring warm so the next frames are already in memory
// when the user presses →.
package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/shaumik/qk-photo-viewer/internal/fsutil"
	"github.com/shaumik/qk-photo-viewer/internal/library"
	"github.com/shaumik/qk-photo-viewer/internal/preview"
	"github.com/shaumik/qk-photo-viewer/internal/trash"
)

// PhotoDTO is what the frontend sees for each shot.
type PhotoDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Pair string `json:"pair"`
}

// OpenResult describes an opened shoot folder.
type OpenResult struct {
	Dir      string     `json:"dir"`
	ReadOnly bool       `json:"readOnly"` // e.g. the card's lock switch is on
	Photos   []PhotoDTO `json:"photos"`
}

// CommitResult reports what a commit actually did. A commit is per-file and
// never all-or-nothing: photos whose files all moved are in MovedIDs; any
// photo with a failed file stays in the session and its error is listed.
type CommitResult struct {
	MovedIDs []string `json:"movedIds"`
	Dest     string   `json:"dest"` // "Trash", the rejects folder name, or both
	Errors   []string `json:"errors"`
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
func (s *Service) OpenFolder(dir string) (OpenResult, error) {
	photos, err := library.Scan(dir)
	if err != nil {
		return OpenResult{}, err
	}
	res := OpenResult{Dir: dir, ReadOnly: isReadOnly(dir), Photos: make([]PhotoDTO, len(photos))}
	for i, p := range photos {
		res.Photos[i] = PhotoDTO{ID: p.ID, Name: filepath.Base(displayFile(p)), Pair: p.Pair()}
	}
	s.mu.Lock()
	s.dir, s.photos = dir, photos
	s.mu.Unlock()
	return res, nil
}

// Rescan re-reads the current folder — the recovery path after a card was
// pulled and reinserted. The frontend re-applies its reject marks by ID.
func (s *Service) Rescan() (OpenResult, error) {
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()
	if dir == "" {
		return OpenResult{}, errors.New("no folder open")
	}
	return s.OpenFolder(dir)
}

// FolderPresent reports whether the open folder is still reachable —
// false typically means the card was ejected or the reader disconnected.
func (s *Service) FolderPresent() bool {
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir)
	return err == nil
}

// isReadOnly probes writability the honest way: by writing. Catches locked
// SD cards and read-only mounts alike, at open time instead of commit time.
func isReadOnly(dir string) bool {
	f, err := os.CreateTemp(dir, ".qk-writetest-*")
	if err != nil {
		return true
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return false
}

// CommitRejects moves the named photos off the keeper list: into the
// system Trash where available, else into the on-card rejects folder.
// Failures are per-file — whatever could move moved, the rest is reported.
func (s *Service) CommitRejects(ids []string) (CommitResult, error) {
	s.mu.Lock()
	if s.dir == "" {
		s.mu.Unlock()
		return CommitResult{}, errors.New("no folder open")
	}
	dir := s.dir
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var rejects []library.Photo
	for _, p := range s.photos {
		if want[p.ID] {
			rejects = append(rejects, p)
		}
	}
	s.mu.Unlock()

	res := CommitResult{}
	rejectsDir := filepath.Join(dir, library.RejectsDirName)
	trashWorks := true
	trashed, forlorn := 0, 0
	for _, p := range rejects {
		ok := true
		for _, src := range p.Files() {
			if trashWorks {
				if _, err := trash.Put(src); err == nil {
					trashed++
					continue
				} else if errors.Is(err, trash.ErrUnsupported) {
					trashWorks = false // stop retrying trash for this commit
				}
			}
			if _, err := fsutil.MoveInto(rejectsDir, src); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", filepath.Base(src), err))
				ok = false
			} else {
				forlorn++
			}
		}
		if ok {
			res.MovedIDs = append(res.MovedIDs, p.ID)
		}
	}
	switch {
	case trashed > 0 && forlorn > 0:
		res.Dest = "Trash + " + library.RejectsDirName
	case trashed > 0:
		res.Dest = "Trash"
	case forlorn > 0:
		res.Dest = library.RejectsDirName
	}

	moved := map[string]bool{}
	for _, id := range res.MovedIDs {
		moved[id] = true
	}
	s.mu.Lock()
	keepers := s.photos[:0:0]
	for _, p := range s.photos {
		if !moved[p.ID] {
			keepers = append(keepers, p)
		}
	}
	s.photos = keepers
	s.mu.Unlock()
	return res, nil
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
