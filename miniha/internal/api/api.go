/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package api serves the HA-compatible REST API, the login endpoints and
// the embedded web UI.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/automation"
	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/store"
	"github.com/probonopd/miniha/internal/template"
)

// Info is what GET /api/config reports about this instance.
type Info struct {
	LocationName string
	Version      string
	TimeZone     string
	UnitSystem   string
	Latitude     float64
	Longitude    float64
	Elevation    float64
	Currency     string
	ConfigDir    string
}

const sessionCookie = "miniha_session"
const sessionTTL = 30 * 24 * time.Hour

// AutomationSource is what the automation engine offers the API.
type AutomationSource interface {
	Summary() []map[string]any
	Reload() error
	SubscribeTrigger(cfg map[string]any, fire func(vars map[string]any)) (func(), error)
	ValidateConfig(triggers, conditions, actions any) map[string]any
	TemplateEnv() *template.Env
	// Traces returns the recent runs of one automation, newest first.
	Traces(entityID string) []map[string]any
}

// DiscoverySource lists devices found on the network (mDNS) that are not
// integrated yet, with a hint how to connect them.
type DiscoverySource interface {
	List() []map[string]any
}

// HistorySource is what the recorder offers the API (nil without one).
type HistorySource interface {
	// History returns per-entity state lists for GET /api/history/period.
	History(start, end time.Time, entityIDs []string, minimal, significantOnly bool) ([][]map[string]any, error)
	// Logbook returns entries for GET /api/logbook.
	Logbook(start, end time.Time, entityID string) ([]map[string]any, error)
	// Statistics returns long-term statistics per statistic id; a zero end
	// means no upper bound and period is hour, day, week or month.
	Statistics(start, end time.Time, statisticIDs []string, period string) (map[string][]map[string]any, error)
	// StatisticsMetadata describes the given statistic ids (all when empty).
	StatisticsMetadata(ids []string) []map[string]any
}

type Server struct {
	Automations AutomationSource
	History     HistorySource
	Discovered  DiscoverySource
	core        *core.Core
	auth        *store.AuthStore
	info        Info
	log         *slog.Logger
	static      fs.FS
	status      func() map[string]any
	mux         *http.ServeMux

	mu       sync.Mutex
	sessions map[string]time.Time
	loginMu  sync.Mutex
}

func New(c *core.Core, a *store.AuthStore, info Info, static fs.FS, status func() map[string]any, log *slog.Logger) *Server {
	s := &Server{core: c, auth: a, info: info, log: log, static: static, status: status, sessions: map[string]time.Time{}}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := http.NewServeMux()
	s.mux = m
	api := s.requireAuth
	m.HandleFunc("GET /api/", api(s.apiRoot))
	m.HandleFunc("GET /api/config", api(s.getConfig))
	m.HandleFunc("GET /api/states", api(s.getStates))
	m.HandleFunc("GET /api/states/{entity_id}", api(s.getState))
	m.HandleFunc("POST /api/states/{entity_id}", api(s.postState))
	m.HandleFunc("GET /api/services", api(s.getServices))
	m.HandleFunc("POST /api/services/{domain}/{service}", api(s.callService))
	m.HandleFunc("GET /api/events", api(s.getEvents))
	m.HandleFunc("POST /api/events/{event_type}", api(s.fireEvent))
	m.HandleFunc("POST /api/template", api(s.renderTemplate))
	m.HandleFunc("GET /api/error_log", api(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
	}))
	m.HandleFunc("GET /api/miniha/registry", api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.core.Snapshot())
	}))
	m.HandleFunc("GET /api/miniha/status", api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.status())
	}))
	m.HandleFunc("GET /api/miniha/stream", api(s.stream))
	m.HandleFunc("GET /api/miniha/automations", api(s.listAutomations))
	m.HandleFunc("GET /api/websocket", s.websocket)
	m.HandleFunc("GET /api/miniha/discovered", api(func(w http.ResponseWriter, r *http.Request) {
		if s.Discovered == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, s.Discovered.List())
	}))
	m.HandleFunc("GET /api/camera_proxy/{entity_id}", api(s.cameraProxy))
	// Webhooks are unauthenticated by design (HA too): the id is the secret.
	for _, method := range []string{"GET", "POST", "PUT", "HEAD"} {
		m.HandleFunc(method+" /api/webhook/{webhook_id}", s.webhook)
	}
	s.extraRoutes(m)
	m.HandleFunc("POST /api/miniha/entities/{entity_id}", api(s.updateEntity))
	m.HandleFunc("POST /api/miniha/devices/{id}", api(s.updateDevice))
	m.HandleFunc("POST /api/miniha/areas", api(s.createArea))
	m.HandleFunc("POST /api/miniha/areas/{id}", api(s.updateArea))
	m.HandleFunc("DELETE /api/miniha/areas/{id}", api(s.deleteArea))
	m.HandleFunc("GET /api/miniha/tokens", api(s.listTokens))
	m.HandleFunc("POST /api/miniha/tokens", api(s.createToken))
	m.HandleFunc("DELETE /api/miniha/tokens/{id}", api(s.deleteToken))
	m.HandleFunc("GET /api/history/period/{timestamp...}", api(s.history))
	m.HandleFunc("GET /api/logbook/{timestamp...}", api(s.logbook))
	m.HandleFunc("GET /api/miniha/files/{name}", api(s.getFile))
	m.HandleFunc("PUT /api/miniha/files/{name}", api(s.putFile))
	m.HandleFunc("POST /auth/login", s.login)
	m.HandleFunc("POST /auth/logout", s.logout)
	m.HandleFunc("GET /auth/check", api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	if s.static != nil {
		m.Handle("GET /", http.FileServer(http.FS(s.static)))
	}
}

