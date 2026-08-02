// Package remote serves the culling UI to phones over the local network —
// the airplane feature. The laptop shows a QR code containing a one-time
// token; the phone scans it, gets the same frontend the desktop uses, and
// culls through the same API with live sync. No internet involved.
package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
)

const preferredPort = 8787
const cookieName = "qk_token"

// Server is a running remote session listener.
type Server struct {
	URL   string // what the QR code encodes, token included
	token string
	srv   *http.Server
	ln    net.Listener
}

// Start begins serving the frontend (an fs.FS rooted at index.html) and the
// culling API on the LAN. api is the Service handler; every route requires
// the token, which arrives once via the QR URL and then rides in a cookie.
func Start(api http.Handler, frontend fs.FS) (*Server, error) {
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", preferredPort))
	if err != nil {
		if ln, err = net.Listen("tcp", ":0"); err != nil {
			return nil, fmt.Errorf("no port available: %w", err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", http.FileServerFS(frontend))

	s := &Server{
		token: tok,
		ln:    ln,
		srv:   &http.Server{Handler: requireToken(tok, mux)},
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s.URL = fmt.Sprintf("http://%s:%d/?t=%s", lanIP(), port, tok)
	go s.srv.Serve(ln)
	return s, nil
}

// Stop closes the listener and drops all remote sessions.
func (s *Server) Stop() error { return s.srv.Close() }

// requireToken gates every request. The QR URL carries ?t=<token> once;
// a matching request gets a cookie and a clean redirect, after which the
// cookie authorizes everything. Anything else is turned away.
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(token)) != 1 {
				http.Error(w, "wrong token — rescan the QR code on the laptop", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			q := r.URL.Query()
			q.Del("t")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil &&
			subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized — scan the QR code shown on the laptop", http.StatusUnauthorized)
	})
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// lanIP finds the address a phone on the same network can reach: the first
// global-unicast IPv4 on a real interface.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			ip := ipn.IP.To4()
			if ip != nil && !ip.IsLoopback() && ip.IsGlobalUnicast() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}
