package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Browser cookies and collector bearer tokens share one local access token.
// Cookies contain a derived session value rather than the raw API credential.
func (s *Server) session() string {
	mac := hmac.New(sha256.New, []byte(s.Token))
	mac.Write([]byte("netwatch-browser-session"))
	return hex.EncodeToString(mac.Sum(nil))
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
	}
	return true // CLI collectors use Bearer authentication without an Origin header.
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !sameOrigin(r) {
			respond(w, 403, map[string]string{"error": "request origin denied"})
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		valid := s.Token != "" && equal(bearer, s.Token)
		if cookie, err := r.Cookie("netwatch_session"); err == nil {
			valid = valid || (s.Token != "" && equal(cookie.Value, s.session()))
		}
		if !valid {
			respond(w, 401, map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		respond(w, 403, map[string]string{"error": "request origin denied"})
		return
	}
	s.loginMu.Lock()
	if time.Since(s.loginWindow) > time.Minute {
		s.loginWindow = time.Now()
		s.loginAttempts = 0
	}
	s.loginAttempts++
	limited := s.loginAttempts > 20
	s.loginMu.Unlock()
	if limited {
		respond(w, 429, map[string]string{"error": "too many login attempts; wait a minute"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(w, r, &body); err != nil {
		respond(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	if s.Token == "" || !equal(body.Token, s.Token) {
		respond(w, 401, map[string]string{"error": "invalid access token"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "netwatch_session", Value: s.session(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 8 * 60 * 60})
	respond(w, 200, map[string]bool{"ok": true})
}
