/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/recorder"
	"github.com/probonopd/miniha/internal/store"
)

// extraEnv is a server with a config directory, an optional automation
// engine and an optional history source.
type extraEnv struct {
	ts  *httptest.Server
	c   *core.Core
	s   *Server
	dir string
	tok string
}

func newExtraEnv(t *testing.T) *extraEnv {
	t.Helper()
	c := core.New()
	a, _ := store.LoadAuth(filepath.Join(t.TempDir(), "auth.json"))
	a.SetPassword("password1")
	tok, _, _ := a.CreateToken("test")
	dir := t.TempDir()
	s := New(c, a, Info{Version: "0.0.1", ConfigDir: dir}, nil, func() map[string]any { return nil }, slog.Default())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &extraEnv{ts: ts, c: c, s: s, dir: dir, tok: tok}
}

func (e *extraEnv) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	return do(t, e.ts, method, path, e.tok, body)
}

// withRecorder attaches a real recorder whose clock is driven by the
// returned pointer, which is also the core clock.
func (e *extraEnv) withRecorder(t *testing.T, start time.Time) (*recorder.Recorder, *time.Time) {
	t.Helper()
	now := start
	e.c.Now = func() time.Time { return now }
	rec, err := recorder.New(e.c, recorder.Config{Path: filepath.Join(t.TempDir(), "history.db")},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rec.Stop() })
	e.s.History = rec
	return rec, &now
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not an object: %v: %s", err, body)
	}
	return m
}

func decodeList(t *testing.T, body string) []any {
	t.Helper()
	var l []any
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		t.Fatalf("not a list: %v: %s", err, body)
	}
	return l
}

