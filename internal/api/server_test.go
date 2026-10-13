package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationAndOrigin(t *testing.T) {
	s := &Server{Token: "test-token"}
	h := s.Handler()
	call := func(method, path, body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/config", "", "", ""); w.Code != 401 {
		t.Fatal("missing token accepted")
	}
	if w := call("GET", "/api/config", "", "wrong", ""); w.Code != 401 {
		t.Fatal("wrong token accepted")
	}
	if w := call("GET", "/api/config", "", "test-token", ""); w.Code != 200 {
		t.Fatal("valid token rejected")
	}
	if w := call("POST", "/api/login", `{"token":"test-token"}`, "", "https://evil.example"); w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	login := call("POST", "/api/login", `{"token":"test-token"}`, "", "")
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatal("login failed")
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Value == s.Token {
		t.Fatal("unsafe session cookie")
	}
	r := httptest.NewRequest("GET", "/api/config", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("session rejected")
	}
	if w := call("PATCH", "/api/incidents/test", `{"status":"resolved"}`, "test-token", "https://evil.example"); w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}

func TestRejectTrailingJSON(t *testing.T) {
	s := &Server{Token: "test-token"}
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"token":"test-token"} {}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("accepted trailing JSON: %d", w.Code)
	}
}
