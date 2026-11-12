/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/store"
)

type fakeSwitch struct {
	c  *core.Core
	id string
}

func (f *fakeSwitch) TurnOn(ctx context.Context, _ map[string]any) error {
	return f.c.SetState(f.id, "on", nil, core.ContextFrom(ctx))
}
func (f *fakeSwitch) TurnOff(ctx context.Context, _ map[string]any) error {
	return f.c.SetState(f.id, "off", nil, core.ContextFrom(ctx))
}

func newTestServer(t *testing.T) (*httptest.Server, *core.Core, string) {
	t.Helper()
	c := core.New()
	a, _ := store.LoadAuth(filepath.Join(t.TempDir(), "auth.json"))
	a.SetPassword("password1")
	tok, _, _ := a.CreateToken("test")
	s := New(c, a, Info{LocationName: "Test", Version: "0.0.1", TimeZone: "UTC", UnitSystem: "metric"}, nil, func() map[string]any { return map[string]any{"ok": true} }, slog.Default())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c.SetState("switch.a", "off", map[string]any{"friendly_name": "A"}, core.Context{})
	c.RegisterController("switch.a", &fakeSwitch{c, "switch.a"})
	return ts, c, tok
}

func do(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, sb.String()
}

func TestAuthRequired(t *testing.T) {
	ts, _, tok := newTestServer(t)
	if code, _ := do(t, ts, "GET", "/api/", "", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := do(t, ts, "GET", "/api/", "miniha_bogus", ""); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code, body := do(t, ts, "GET", "/api/", tok, ""); code != 200 || !strings.Contains(body, "API running.") {
		t.Fatalf("token: %d %s", code, body)
	}
}

func TestOpenModeWithoutPassword(t *testing.T) {
	c := core.New()
	a, _ := store.LoadAuth(filepath.Join(t.TempDir(), "auth.json"))
	s := New(c, a, Info{}, nil, func() map[string]any { return nil }, slog.Default())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if code, _ := do(t, ts, "GET", "/api/states", "", ""); code != 200 {
		t.Fatalf("open mode: %d", code)
	}
	a.SetPassword("x")
	if code, _ := do(t, ts, "GET", "/api/states", "", ""); code != 401 {
		t.Fatalf("after setting a password: %d", code)
	}
}

func TestLoginCookie(t *testing.T) {
	ts, _, _ := newTestServer(t)
	res, err := http.Post(ts.URL+"/auth/login", "application/json", strings.NewReader(`{"password":"wrong"}`))
	if err != nil || res.StatusCode != 401 {
		t.Fatalf("wrong password: %v %v", err, res.StatusCode)
	}
	res, _ = http.Post(ts.URL+"/auth/login", "application/json", strings.NewReader(`{"password":"password1"}`))
	if res.StatusCode != 200 || len(res.Cookies()) != 1 {
		t.Fatalf("login: %d cookies=%d", res.StatusCode, len(res.Cookies()))
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/states", nil)
	req.AddCookie(res.Cookies()[0])
	res2, _ := http.DefaultClient.Do(req)
	if res2.StatusCode != 200 {
		t.Fatalf("cookie auth: %d", res2.StatusCode)
	}
}

func TestStatesAndServices(t *testing.T) {
	ts, c, tok := newTestServer(t)
	code, body := do(t, ts, "GET", "/api/states", tok, "")
	var states []map[string]any
	if err := json.Unmarshal([]byte(body), &states); err != nil || code != 200 || len(states) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	st := states[0]
	if st["entity_id"] != "switch.a" || st["state"] != "off" || !strings.HasSuffix(st["last_changed"].(string), "+00:00") || len(st["last_changed"].(string)) != 32 {
		t.Fatalf("state shape: %v", st)
	}
	if ctx := st["context"].(map[string]any); ctx["id"] == "" || ctx["user_id"] != nil {
		t.Fatalf("context shape: %v", ctx)
	}
	code, body = do(t, ts, "POST", "/api/services/switch/turn_on", tok, `{"entity_id":"switch.a"}`)
	if code != 200 || !strings.Contains(body, `"state":"on"`) {
		t.Fatalf("service: %d %s", code, body)
	}
	if c.GetState("switch.a").State != "on" {
		t.Fatal("state not changed")
	}
	if code, _ := do(t, ts, "POST", "/api/services/nope/x", tok, `{}`); code != 400 {
		t.Fatalf("unknown service: %d", code)
	}
	if code, _ := do(t, ts, "GET", "/api/states/switch.nope", tok, ""); code != 404 {
		t.Fatalf("missing entity: %d", code)
	}
	code, body = do(t, ts, "POST", "/api/states/sensor.manual", tok, `{"state":"42","attributes":{"unit_of_measurement":"x"}}`)
	if code != 201 || !strings.Contains(body, `"state":"42"`) {
		t.Fatalf("post state: %d %s", code, body)
	}
	if code, _ := do(t, ts, "POST", "/api/states/sensor.manual", tok, `{"state":"43"}`); code != 200 {
		t.Fatalf("post existing state: %d", code)
	}
	if code, _ := do(t, ts, "POST", "/api/states/bad", tok, `{"state":"1"}`); code != 400 {
		t.Fatalf("bad entity id: %d", code)
	}
	code, body = do(t, ts, "POST", "/api/template", tok, `{"template":"{{ 21.5 | round(0) }} {{ x }}","variables":{"x":"y"}}`)
	if code != 200 || body != "22 y" {
		t.Fatalf("template: %d %q", code, body)
	}
	code, body = do(t, ts, "GET", "/api/config", tok, "")
	if code != 200 || !strings.Contains(body, `"location_name":"Test"`) || !strings.Contains(body, `"temperature":"°C"`) {
		t.Fatalf("config: %d %s", code, body)
	}
	code, body = do(t, ts, "GET", "/api/services", tok, "")
	if code != 200 || !strings.Contains(body, `"domain":"switch"`) {
		t.Fatalf("services: %d %s", code, body)
	}
	code, body = do(t, ts, "GET", "/api/events", tok, "")
	if code != 200 || !strings.Contains(body, `"event":"state_changed"`) {
		t.Fatalf("events: %d %s", code, body)
	}
}
