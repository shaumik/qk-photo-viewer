package remote

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"testing/fstest"
)

func startTestServer(t *testing.T) (*Server, *http.Client) {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "api-ok")
	})
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<title>QK</title>")},
	}
	s, err := Start(api, frontend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop() })
	jar, _ := cookiejar.New(nil)
	return s, &http.Client{Jar: jar}
}

// localURL rewrites the LAN-IP URL to loopback so tests don't depend on
// the container's routing.
func localURL(s *Server) string {
	port := s.ln.Addr().String()
	port = port[strings.LastIndex(port, ":")+1:]
	return "http://127.0.0.1:" + port
}

func TestNoTokenIsRejected(t *testing.T) {
	s, c := startTestServer(t)
	resp, err := c.Get(localURL(s) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: HTTP %d, want 401", resp.StatusCode)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s, c := startTestServer(t)
	resp, err := c.Get(localURL(s) + "/?t=deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong token: HTTP %d, want 403", resp.StatusCode)
	}
}

func TestTokenGrantsCookieThenEverythingWorks(t *testing.T) {
	s, c := startTestServer(t)
	// The QR-code URL: token in query. Client follows the redirect and
	// keeps the cookie, like a phone browser would.
	resp, err := c.Get(localURL(s) + "/?t=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "QK") {
		t.Fatalf("after token: HTTP %d body %q", resp.StatusCode, body)
	}
	// Subsequent requests ride the cookie — no token in URL.
	resp, err = c.Get(localURL(s) + "/api/anything")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "api-ok" {
		t.Errorf("api after cookie: %q", body)
	}
}

func TestURLContainsToken(t *testing.T) {
	s, _ := startTestServer(t)
	if !strings.Contains(s.URL, "?t="+s.token) {
		t.Errorf("URL should embed the token for the QR code: %s", s.URL)
	}
}