// --- auth ---

func (s *Server) authenticated(r *http.Request) bool {
	// Open mode: with no admin password set, nothing requires a login.
	if !s.auth.HasPassword() {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		_, ok := s.auth.ValidateToken(strings.TrimSpace(h[7:]))
		return ok
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Unauthorized"})
			return
		}
		h(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	// One login at a time plus a delay on failure keeps brute force slow
	// without any state to persist.
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if !s.auth.CheckPassword(body.Password) {
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Invalid password"})
		return
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	tok := hex.EncodeToString(raw)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// haTime formats like HA: ISO 8601 with microseconds and a numeric offset.
func haTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

func stateJSON(s *core.State) map[string]any {
	attrs := s.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	return map[string]any{
		"entity_id": s.EntityID, "state": s.State, "attributes": attrs,
		"last_changed": haTime(s.LastChanged), "last_updated": haTime(s.LastUpdated), "last_reported": haTime(s.LastReported),
		"context": s.Context,
	}
}

// --- handlers ---

func (s *Server) apiRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "API running."})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.configData())
}

// configData is the GET /api/config document, shared with the
// WebSocket get_config command.
func (s *Server) configData() map[string]any {
	units := map[string]any{"length": "km", "mass": "g", "temperature": "°C", "volume": "L", "accumulated_precipitation": "mm", "area": "m²", "pressure": "Pa", "wind_speed": "m/s"}
	if s.info.UnitSystem != "metric" {
		units = map[string]any{"length": "mi", "mass": "lb", "temperature": "°F", "volume": "gal", "accumulated_precipitation": "in", "area": "ft²", "pressure": "psi", "wind_speed": "mph"}
	}
	return map[string]any{
		"components": []string{"homeassistant", "api", "http", "mqtt", "automation", "script", "scene", "websocket_api"}, "config_dir": s.info.ConfigDir,
		"elevation": s.info.Elevation, "latitude": s.info.Latitude, "longitude": s.info.Longitude,
		"location_name": s.info.LocationName, "time_zone": s.info.TimeZone, "unit_system": units,
		"version": s.info.Version, "state": "RUNNING", "currency": s.info.Currency, "language": "en",
		"external_url": nil, "internal_url": nil, "country": nil, "safe_mode": false, "recovery_mode": false,
		"debug": false, "radius": 100, "allowlist_external_dirs": []string{}, "whitelist_external_dirs": []string{},
	}
}

// cameraProxy serves the latest image of a camera or image entity.
func (s *Server) cameraProxy(w http.ResponseWriter, r *http.Request) {
	src, ok := s.core.Controller(r.PathValue("entity_id")).(core.ImageSource)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Entity not found."})
		return
	}
	data, ct := src.Image()
	if len(data) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "No image available."})
		return
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func (s *Server) getStates(w http.ResponseWriter, r *http.Request) {
	states := s.core.States()
	out := make([]map[string]any, 0, len(states))
	for _, st := range states {
		out = append(out, stateJSON(st))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	st := s.core.GetState(r.PathValue("entity_id"))
	if st == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Entity not found."})
		return
	}
	writeJSON(w, http.StatusOK, stateJSON(st))
}

