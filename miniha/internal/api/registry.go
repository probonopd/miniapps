/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) updateEntity(w http.ResponseWriter, r *http.Request) {
	var upd map[string]any
	if err := readJSON(r, &upd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	e, err := s.core.UpdateEntity(r.PathValue("entity_id"), upd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var upd map[string]any
	if err := readJSON(r, &upd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	d, err := s.core.UpdateDevice(r.PathValue("id"), upd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) createArea(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	a, err := s.core.CreateArea(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) updateArea(w http.ResponseWriter, r *http.Request) {
	var upd map[string]any
	if err := readJSON(r, &upd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	a, err := s.core.UpdateArea(r.PathValue("id"), upd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteArea(w http.ResponseWriter, r *http.Request) {
	if err := s.core.DeleteArea(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.auth.ListTokens())
}

// createToken returns the plain token exactly once, like HA's
// long-lived access token dialog.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	plain, t, err := s.auth.CreateToken(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": t.ID, "name": t.Name, "token": plain})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.DeleteToken(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// parseTimestamp reads HA's URL timestamps (RFC 3339, "+" may arrive as
// a space after URL decoding).
func parseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(s, "/"))
	if s == "" {
		return time.Time{}, false
	}
	s = strings.Replace(s, " ", "+", 1)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// history implements GET /api/history/period/<start>?filter_entity_id=
// &end_time=&minimal_response&significant_changes_only.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "recorder not enabled"})
		return
	}
	start, ok := parseTimestamp(r.PathValue("timestamp"))
	if !ok {
		start = time.Now().Add(-24 * time.Hour)
	}
	end := start.Add(24 * time.Hour)
	if e, ok := parseTimestamp(r.URL.Query().Get("end_time")); ok {
		end = e
	}
	var ids []string
	if f := r.URL.Query().Get("filter_entity_id"); f != "" {
		ids = strings.Split(f, ",")
	}
	_, minimal := r.URL.Query()["minimal_response"]
	_, significant := r.URL.Query()["significant_changes_only"]
	out, err := s.History.History(start, end, ids, minimal, significant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	if out == nil {
		out = [][]map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) logbook(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "recorder not enabled"})
		return
	}
	start, ok := parseTimestamp(r.PathValue("timestamp"))
	if !ok {
		start = time.Now().Add(-24 * time.Hour)
	}
	end := start.Add(24 * time.Hour)
	if e, ok := parseTimestamp(r.URL.Query().Get("end_time")); ok {
		end = e
	}
	out, err := s.History.Logbook(start, end, r.URL.Query().Get("entity"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}
