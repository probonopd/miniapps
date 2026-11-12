/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

const dashboardFile = "ui-lovelace.yaml"

// extraRoutes registers the routes added after the first release
// (notifications, traces, statistics, dashboards).
func (s *Server) extraRoutes(m *http.ServeMux) {
	api := s.requireAuth
	m.HandleFunc("GET /api/miniha/notifications", api(s.listNotifications))
	m.HandleFunc("POST /api/miniha/notifications", api(s.createNotification))
	m.HandleFunc("DELETE /api/miniha/notifications/{id}", api(s.dismissNotification))
	m.HandleFunc("DELETE /api/miniha/notifications", api(s.dismissAllNotifications))
	m.HandleFunc("GET /api/miniha/automations/{entity_id}/traces", api(s.automationTraces))
	m.HandleFunc("GET /api/miniha/statistics", api(s.statistics))
	m.HandleFunc("GET /api/miniha/dashboard", api(s.dashboard))
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.core.Notifications())
}

func (s *Server) createNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title          string `json:"title"`
		Message        string `json:"message"`
		NotificationID string `json:"notification_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if body.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "message is required"})
		return
	}
	writeJSON(w, http.StatusCreated, s.core.Notify(body.NotificationID, body.Title, body.Message))
}

func (s *Server) dismissNotification(w http.ResponseWriter, r *http.Request) {
	if !s.core.Dismiss(r.PathValue("id")) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Notification not found."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) dismissAllNotifications(w http.ResponseWriter, r *http.Request) {
	s.core.DismissAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) automationTraces(w http.ResponseWriter, r *http.Request) {
	if s.Automations == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	traces := s.Automations.Traces(r.PathValue("entity_id"))
	if traces == nil {
		traces = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, traces)
}

// statistics implements GET /api/miniha/statistics?statistic_ids=a,b
// &start=ISO&end=ISO&period=hour. start is required: a default window
// would silently hide data the caller expected.
func (s *Server) statistics(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "recorder not enabled"})
		return
	}
	q := r.URL.Query()
	start, ok := parseTimestamp(q.Get("start"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "start is required (ISO 8601)"})
		return
	}
	var end time.Time
	if v := q.Get("end"); v != "" {
		if end, ok = parseTimestamp(v); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "end must be ISO 8601"})
			return
		}
	}
	period := q.Get("period")
	if period == "" {
		period = "hour"
	}
	if !validStatisticsPeriod(period) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "period must be hour, day, week or month"})
		return
	}
	var ids []string
	if v := q.Get("statistic_ids"); v != "" {
		ids = strings.Split(v, ",")
	}
	out, err := s.History.Statistics(start, end, ids, period)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validStatisticsPeriod(p string) bool {
	return p == "hour" || p == "day" || p == "week" || p == "month"
}

// errDashboardMissing means there is no ui-lovelace.yaml.
var errDashboardMissing = errors.New("no dashboard configured")

// dashboardError is a problem with the content of ui-lovelace.yaml, as
// opposed to a failure to read it.
type dashboardError struct{ msg string }

func (e *dashboardError) Error() string { return e.msg }

// loadDashboard parses <config dir>/ui-lovelace.yaml into the document
// the frontend renders ({title, views: [{title, path, cards}]}).
func (s *Server) loadDashboard() (map[string]any, error) {
	if s.info.ConfigDir == "" {
		return nil, errDashboardMissing
	}
	data, err := os.ReadFile(filepath.Join(s.info.ConfigDir, dashboardFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errDashboardMissing
	}
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &dashboardError{dashboardFile + ": " + err.Error()}
	}
	cfg, ok := doc.(map[string]any)
	if !ok {
		return nil, &dashboardError{dashboardFile + ": the document must be a mapping with a views list"}
	}
	if err := validateDashboard(cfg); err != nil {
		return nil, &dashboardError{dashboardFile + ": " + err.Error()}
	}
	return cfg, nil
}

func validateDashboard(cfg map[string]any) error {
	views, ok := cfg["views"].([]any)
	if !ok {
		return errors.New("views must be a list")
	}
	for i, v := range views {
		view, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("views[%d] must be a mapping", i)
		}
		if err := validateCards(view["cards"], fmt.Sprintf("views[%d].cards", i)); err != nil {
			return err
		}
	}
	return nil
}

// validateCards checks the list of cards of a view or of a stack card
// (stacks nest further cards under the same key).
func validateCards(v any, path string) error {
	if v == nil {
		return nil
	}
	cards, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s must be a list", path)
	}
	for i, c := range cards {
		cp := fmt.Sprintf("%s[%d]", path, i)
		card, ok := c.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a mapping", cp)
		}
		if typ, _ := card["type"].(string); typ == "" {
			return fmt.Errorf("%s has no type", cp)
		}
		if err := validateCards(card["cards"], cp+".cards"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadDashboard()
	var invalid *dashboardError
	switch {
	case errors.Is(err, errDashboardMissing):
		writeJSON(w, http.StatusNotFound, map[string]any{"message": err.Error()})
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
	default:
		writeJSON(w, http.StatusOK, cfg)
	}
}
