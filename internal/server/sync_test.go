package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRejectStateSyncsAndSurvivesJoin(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}

	ch, cancel := s.Subscribe()
	defer cancel()

	if err := s.SetReject("DSC00010", true); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Type != "reject" || e.ID != "DSC00010" || !e.Rejected {
			t.Errorf("wrong event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no reject event delivered")
	}

	// A late joiner (phone connecting mid-cull) sees the mark in State.
	st := s.State()
	if len(st.Rejected) != 1 || st.Rejected[0] != "DSC00010" {
		t.Errorf("late joiner should see marks: %+v", st.Rejected)
	}

	if err := s.SetReject("GHOST", true); err == nil {
		t.Error("marking an unknown photo should error")
	}
}

func TestCommitEmitsEventAndClearsMarks(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	s.SetReject("DSC00010", true)
	ch, cancel := s.Subscribe()
	defer cancel()

	if _, err := s.CommitRejects([]string{"DSC00010"}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Type != "commit" || len(e.MovedIDs) != 1 || e.MovedIDs[0] != "DSC00010" {
			t.Errorf("wrong commit event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no commit event delivered")
	}
	if got := s.State().Rejected; len(got) != 0 {
		t.Errorf("marks should clear after commit: %v", got)
	}
}

func TestHTTPRejectCommitAndPhotos(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	post := func(url, body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", url, strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("/api/reject", `{"id":"DSC00011","rejected":true}`); code != 204 {
		t.Fatalf("reject: HTTP %d", code)
	}
	if code := post("/api/reject", `{"id":"GHOST","rejected":true}`); code != 404 {
		t.Errorf("unknown id reject: HTTP %d, want 404", code)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/photos", nil))
	var st OpenResult
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if len(st.Photos) != 3 || len(st.Rejected) != 1 || st.Rejected[0] != "DSC00011" {
		t.Errorf("photos endpoint state wrong: %+v", st)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/commit", strings.NewReader(`{"ids":["DSC00011"]}`)))
	var cr CommitResult
	if err := json.NewDecoder(rec.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.MovedIDs) != 1 || cr.MovedIDs[0] != "DSC00011" {
		t.Errorf("commit over HTTP: %+v", cr)
	}
}

func TestSSEStreamDeliversEvents(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.SetReject("DSC00012", true)
	}()

	sc := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var e Event
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "reject" && e.ID == "DSC00012" && e.Rejected {
			return // success
		}
	}
	t.Fatal("reject event never arrived on the SSE stream")
}
