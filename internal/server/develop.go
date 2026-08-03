package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/shaumik/qk-photo-viewer/internal/develop"
	"github.com/shaumik/qk-photo-viewer/internal/edits"
	"github.com/shaumik/qk-photo-viewer/internal/lens"
	"github.com/shaumik/qk-photo-viewer/internal/library"
	"github.com/shaumik/qk-photo-viewer/internal/preview"
	"github.com/shaumik/qk-photo-viewer/internal/raw"
)

// Editing splits into an expensive half and a cheap one. Decoding a RAW,
// demosaicing it and getting it into linear sRGB takes a moment and
// depends only on the file. Applying an edit to the result is fast and
// depends only on the sliders. So the Scene is built once per photo and
// cached, and every slider move re-renders it — which is what makes
// dragging feel like dragging rather than waiting.

const (
	sceneCacheSize  = 3  // ~16 MB each at preview size
	renderCacheSize = 12 // developed JPEGs, keyed by photo and edit
)

/* ---------- scene cache ---------- */

type sceneEntry struct {
	done  chan struct{}
	scene *develop.Scene
	err   error
}

// sceneCache is a tiny LRU with in-flight deduplication: two screens
// asking for the same photo at once decode it once.
type sceneCache struct {
	mu      sync.Mutex
	cap     int
	entries map[string]*sceneEntry
	order   []string
}

func newSceneCache(capacity int) *sceneCache {
	return &sceneCache{cap: capacity, entries: map[string]*sceneEntry{}}
}

func (c *sceneCache) get(key string, load func() (*develop.Scene, error)) (*develop.Scene, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.touchLocked(key)
		c.mu.Unlock()
		<-e.done
		return e.scene, e.err
	}
	e := &sceneEntry{done: make(chan struct{})}
	c.entries[key] = e
	c.order = append(c.order, key)
	c.evictLocked()
	c.mu.Unlock()

	e.scene, e.err = load()
	close(e.done)
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			c.removeLocked(key)
		}
		c.mu.Unlock()
	}
	return e.scene, e.err
}

func (c *sceneCache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(append(c.order[:i], c.order[i+1:]...), key)
			return
		}
	}
}

func (c *sceneCache) removeLocked(key string) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *sceneCache) evictLocked() {
	for len(c.order) > c.cap {
		victim := ""
		for _, k := range c.order {
			select {
			case <-c.entries[k].done:
				victim = k
			default:
			}
			if victim != "" {
				break
			}
		}
		if victim == "" {
			return
		}
		c.removeLocked(victim)
	}
}

func (c *sceneCache) clear() {
	c.mu.Lock()
	c.entries = map[string]*sceneEntry{}
	c.order = nil
	c.mu.Unlock()
}

/* ---------- building scenes ---------- */

// sceneFor develops a photo to the point just before the sliders: linear
// sRGB, camera colour applied, the right way up.
//
// The RAW is what we want, for the headroom and the colour latitude. When
// it cannot be decoded — a compression scheme we do not handle, a make we
// have no decoder for — the camera's rendered preview stands in. Editing
// an 8-bit preview is worse than editing sensor data, and much better
// than refusing to edit at all; Scene.FromRAW records which one happened
// so the UI can say so.
func sceneFor(p library.Photo) (*develop.Scene, error) {
	if p.Raw != "" {
		im, err := raw.Decode(p.Raw)
		if err == nil {
			return develop.FromRAWImage(im, develop.PreviewMaxDim), nil
		}
		if !errors.Is(err, raw.ErrUnsupported) {
			return nil, err
		}
	}
	return sceneFromPreview(displayFile(p), develop.PreviewMaxDim)
}

func sceneFromPreview(file string, maxDim int) (*develop.Scene, error) {
	data, err := preview.Preview(file)
	if err != nil {
		return nil, err
	}
	// The preview carries its own orientation tag, and it is the one that
	// describes those pixels. Fall back to the container's.
	o, ok := develop.OrientationOfJPEG(data)
	if !ok {
		o = develop.OrientationOf(file)
	}
	sc, err := develop.FromJPEGBytes(data, o, maxDim)
	if err != nil {
		return nil, err
	}
	return sc.WithISO(develop.ISOOf(file)), nil
}

