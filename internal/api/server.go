package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
	"github.com/Gurveer1510/netwatch/internal/store"
	"github.com/redis/go-redis/v9"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	Store         *store.Store
	Redis         *redis.Client
	Token         string
	AIEnabled     bool
	Model         string
	loginMu       sync.Mutex
	loginAttempts int
	loginWindow   time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("POST /api/login", s.login)
	mux.Handle("POST /api/logout", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "netwatch_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		respond(w, http.StatusOK, map[string]bool{"ok": true})
	})))
	mux.Handle("GET /api/config", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]interface{}{"ai_enabled": s.AIEnabled, "model": s.Model, "poll_seconds": 5})
	})))
	mux.Handle("GET /api/devices", s.auth(http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/devices/{device}/history", s.auth(http.HandlerFunc(s.history)))
	mux.Handle("GET /api/incidents", s.auth(http.HandlerFunc(s.incidents)))
	mux.Handle("PATCH /api/incidents/{id}", s.auth(http.HandlerFunc(s.status)))
	mux.Handle("POST /api/incidents/{id}/analyze", s.auth(http.HandlerFunc(s.analyze)))
	mux.Handle("POST /api/telemetry", s.auth(http.HandlerFunc(s.ingest)))
	web, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Pool.Ping(r.Context()); err != nil {
		respond(w, http.StatusServiceUnavailable, map[string]string{"status": "database unavailable"})
		return
	}
	if err := s.Redis.Ping(r.Context()).Err(); err != nil {
		respond(w, http.StatusServiceUnavailable, map[string]string{"status": "Redis unavailable"})
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.Devices(r.Context())
	s.result(w, items, err)
}
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.History(r.Context(), r.PathValue("device"), limit(r, 120))
	s.result(w, items, err)
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "open" && status != "resolved" {
		respond(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}
	items, err := s.Store.Incidents(r.Context(), status, limit(r, 100))
	s.result(w, items, err)
}
func limit(r *http.Request, fallback int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return fallback
	}
	if n > 500 {
		return 500
	}
	return n
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := decode(w, r, &body); err != nil || (body.Status != "open" && body.Status != "resolved") {
		respond(w, http.StatusBadRequest, map[string]string{"error": "status must be open or resolved"})
		return
	}
	ok, err := s.Store.SetStatus(r.Context(), r.PathValue("id"), body.Status)
	if err != nil {
		s.result(w, nil, err)
		return
	}
	if !ok {
		respond(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}
	respond(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	if !s.AIEnabled {
		respond(w, http.StatusConflict, map[string]string{"error": "AI is disabled; enable it in the environment and restart server"})
		return
	}
	ok, err := s.Store.RetryAnalysis(r.Context(), r.PathValue("id"))
	if err != nil {
		s.result(w, nil, err)
		return
	}
	if !ok {
		respond(w, http.StatusConflict, map[string]string{"error": "incident missing or analysis already running"})
		return
	}
	respond(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if err := decode(w, r, &body); err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	metric, err := monitor.Decode(redis.XMessage{Values: map[string]interface{}{"payload": string(body)}})
	if err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if metric.Timestamp.After(time.Now().Add(5 * time.Minute)) {
		respond(w, http.StatusBadRequest, map[string]string{"error": "timestamp too far in the future"})
		return
	}
	id, err := s.Redis.XAdd(r.Context(), &redis.XAddArgs{Stream: "telemetry:raw", Values: map[string]interface{}{"payload": string(body)}}).Result()
	if err != nil {
		s.result(w, nil, err)
		return
	}
	respond(w, http.StatusAccepted, map[string]string{"event_id": id})
}

func decode(w http.ResponseWriter, r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra interface{}
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected trailing content")
	}
	return nil
}

func respond(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
func (s *Server) result(w http.ResponseWriter, body interface{}, err error) {
	if err != nil {
		log.Printf("API storage: %v", err)
		respond(w, http.StatusServiceUnavailable, map[string]string{"error": "storage temporarily unavailable"})
		return
	}
	respond(w, http.StatusOK, body)
}
