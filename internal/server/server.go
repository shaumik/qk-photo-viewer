// Package server is the culling backend behind the UI: it owns the open
// folder and the reject marks (the single source of truth, so every
// connected screen agrees), serves thumbnails and previews over HTTP, and
// keeps a prefetch ring warm so the next frames are already in memory when
// the user presses →. The same Handler backs the desktop webview's asset
// server and phone remote sessions over the LAN.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

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

// OpenResult describes the open shoot folder and its current cull state.
type OpenResult struct {
	Dir      string     `json:"dir"`
	ReadOnly bool       `json:"readOnly"` // e.g. the card's lock switch is on
	Photos   []PhotoDTO `json:"photos"`
	Rejected []string   `json:"rejected"` // IDs currently marked, so late joiners sync up
}

// CommitResult reports what a commit actually did. A commit is per-file and
// never all-or-nothing: photos whose files all moved are in MovedIDs; any
// photo with a failed file stays in the session and its error is listed.
type CommitResult struct {
	MovedIDs []string `json:"movedIds"`
	Dest     string   `json:"dest"` // "Trash", the rejects folder name, or both
	Errors   []string `json:"errors"`
}

// Event is a state change broadcast to every connected screen — the desktop
// webview and any phone remote sessions.
type Event struct {
	Type     string   `json:"type"` // "reject" | "commit" | "open"
	ID       string   `json:"id,omitempty"`
	Rejected bool     `json:"rejected,omitempty"`
	MovedIDs []string `json:"movedIds,omitempty"`
	Dest     string   `json:"dest,omitempty"`
}

const (
	previewCacheSize = 24  // ~2–8 MB each: bounded well under typical RAM
	thumbCacheSize   = 512 // tiny EXIF thumbnails
	prefetchAhead    = 3   // frames warmed ahead of the one on screen
	warmQueueSize    = 32
	warmWorkers      = 2
	ssePingInterval  = 25 * time.Second
)

type Service struct {
	mu       sync.Mutex
	dir      string
	readOnly bool
	photos   []library.Photo
	rejected map[string]bool
	subs     map[chan Event]struct{}
	notify   func(Event) // extra sink, e.g. Wails runtime events for the desktop webview

	thumbs   *preview.Cache
	previews *preview.Cache
	warm     chan string // file paths queued for background prefetch
}

func New() *Service {
	s := &Service{
		rejected: map[string]bool{},
		subs:     map[chan Event]struct{}{},
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

/* ---------- event fan-out ---------- */

// Subscribe returns a channel of state changes and a cancel function.
// Slow consumers never block the culler: sends are dropped when a
// subscriber's buffer is full (SSE clients resync via /api/photos).
func (s *Service) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// SetNotify installs an extra synchronous event sink (the desktop webview
// bridge). Call before events start flowing.
func (s *Service) SetNotify(fn func(Event)) {
	s.mu.Lock()
	s.notify = fn
	s.mu.Unlock()
}

func (s *Service) emit(e Event) {
	s.mu.Lock()
	fn := s.notify
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
	s.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

/* ---------- session state ---------- */

// OpenFolder scans a shoot folder and makes it the active one. Reject
// marks reset — it's a new session.
func (s *Service) OpenFolder(dir string) (OpenResult, error) {
	photos, err := library.Scan(dir)
	if err != nil {
		return OpenResult{}, err
	}
	ro := isReadOnly(dir)
	s.mu.Lock()
	s.dir, s.photos, s.readOnly = dir, photos, ro
	s.rejected = map[string]bool{}
	res := s.stateLocked()
	s.mu.Unlock()
	s.emit(Event{Type: "open"})
	return res, nil
}

// State reports the current session for late-joining screens.
func (s *Service) State() OpenResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) stateLocked() OpenResult {
	res := OpenResult{Dir: s.dir, ReadOnly: s.readOnly,
		Photos: make([]PhotoDTO, len(s.photos)), Rejected: []string{}}
	for i, p := range s.photos {
		res.Photos[i] = PhotoDTO{ID: p.ID, Name: filepath.Base(displayFile(p)), Pair: p.Pair()}
		if s.rejected[p.ID] {
			res.Rejected = append(res.Rejected, p.ID)
		}
	}
	return res
}

// SetReject marks or unmarks a photo and tells every connected screen.
func (s *Service) SetReject(id string, rejected bool) error {
	s.mu.Lock()
	found := false
	for _, p := range s.photos {
		if p.ID == id {
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("unknown photo %q", id)
	}
	if rejected {
		s.rejected[id] = true
	} else {
		delete(s.rejected, id)
	}
	s.mu.Unlock()
	s.emit(Event{Type: "reject", ID: id, Rejected: rejected})
	return nil
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
	for id := range moved {
		delete(s.rejected, id)
	}
	s.mu.Unlock()
	if len(res.MovedIDs) > 0 {
		s.emit(Event{Type: "commit", MovedIDs: res.MovedIDs, Dest: res.Dest})
	}
	return res, nil
}

/* ---------- HTTP API ---------- */

// Handler serves the full culling API: images (/api/thumb, /api/preview),
// session state (/api/photos), actions (/api/reject, /api/commit), and the
// live event stream (/api/events). Identical for desktop and phone.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/thumb/", s.serveImage(false))
	mux.HandleFunc("/api/preview/", s.serveImage(true))
	mux.HandleFunc("/api/photos", s.servePhotos)
	mux.HandleFunc("/api/meta/", s.serveMeta)
	mux.HandleFunc("/api/reject", s.serveReject)
	mux.HandleFunc("/api/commit", s.serveCommit)
	mux.HandleFunc("/api/events", s.serveEvents)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Service) servePhotos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.State())
}

// serveMeta returns a photo's shooting metadata (camera, exposure, GPS).
// Files without EXIF return an empty object — that's normal, not an error.
func (s *Service) serveMeta(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	s.mu.Lock()
	file := ""
	for _, p := range s.photos {
		if p.ID == id {
			file = displayFile(p)
			break
		}
	}
	s.mu.Unlock()
	if file == "" {
		http.NotFound(w, r)
		return
	}
	m, err := preview.ReadMeta(file)
	if err != nil {
		m = preview.Meta{} // unreadable right now: show nothing rather than fail
	}
	writeJSON(w, m)
}

func (s *Service) serveReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Rejected bool   `json:"rejected"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.SetReject(req.ID, req.Rejected); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) serveCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.CommitRejects(req.IDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, res)
}

// serveEvents streams state changes as Server-Sent Events — chosen over
// WebSockets because it's plain HTTP (works through the Wails asset server
// and any LAN setup) and EventSource reconnects by itself.
func (s *Service) serveEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, cancel := s.Subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()
	for {
		select {
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
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