// fullScene is the export path: every pixel the sensor recorded, through
// the slow demosaic.
func fullScene(p library.Photo) (*develop.Scene, error) {
	if p.Raw != "" {
		im, err := raw.Decode(p.Raw)
		if err == nil {
			return develop.FromRAWImage(im, 0), nil
		}
		if !errors.Is(err, raw.ErrUnsupported) {
			return nil, err
		}
	}
	return sceneFromPreview(displayFile(p), 0)
}

/* ---------- session plumbing ---------- */

// editFile is where a photo's edit is filed: the RAW when there is one, so
// that a pair keeps one edit whichever half is on screen.
func editFile(p library.Photo) string {
	if p.Raw != "" {
		return p.Raw
	}
	return p.Jpeg
}

func (s *Service) photoByID(id string) (library.Photo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.photos {
		if p.ID == id {
			return p, true
		}
	}
	return library.Photo{}, false
}

// editFor returns a photo's current edit, empty if it has none.
func (s *Service) editFor(p library.Photo) develop.Edit {
	s.mu.Lock()
	st := s.edits
	s.mu.Unlock()
	if st == nil {
		return develop.Edit{}
	}
	e, _ := st.Get(editFile(p))
	return e
}

func (s *Service) editStore() *edits.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edits
}

// editTag is a short stable name for an edit, used to key the render cache
// and to let the browser cache a developed frame until it changes.
func editTag(e develop.Edit) string {
	b, _ := json.Marshal(e)
	h := fnv.New64a()
	h.Write(b)
	return strconv.FormatUint(h.Sum64(), 36)
}

/* ---------- API ---------- */

// DevelopInfo is what the edit panel needs to know about a photo.
type DevelopInfo struct {
	ID     string       `json:"id"`
	Edit   develop.Edit `json:"edit"`
	Edited bool         `json:"edited"`
	Tag    string       `json:"tag"`
	Source string       `json:"source"` // "raw" or "preview"
	Camera string       `json:"camera,omitempty"`
	// Headroom is how many stops of detail sit above white, waiting for
	// the highlight slider. Zero on the preview fallback.
	Headroom float64 `json:"headroom"`
	// ApproxColor warns that the colour matrix is a generic stand-in.
	ApproxColor bool `json:"approxColor,omitempty"`
	Width       int  `json:"width"`
	Height      int  `json:"height"`

	// Lens names what took the shot, and LensLearned says a correction for
	// it was already known and has been applied.
	Lens        string `json:"lens,omitempty"`
	LensLearned bool   `json:"lensLearned,omitempty"`

	// Synced counts the photos a sync just reached.
	Synced int `json:"synced,omitempty"`
}

// infoFor describes a photo and an edit together. Every reply that carries
// a DevelopInfo goes through here, so the panel is never told half the
// story — an answer missing the source would have it announce that the RAW
// could not be decoded, which would be a lie told confidently.
func (s *Service) infoFor(p library.Photo, e develop.Edit) DevelopInfo {
	info := DevelopInfo{ID: p.ID, Edit: e, Edited: !e.IsZero(), Tag: editTag(e)}
	sc, err := s.scenes.get(sceneKey(p), func() (*develop.Scene, error) { return sceneFor(p) })
	if err != nil {
		return info // the caller's own error handling covers the rest
	}
	info.Source = "preview"
	if sc.FromRAW {
		info.Source = "raw"
	}
	info.Camera, info.Headroom = sc.Camera, sc.Headroom
	info.ApproxColor, info.Width, info.Height = sc.ApproxColor, sc.W, sc.H
	if name, focal := s.lensOf(p); name != "" {
		info.Lens = name
		if focal > 0 {
			info.Lens = fmt.Sprintf("%s at %gmm", name, math.Round(focal))
		}
		_, info.LensLearned = s.lenses.Get(lens.Key(name, focal))
	}
	// Width and Height describe the corrected frame before the crop —
	// straightening and lens correction keep the frame's size, so this is
	// the shape the crop rectangle is measured against and drawn on.
	return info
}