func (s *Server) postState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("entity_id")
	var body struct {
		State      *string        `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if body.State == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "No state specified."})
		return
	}
	existed := s.core.GetState(id) != nil
	if err := s.core.SetState(id, *body.State, body.Attributes, core.Context{}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/states/"+id)
	writeJSON(w, status, stateJSON(s.core.GetState(id)))
}

func (s *Server) getServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.core.Services())
}

func (s *Server) callService(w http.ResponseWriter, r *http.Request) {
	var data map[string]any
	if err := readJSON(r, &data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	changed, err := s.core.CallService(r.Context(), r.PathValue("domain"), r.PathValue("service"), data, core.Context{})
	if err != nil {
		var nf *core.ServiceNotFound
		if errors.As(err, &nf) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Service not found."})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(changed))
	for _, st := range changed {
		out = append(out, stateJSON(st))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for typ, n := range s.core.EventTypes() {
		out = append(out, map[string]any{"event": typ, "listener_count": n})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fireEvent(w http.ResponseWriter, r *http.Request) {
	var data map[string]any
	if err := readJSON(r, &data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	typ := r.PathValue("event_type")
	s.core.Fire(typ, data, core.NewContext())
	writeJSON(w, http.StatusOK, map[string]any{"message": "Event " + typ + " fired."})
}

func (s *Server) renderTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template  string         `json:"template"`
		Variables map[string]any `json:"variables"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	tpl, err := template.Parse(body.Template)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Error rendering template: " + err.Error()})
		return
	}
	out, err := tpl.Render(body.Variables, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Error rendering template: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, out)
}

// stream pushes state and registry changes as server-sent events so the
// UI follows discovered and lost devices without polling.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events := make(chan []byte, 256)
	unsub := s.core.Subscribe("", func(ev core.Event) {
		var payload any
		switch ev.Type {
		case "state_changed":
			ns, _ := ev.Data["new_state"].(*core.State)
			d := map[string]any{"entity_id": ev.Data["entity_id"]}
			if ns != nil {
				d["new_state"] = stateJSON(ns)
			} else {
				d["new_state"] = nil
			}
			payload = map[string]any{"event_type": ev.Type, "data": d}
		case "registry_updated":
			payload = map[string]any{"event_type": ev.Type}
		default:
			return
		}
		b, _ := json.Marshal(payload)
		select {
		case events <- b:
		default:
			// A client that cannot keep up loses events; it resyncs on
			// the next full fetch triggered by whatever arrives after.
		}
	})
	defer unsub()
	fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-events:
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

var editableFiles = map[string]bool{"automations.yaml": true, "scripts.yaml": true, "scenes.yaml": true, "ui-lovelace.yaml": true}

func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request) {
	if s.Automations == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.Automations.Summary())
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !editableFiles[name] {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "no such file"})
		return
	}
	data, err := os.ReadFile(filepath.Join(s.info.ConfigDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Write(data)
}

// putFile validates, writes atomically and reloads; a file with errors
// is never written.
func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !editableFiles[name] {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "no such file"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if err := automation.ValidateWithBlueprints(s.info.ConfigDir, name, data); err != nil && name != "ui-lovelace.yaml" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if err := store.WriteAtomic(filepath.Join(s.info.ConfigDir, name), data, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	if s.Automations != nil {
		if err := s.Automations.Reload(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// webhook fires the "webhook" event that the automation webhook trigger
// listens to, with the payload parsed like HA: json when it parses,
// form data otherwise, plus query and headers.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("webhook_id")
	if len(id) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "webhook id too short"})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	data := map[string]any{"webhook_id": id, "method": r.Method, "query": flatten(r.URL.Query()), "headers": flatten(r.Header), "remote": r.RemoteAddr}
	var js any
	if len(body) > 0 && json.Unmarshal(body, &js) == nil {
		data["json"] = js
	} else if len(body) > 0 {
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			r.ParseForm()
			data["data"] = flatten(r.PostForm)
		} else {
			data["data"] = string(body)
		}
	}
	s.core.Fire("webhook", data, core.NewContext())
	w.WriteHeader(http.StatusOK)
}

func flatten(v map[string][]string) map[string]any {
	out := make(map[string]any, len(v))
	for k, vals := range v {
		if len(vals) == 1 {
			out[k] = vals[0]
		} else {
			out[k] = vals
		}
	}
	return out
}