func TestRESTNotificationsCRUD(t *testing.T) {
	e := newExtraEnv(t)
	if code, body := e.do(t, "GET", "/api/miniha/notifications", ""); code != 200 || body != "[]\n" {
		t.Fatalf("empty list: %d %q", code, body)
	}
	if code, _ := e.do(t, "POST", "/api/miniha/notifications", `{"title":"T"}`); code != 400 {
		t.Fatalf("missing message: %d", code)
	}
	code, body := e.do(t, "POST", "/api/miniha/notifications", `{"title":"Hello","message":"World","notification_id":"greet"}`)
	n := decodeObject(t, body)
	if code != 201 || n["notification_id"] != "greet" || n["title"] != "Hello" || n["message"] != "World" {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, body := e.do(t, "POST", "/api/miniha/notifications", `{"message":"anonymous"}`); code != 201 || decodeObject(t, body)["notification_id"] == "" {
		t.Fatalf("create without id: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/miniha/notifications", "")
	if list := decodeList(t, body); code != 200 || len(list) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ := e.do(t, "DELETE", "/api/miniha/notifications/greet", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/miniha/notifications/greet", ""); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/miniha/notifications", ""); code != 200 {
		t.Fatalf("delete all: %d", code)
	}
	if got := e.c.Notifications(); len(got) != 0 {
		t.Fatalf("not dismissed: %v", got)
	}
	if code, _ := do(t, e.ts, "GET", "/api/miniha/notifications", "", ""); code != 401 {
		t.Fatalf("no auth: %d", code)
	}
}

func TestRESTAutomationTraces(t *testing.T) {
	e := newExtraEnv(t)
	if code, body := e.do(t, "GET", "/api/miniha/automations/automation.a/traces", ""); code != 200 || body != "[]\n" {
		t.Fatalf("without engine: %d %q", code, body)
	}
	e.s.Automations = &fakeAutomations{c: e.c}
	code, body := e.do(t, "GET", "/api/miniha/automations/automation.a/traces", "")
	list := decodeList(t, body)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["entity_id"] != "automation.a" {
		t.Fatalf("traces: %d %s", code, body)
	}
	if code, body := e.do(t, "GET", "/api/miniha/automations/automation.none/traces", ""); code != 200 || body != "[]\n" {
		t.Fatalf("no traces must be a list: %d %q", code, body)
	}
}

const goodDashboard = `title: Home
views:
  - title: Living
    path: living
    cards:
      - type: entities
        entities: [light.a, light.b]
      - type: vertical-stack
        cards:
          - type: gauge
            entity: sensor.t
`

func TestRESTDashboard(t *testing.T) {
	e := newExtraEnv(t)
	if code, body := e.do(t, "GET", "/api/miniha/dashboard", ""); code != 404 || decodeObject(t, body)["message"] == "" {
		t.Fatalf("absent file: %d %s", code, body)
	}
	if code, body := e.do(t, "PUT", "/api/miniha/files/ui-lovelace.yaml", goodDashboard); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	code, body := e.do(t, "GET", "/api/miniha/dashboard", "")
	doc := decodeObject(t, body)
	views, _ := doc["views"].([]any)
	if code != 200 || doc["title"] != "Home" || len(views) != 1 {
		t.Fatalf("dashboard: %d %s", code, body)
	}
	cards := views[0].(map[string]any)["cards"].([]any)
	if views[0].(map[string]any)["path"] != "living" || len(cards) != 2 || cards[0].(map[string]any)["type"] != "entities" {
		t.Fatalf("views: %s", body)
	}

	bad := map[string]string{
		"not a mapping":     "- a\n- b\n",
		"views not a list":  "views: nope\n",
		"views missing":     "title: x\n",
		"view not a map":    "views:\n  - 3\n",
		"cards not a list":  "views:\n  - cards: 4\n",
		"card without type": "views:\n  - cards:\n      - entity: light.a\n",
		"nested no type":    "views:\n  - cards:\n      - type: vertical-stack\n        cards:\n          - entity: x\n",
		"syntax":            "views: [\n",
	}
	for name, content := range bad {
		if code, _ := e.do(t, "PUT", "/api/miniha/files/ui-lovelace.yaml", content); code != 200 {
			t.Fatalf("%s: put %d", name, code)
		}
		code, body := e.do(t, "GET", "/api/miniha/dashboard", "")
		if code != 400 || !strings.Contains(decodeObject(t, body)["message"].(string), "ui-lovelace.yaml") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
}

// statsHistory is a HistorySource that records the statistics call.
type statsHistory struct {
	HistorySource
	start, end time.Time
	ids        []string
	period     string
	fail       error
}

func (h *statsHistory) Statistics(start, end time.Time, ids []string, period string) (map[string][]map[string]any, error) {
	h.start, h.end, h.ids, h.period = start, end, ids, period
	if h.fail != nil {
		return nil, h.fail
	}
	return map[string][]map[string]any{"sensor.t": {{"start": "s", "end": "e", "mean": 1.5}}}, nil
}

func TestRESTStatistics(t *testing.T) {
	e := newExtraEnv(t)
	if code, _ := e.do(t, "GET", "/api/miniha/statistics?start=2026-01-01T00:00:00Z", ""); code != 404 {
		t.Fatalf("without recorder: %d", code)
	}
	h := &statsHistory{}
	e.s.History = h
	code, body := e.do(t, "GET", "/api/miniha/statistics?statistic_ids=sensor.t,sensor.u&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&period=day", "")
	if code != 200 || decodeObject(t, body)["sensor.t"] == nil {
		t.Fatalf("statistics: %d %s", code, body)
	}
	if !h.start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !h.end.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) ||
		strings.Join(h.ids, "|") != "sensor.t|sensor.u" || h.period != "day" {
		t.Fatalf("arguments: %+v", h)
	}
	if code, _ := e.do(t, "GET", "/api/miniha/statistics?start=2026-01-01T00:00:00Z", ""); code != 200 || h.period != "hour" || h.end != (time.Time{}) || h.ids != nil {
		t.Fatalf("defaults: %d %+v", code, h)
	}
	for _, q := range []string{"", "?start=garbage", "?start=2026-01-01T00:00:00Z&end=garbage", "?start=2026-01-01T00:00:00Z&period=year"} {
		if code, _ := e.do(t, "GET", "/api/miniha/statistics"+q, ""); code != 400 {
			t.Errorf("query %q: %d", q, code)
		}
	}
	h.fail = errors.New("disk on fire")
	if code, body := e.do(t, "GET", "/api/miniha/statistics?start=2026-01-01T00:00:00Z", ""); code != 500 || !strings.Contains(body, "disk on fire") {
		t.Fatalf("recorder error: %d %s", code, body)
	}
}

func TestRESTStatisticsWithRecorder(t *testing.T) {
	e := newExtraEnv(t)
	rec, now := e.withRecorder(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	attrs := map[string]any{"state_class": "measurement", "unit_of_measurement": "C"}
	e.c.SetState("sensor.t", "10", attrs, core.Context{})
	*now = now.Add(30 * time.Minute)
	e.c.SetState("sensor.t", "20", attrs, core.Context{})
	*now = now.Add(150 * time.Minute)
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(t, "GET", "/api/miniha/statistics?statistic_ids=sensor.t&start=2026-01-01T00:00:00Z&period=hour", "")
	rows := decodeObject(t, body)["sensor.t"].([]any)
	if code != 200 || len(rows) != 2 || rows[0].(map[string]any)["max"] != float64(20) {
		t.Fatalf("recorded statistics: %d %s", code, body)
	}
}