// lensOf reports the lens a photo was taken with. Reading tags is cheap
// but not free and the answer never changes, so it is remembered for the
// session.
func (s *Service) lensOf(p library.Photo) (string, float64) {
	key := editFile(p)
	s.mu.Lock()
	if v, ok := s.lensCache[key]; ok {
		s.mu.Unlock()
		return v.name, v.focal
	}
	s.mu.Unlock()

	name, focal := develop.LensOf(displayFile(p))
	s.mu.Lock()
	if s.lensCache == nil {
		s.lensCache = map[string]lensID{}
	}
	s.lensCache[key] = lensID{name, focal}
	s.mu.Unlock()
	return name, focal
}

// lensProfileFor returns what has been learned about this photo's lens.
func (s *Service) lensProfileFor(p library.Photo) (lens.Profile, bool) {
	name, focal := s.lensOf(p)
	return s.lenses.Get(lens.Key(name, focal))
}

// rememberLens files the geometry half of an edit under the lens that took
// the photo, so the next shot on that lens at that focal length starts
// already corrected. This is the whole point of the lens store: a lens
// distorts the same way every time, so it should only be fixed once.
func (s *Service) rememberLens(p library.Photo, e develop.Edit) {
	name, focal := s.lensOf(p)
	key := lens.Key(name, focal)
	if key == "" {
		return
	}
	s.lenses.Set(key, lens.Profile{Distortion: e.Distortion, Vignette: e.Vignette})
}

func (s *Service) serveDevelopInfo(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	p, ok := s.photoByID(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.scenes.get(sceneKey(p), func() (*develop.Scene, error) { return sceneFor(p) }); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.infoFor(p, s.editFor(p)))
}

func sceneKey(p library.Photo) string { return editFile(p) }

// serveDevelop renders a photo with its current edit. The ?v= tag in the
// query is not read — it is there so a changed edit is a different URL and
// the browser stops using the frame it already has. (It is not ?t=, which
// the LAN remote server reserves for its session token.)
func (s *Service) serveDevelop(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	p, ok := s.photoByID(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	maxDim := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("w")); err == nil && v > 0 {
		maxDim = v
	}
	e := s.editFor(p)
	if r.URL.Query().Get("original") == "1" {
		e = develop.Edit{} // the before half of a before/after
	}
	// The crop tool draws its rectangle on the frame it is cutting from,
	// which means rendering everything except the cut.
	uncropped := r.URL.Query().Get("uncropped") == "1"

	key := fmt.Sprintf("%s|%s|%d|%v", sceneKey(p), editTag(e), maxDim, uncropped)
	data, err := s.renders.Get(key, func() ([]byte, error) {
		sc, err := s.scenes.get(sceneKey(p), func() (*develop.Scene, error) { return sceneFor(p) })
		if err != nil {
			return nil, err
		}
		small := sc.Downscaled(maxDim)
		var img *image.RGBA
		if uncropped {
			img = develop.RenderUncropped(small, e)
		} else {
			img = develop.Render(small, e)
		}
		return develop.EncodeJPEG(img, 90, nil)
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write(data)
}

// SetEdit records a photo's edit and tells every connected screen.
//
// hold means the value is still moving — a slider under a finger. Those
// are kept in memory and not written out, because a drag makes dozens of
// values a second and a memory card should not see any of them; the one
// the user stops on arrives as an ordinary Set. Nor are they broadcast:
// a phone does not need to watch a laptop's slider travel.
func (s *Service) SetEdit(id string, e develop.Edit, hold bool) (DevelopInfo, error) {
	p, ok := s.photoByID(id)
	if !ok {
		return DevelopInfo{}, fmt.Errorf("unknown photo %q", id)
	}
	st := s.editStore()
	if st == nil {
		return DevelopInfo{}, errors.New("no folder open")
	}
	e = e.Clamp()
	info := s.infoFor(p, e)
	if hold {
		st.Hold(editFile(p), e)
		return info, nil
	}
	s.rememberLens(p, e)
	// A failed write is worth reporting but not worth refusing the edit
	// over: the value is live in memory either way.
	saveErr := st.Set(editFile(p), e)
	s.emit(Event{Type: "edit", ID: id, Edit: &e, Tag: info.Tag})
	return info, saveErr
}

// AutoDevelop works out an edit for a photo and applies it.
func (s *Service) AutoDevelop(id string) (DevelopInfo, error) {
	p, ok := s.photoByID(id)
	if !ok {
		return DevelopInfo{}, fmt.Errorf("unknown photo %q", id)
	}
	sc, err := s.scenes.get(sceneKey(p), func() (*develop.Scene, error) { return sceneFor(p) })
	if err != nil {
		return DevelopInfo{}, err
	}
	e := develop.Auto(sc)
	// Auto reads the picture; the lens correction is a property of the
	// glass, already worked out, and belongs on top.
	if prof, ok := s.lensProfileFor(p); ok {
		e.Distortion, e.Vignette = prof.Distortion, prof.Vignette
	}
	return s.SetEdit(id, e, false)
}

// ResetEdit puts a photo back to as shot.
func (s *Service) ResetEdit(id string) (DevelopInfo, error) {
	p, ok := s.photoByID(id)
	if !ok {
		return DevelopInfo{}, fmt.Errorf("unknown photo %q", id)
	}
	if st := s.editStore(); st != nil {
		if err := st.Reset(editFile(p)); err != nil {
			return DevelopInfo{}, err
		}
	}
	info := s.infoFor(p, develop.Edit{})
	s.emit(Event{Type: "edit", ID: id, Edit: &develop.Edit{}, Tag: info.Tag})
	return info, nil
}

func (s *Service) serveEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     string        `json:"id"`
		Edit   *develop.Edit `json:"edit"`
		Action string        `json:"action"` // "", "auto", "reset" or "sync"
		Hold   bool          `json:"hold"`   // the slider is still moving
		IDs    []string      `json:"ids"`    // sync targets; empty means every other photo
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var (
		info DevelopInfo
		err  error
	)
	switch req.Action {
	case "auto":
		info, err = s.AutoDevelop(req.ID)
	case "reset":
		info, err = s.ResetEdit(req.ID)
	case "sync":
		var applied []string
		applied, err = s.SyncLook(req.ID, req.IDs)
		if err == nil {
			p, _ := s.photoByID(req.ID)
			info = s.infoFor(p, s.editFor(p))
			info.Synced = len(applied)
		}
	default:
		if req.Edit == nil {
			http.Error(w, "no edit supplied", http.StatusBadRequest)
			return
		}
		info, err = s.SetEdit(req.ID, *req.Edit, req.Hold)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

// SyncLook copies one photo's look onto others: tone, colour and lens
// correction, but not framing. A shoot developed one frame at a time looks
// like a shoot developed one frame at a time, and this is the step that
// stops that being the only option.
//
// Each target keeps its own crop and straightening, because those are
// decisions about one photograph rather than about the light.
func (s *Service) SyncLook(fromID string, toIDs []string) ([]string, error) {
	from, ok := s.photoByID(fromID)
	if !ok {
		return nil, fmt.Errorf("unknown photo %q", fromID)
	}
	st := s.editStore()
	if st == nil {
		return nil, errors.New("no folder open")
	}
	look := s.editFor(from)

	want := map[string]bool{}
	for _, id := range toIDs {
		want[id] = true
	}
	s.mu.Lock()
	targets := make([]library.Photo, 0, len(want))
	for _, p := range s.photos {
		if p.ID != fromID && (len(toIDs) == 0 || want[p.ID]) {
			targets = append(targets, p)
		}
	}
	s.mu.Unlock()

	applied := make([]string, 0, len(targets))
	var firstErr error
	for _, p := range targets {
		e := s.editFor(p).WithLookOf(look)
		if err := st.Set(editFile(p), e); err != nil && firstErr == nil {
			firstErr = err
		}
		s.rememberLens(p, e)
		applied = append(applied, p.ID)
	}
	if len(applied) > 0 {
		// One event for the batch: a shoot of eight hundred would otherwise
		// be eight hundred messages to every connected screen.
		s.emit(Event{Type: "sync", ID: fromID, SyncedIDs: applied, Tag: editTag(look)})
	}
	return applied, firstErr
}

/* ---------- export ---------- */

// ExportResult reports where a photo ended up.
type ExportResult struct {
	Path  string `json:"path"`
	Dir   string `json:"dir"`
	Count int    `json:"count"`
}

// ExportOne develops a photo at full resolution and writes a JPEG. dest
// empty means the default folder beside the shoot.
func (s *Service) ExportOne(id, dest string) (ExportResult, error) {
	p, ok := s.photoByID(id)
	if !ok {
		return ExportResult{}, fmt.Errorf("unknown photo %q", id)
	}
	dir, err := s.exportDir(dest)
	if err != nil {
		return ExportResult{}, err
	}
	out, err := s.exportPhoto(p, dir)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Path: out, Dir: dir, Count: 1}, nil
}

// exportPhoto is the whole job for one photo: decode, demosaic at full
// resolution, develop, encode, write.
func (s *Service) exportPhoto(p library.Photo, dir string) (string, error) {
	sc, err := fullScene(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(displayFile(p)), err)
	}
	// The Scene is single-use here, so render into its own buffer rather
	// than allocating a second one the size of a full frame.
	img := develop.RenderInPlace(sc, s.editFor(p))
	data, err := develop.EncodeJPEG(img, develop.DefaultQuality,
		develop.ExifFor(displayFile(p), sc.W, sc.H))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(editFile(p))
	name = name[:len(name)-len(filepath.Ext(name))] + ".jpg"
	out := filepath.Join(dir, name)
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// exportDir resolves where exports go. Beside the shoot by default, so
// they travel with it; somewhere writable when the card is not.
func (s *Service) exportDir(dest string) (string, error) {
	if dest != "" {
		return dest, nil
	}
	s.mu.Lock()
	dir, ro := s.dir, s.readOnly
	s.mu.Unlock()
	if dir == "" {
		return "", errors.New("no folder open")
	}
	if !ro {
		return filepath.Join(dir, library.ExportDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("the card is read-only and there is nowhere else to write: %w", err)
	}
	return filepath.Join(home, "Pictures", "QK Export", filepath.Base(dir)), nil
}

// ExportAll develops every keeper and writes it out, one at a time — a
// full-resolution frame is hundreds of megabytes in flight, and doing four
// at once would buy nothing but swap. Progress goes out as events.
func (s *Service) ExportAll(ids []string, dest string) (ExportResult, error) {
	dir, err := s.exportDir(dest)
	if err != nil {
		return ExportResult{}, err
	}
	s.mu.Lock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var todo []library.Photo
	for _, p := range s.photos {
		if len(ids) == 0 || want[p.ID] {
			todo = append(todo, p)
		}
	}
	s.mu.Unlock()
	if len(todo) == 0 {
		return ExportResult{Dir: dir}, nil
	}

	go func() {
		done, failed := 0, 0
		for _, p := range todo {
			name := filepath.Base(displayFile(p))
			s.emit(Event{Type: "export", ID: p.ID, Name: name,
				Done: done, Total: len(todo)})
			if _, err := s.exportPhoto(p, dir); err != nil {
				failed++
			}
			done++
		}
		s.emit(Event{Type: "export", Done: done, Total: len(todo),
			Failed: failed, Dest: dir, Finished: true})
	}()
	return ExportResult{Dir: dir, Count: len(todo)}, nil
}

func (s *Service) serveExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID   string   `json:"id"`
		IDs  []string `json:"ids"`
		All  bool     `json:"all"`
		Dest string   `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var (
		res ExportResult
		err error
	)
	if req.All || len(req.IDs) > 0 {
		res, err = s.ExportAll(req.IDs, req.Dest)
	} else {
		res, err = s.ExportOne(req.ID, req.Dest)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}

// ExportedBytes develops a photo at full resolution and hands back the
// JPEG without writing it — the clipboard path.
func (s *Service) ExportedBytes(id string) ([]byte, error) {
	p, ok := s.photoByID(id)
	if !ok {
		return nil, fmt.Errorf("unknown photo %q", id)
	}
	sc, err := fullScene(p)
	if err != nil {
		return nil, err
	}
	img := develop.RenderInPlace(sc, s.editFor(p))
	return develop.EncodeJPEG(img, develop.DefaultQuality,
		develop.ExifFor(displayFile(p), sc.W, sc.H))
}
