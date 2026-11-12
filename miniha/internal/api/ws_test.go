/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/store"
	"github.com/probonopd/miniha/internal/template"
)

type wsClient struct {
	t *testing.T
	c *websocket.Conn
}

func dialWS(t *testing.T, ts *httptest.Server) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/websocket", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	w := &wsClient{t, c}
	if m := w.recv(); m["type"] != "auth_required" || m["ha_version"] != "0.0.1" {
		t.Fatalf("auth_required: %v", m)
	}
	return w
}

func authedWS(t *testing.T, ts *httptest.Server, tok string) *wsClient {
	t.Helper()
	w := dialWS(t, ts)
	w.send(map[string]any{"type": "auth", "access_token": tok})
	if m := w.recv(); m["type"] != "auth_ok" {
		t.Fatalf("auth_ok: %v", m)
	}
	return w
}

func (w *wsClient) send(v any) {
	w.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, b); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsClient) recv() map[string]any {
	w.t.Helper()
	m, err := w.tryRecv(5 * time.Second)
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *wsClient) tryRecv(d time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := w.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// call sends a command and returns its result message.
func (w *wsClient) call(id int, typ string, extra map[string]any) map[string]any {
	w.t.Helper()
	msg := map[string]any{"id": id, "type": typ}
	for k, v := range extra {
		msg[k] = v
	}
	w.send(msg)
	return w.recv()
}

func wantSuccess(t *testing.T, m map[string]any) any {
	t.Helper()
	if m["type"] != "result" || m["success"] != true {
		t.Fatalf("want success, got %v", m)
	}
	return m["result"]
}

func wantError(t *testing.T, m map[string]any, code string) {
	t.Helper()
	e, _ := m["error"].(map[string]any)
	if m["success"] != false || e["code"] != code {
		t.Fatalf("want error %s, got %v", code, m)
	}
}

func TestWSAuthInvalidToken(t *testing.T) {
	ts, _, _ := newTestServer(t)
	w := dialWS(t, ts)
	w.send(map[string]any{"type": "auth", "access_token": "miniha_bogus"})
	m := w.recv()
	if m["type"] != "auth_invalid" || m["message"] != "Invalid access token or password" {
		t.Fatalf("got %v", m)
	}
	if _, err := w.tryRecv(5 * time.Second); err == nil {
		t.Fatal("connection stayed open after auth_invalid")
	}
}

func TestWSAuthMissingToken(t *testing.T) {
	ts, _, _ := newTestServer(t)
	w := dialWS(t, ts)
	w.send(map[string]any{"type": "auth"})
	if m := w.recv(); m["type"] != "auth_invalid" {
		t.Fatalf("got %v", m)
	}
}

func TestWSCommandBeforeAuth(t *testing.T) {
	ts, _, _ := newTestServer(t)
	w := dialWS(t, ts)
	w.send(map[string]any{"id": 1, "type": "get_states"})
	if m := w.recv(); m["type"] != "auth_invalid" {
		t.Fatalf("got %v", m)
	}
}

func TestWSAuthOK(t *testing.T) {
	ts, _, tok := newTestServer(t)
	authedWS(t, ts, tok)
}

func TestWSAuthSessionCookie(t *testing.T) {
	ts, _, _ := newTestServer(t)
	res, err := ts.Client().Post(ts.URL+"/auth/login", "application/json", strings.NewReader(`{"password":"password1"}`))
	if err != nil || len(res.Cookies()) != 1 {
		t.Fatalf("login: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := map[string][]string{"Cookie": {res.Cookies()[0].String()}}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/websocket", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	w := &wsClient{t, c}
	w.recv()
	w.send(map[string]any{"type": "auth"})
	if m := w.recv(); m["type"] != "auth_ok" {
		t.Fatalf("got %v", m)
	}
}

func TestWSAuthOpenMode(t *testing.T) {
	c := core.New()
	a, _ := store.LoadAuth(filepath.Join(t.TempDir(), "auth.json"))
	s := New(c, a, Info{Version: "0.0.1"}, nil, func() map[string]any { return nil }, slog.Default())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	authedWS(t, ts, "anything")
}

func TestWSPing(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	w.send(map[string]any{"id": 1, "type": "ping"})
	if m := w.recv(); m["type"] != "pong" || m["id"] != float64(1) {
		t.Fatalf("got %v", m)
	}
}

func TestWSGetStates(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	res := wantSuccess(t, w.call(1, "get_states", nil)).([]any)
	if len(res) != 1 || res[0].(map[string]any)["entity_id"] != "switch.a" {
		t.Fatalf("got %v", res)
	}
}

func TestWSGetConfigAndServices(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	cfg := wantSuccess(t, w.call(1, "get_config", nil)).(map[string]any)
	if cfg["location_name"] != "Test" {
		t.Fatalf("config: %v", cfg)
	}
	svcs := wantSuccess(t, w.call(2, "get_services", nil)).(map[string]any)
	sw, _ := svcs["switch"].(map[string]any)
	on, _ := sw["turn_on"].(map[string]any)
	if on["name"] == nil || on["fields"] == nil {
		t.Fatalf("services: %v", svcs)
	}
}

func TestWSSubscribeEvents(t *testing.T) {
	ts, c, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_events", map[string]any{"event_type": "state_changed"}))
	c.SetState("switch.a", "on", nil, core.Context{})
	m := w.recv()
	ev, _ := m["event"].(map[string]any)
	if m["type"] != "event" || m["id"] != float64(1) || ev["event_type"] != "state_changed" || ev["origin"] != "LOCAL" {
		t.Fatalf("got %v", m)
	}
	data := ev["data"].(map[string]any)
	if data["entity_id"] != "switch.a" || data["old_state"].(map[string]any)["state"] != "off" || data["new_state"].(map[string]any)["state"] != "on" {
		t.Fatalf("data: %v", data)
	}
	if ev["context"].(map[string]any)["id"] == "" {
		t.Fatalf("context: %v", ev["context"])
	}

	// A custom event of another type is filtered out; the next state
	// change still arrives first.
	c.Fire("other", map[string]any{"x": 1}, core.NewContext())
	c.SetState("switch.a", "off", nil, core.Context{})
	if m := w.recv(); m["event"].(map[string]any)["event_type"] != "state_changed" {
		t.Fatalf("filter: %v", m)
	}

	wantSuccess(t, w.call(2, "unsubscribe_events", map[string]any{"subscription": 1}))
	wantError(t, w.call(3, "unsubscribe_events", map[string]any{"subscription": 1}), "not_found")
	c.SetState("switch.a", "on", nil, core.Context{})
	w.send(map[string]any{"id": 4, "type": "ping"})
	if m := w.recv(); m["type"] != "pong" {
		t.Fatalf("event after unsubscribe: %v", m)
	}
}

func TestWSSubscribeAllEvents(t *testing.T) {
	ts, c, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_events", nil))
	c.Fire("custom", map[string]any{"x": 1}, core.NewContext())
	ev := w.recv()["event"].(map[string]any)
	if ev["event_type"] != "custom" || ev["data"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("got %v", ev)
	}
}

func TestWSSubscribeEntities(t *testing.T) {
	ts, c, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_entities", nil))
	first := w.recv()
	a := first["event"].(map[string]any)["a"].(map[string]any)
	sw := a["switch.a"].(map[string]any)
	if first["id"] != float64(1) || sw["s"] != "off" || sw["a"].(map[string]any)["friendly_name"] != "A" || sw["c"] == "" || sw["lc"] == nil || sw["lu"] == nil {
		t.Fatalf("initial: %v", first)
	}

	c.SetState("switch.a", "on", map[string]any{"friendly_name": "A", "extra": 1}, core.Context{})
	ch := w.recv()["event"].(map[string]any)["c"].(map[string]any)["switch.a"].(map[string]any)
	plus := ch["+"].(map[string]any)
	attrs := plus["a"].(map[string]any)
	if plus["s"] != "on" || attrs["extra"] != float64(1) || len(attrs) != 1 || plus["lu"] == nil {
		t.Fatalf("change: %v", ch)
	}

	c.SetState("switch.a", "on", map[string]any{"friendly_name": "A"}, core.Context{})
	ch = w.recv()["event"].(map[string]any)["c"].(map[string]any)["switch.a"].(map[string]any)
	if _, has := ch["+"].(map[string]any)["s"]; has {
		t.Fatalf("unchanged state was sent: %v", ch)
	}
	removed := ch["-"].(map[string]any)["a"].([]any)
	if len(removed) != 1 || removed[0] != "extra" {
		t.Fatalf("removed attributes: %v", ch)
	}

	c.SetState("sensor.new", "1", nil, core.Context{})
	if add := w.recv()["event"].(map[string]any)["a"].(map[string]any); add["sensor.new"] == nil {
		t.Fatalf("new entity: %v", add)
	}
	c.RemoveState("sensor.new", core.Context{})
	if rm := w.recv()["event"].(map[string]any)["r"].([]any); len(rm) != 1 || rm[0] != "sensor.new" {
		t.Fatalf("removed entity: %v", rm)
	}
}

func TestWSSubscribeEntitiesFilter(t *testing.T) {
	ts, c, tok := newTestServer(t)
	c.SetState("sensor.other", "1", nil, core.Context{})
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_entities", map[string]any{"entity_ids": []string{"switch.a"}}))
	a := w.recv()["event"].(map[string]any)["a"].(map[string]any)
	if len(a) != 1 || a["switch.a"] == nil {
		t.Fatalf("initial: %v", a)
	}
	c.SetState("sensor.other", "2", nil, core.Context{})
	c.SetState("switch.a", "on", nil, core.Context{})
	if ch := w.recv()["event"].(map[string]any)["c"].(map[string]any); ch["switch.a"] == nil || len(ch) != 1 {
		t.Fatalf("filtered change: %v", ch)
	}
}

func TestWSCallService(t *testing.T) {
	ts, c, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	res := wantSuccess(t, w.call(1, "call_service", map[string]any{
		"domain": "switch", "service": "turn_on", "target": map[string]any{"entity_id": "switch.a"},
	})).(map[string]any)
	ctx := res["context"].(map[string]any)
	if ctx["id"] == "" || ctx["parent_id"] != nil || ctx["user_id"] != nil {
		t.Fatalf("context: %v", ctx)
	}
	if v, has := res["response"]; !has || v != nil {
		t.Fatalf("response: %v", res)
	}
	if c.GetState("switch.a").State != "on" {
		t.Fatal("state not changed")
	}
	wantSuccess(t, w.call(2, "call_service", map[string]any{
		"domain": "switch", "service": "turn_off", "service_data": map[string]any{"entity_id": "switch.a"},
	}))
	if c.GetState("switch.a").State != "off" {
		t.Fatal("service_data entity_id ignored")
	}
	wantError(t, w.call(3, "call_service", map[string]any{"domain": "nope", "service": "x"}), "not_found")
	wantError(t, w.call(4, "call_service", map[string]any{"domain": "switch"}), "invalid_format")
}

func TestWSIDReuse(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	w.send(map[string]any{"id": 5, "type": "ping"})
	w.recv()
	wantError(t, w.call(5, "ping", nil), "id_reuse")
	wantError(t, w.call(3, "ping", nil), "id_reuse")
	w.send(map[string]any{"id": 6, "type": "ping"})
	if m := w.recv(); m["type"] != "pong" {
		t.Fatalf("got %v", m)
	}
}

func TestWSInvalidFormat(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	w.send(map[string]any{"type": "ping"})
	wantError(t, w.recv(), "invalid_format")
	w.send(map[string]any{"id": 1.5, "type": "ping"})
	wantError(t, w.recv(), "invalid_format")
}

func TestWSNonObjectClosesConnection(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, []byte("[1,2]")); err != nil {
		t.Fatal(err)
	}
	_, err := w.tryRecv(5 * time.Second)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusUnsupportedData {
		t.Fatalf("want close 1003, got %v", err)
	}
}

func TestWSMessageTooBig(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.c.Write(ctx, websocket.MessageText, []byte(`{"id":1,"type":"ping","pad":"`+strings.Repeat("x", wsMaxMessage)+`"}`))
	if _, err := w.tryRecv(5 * time.Second); err == nil {
		t.Fatal("oversized message accepted")
	}
}

func TestWSUnknownCommand(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantError(t, w.call(1, "no/such/command", nil), "unknown_command")
}

func TestWSAreaRegistry(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	area := wantSuccess(t, w.call(1, "config/area_registry/create", map[string]any{"name": "Kitchen"})).(map[string]any)
	if area["area_id"] != "kitchen" || area["name"] != "Kitchen" {
		t.Fatalf("create: %v", area)
	}
	list := wantSuccess(t, w.call(2, "config/area_registry/list", nil)).([]any)
	if len(list) != 1 || list[0].(map[string]any)["area_id"] != "kitchen" {
		t.Fatalf("list: %v", list)
	}
	upd := wantSuccess(t, w.call(3, "config/area_registry/update", map[string]any{"area_id": "kitchen", "name": "Cookery", "icon": "mdi:stove"})).(map[string]any)
	if upd["name"] != "Cookery" || upd["icon"] != "mdi:stove" {
		t.Fatalf("update: %v", upd)
	}
	wantError(t, w.call(4, "config/area_registry/update", map[string]any{"area_id": "nope", "name": "x"}), "not_found")
	wantSuccess(t, w.call(5, "config/area_registry/delete", map[string]any{"area_id": "kitchen"}))
	if list := wantSuccess(t, w.call(6, "config/area_registry/list", nil)).([]any); len(list) != 0 {
		t.Fatalf("after delete: %v", list)
	}
	wantError(t, w.call(7, "config/area_registry/create", map[string]any{"name": ""}), "home_assistant_error")
}

func TestWSEntityAndDeviceRegistry(t *testing.T) {
	ts, c, tok := newTestServer(t)
	dev, err := c.RegisterDevice(core.DeviceInfo{Identifiers: [][2]string{{"test", "d1"}}, Name: "Dev"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterEntity(core.EntityRegistration{Platform: "test", UniqueID: "u1", Domain: "sensor", SuggestedObjectID: "temp", DeviceID: dev.ID}); err != nil {
		t.Fatal(err)
	}
	w := authedWS(t, ts, tok)
	list := wantSuccess(t, w.call(1, "config/entity_registry/list", nil)).([]any)
	if len(list) != 1 {
		t.Fatalf("entities: %v", list)
	}
	ent := list[0].(map[string]any)
	if ent["id"] != ent["entity_id"] || ent["entity_id"] == "" {
		t.Fatalf("entity shape: %v", ent)
	}
	got := wantSuccess(t, w.call(2, "config/entity_registry/get", map[string]any{"entity_id": ent["entity_id"]})).(map[string]any)
	if got["unique_id"] != "u1" {
		t.Fatalf("get: %v", got)
	}
	wantError(t, w.call(3, "config/entity_registry/get", map[string]any{"entity_id": "sensor.nope"}), "not_found")

	area := wantSuccess(t, w.call(4, "config/area_registry/create", map[string]any{"name": "Hall"})).(map[string]any)
	upd := wantSuccess(t, w.call(5, "config/entity_registry/update", map[string]any{"entity_id": ent["entity_id"], "name": "Hall temp", "area_id": area["area_id"]})).(map[string]any)
	entry := upd["entity_entry"].(map[string]any)
	if entry["name"] != "Hall temp" || entry["area_id"] != "hall" {
		t.Fatalf("update: %v", upd)
	}
	wantError(t, w.call(6, "config/entity_registry/update", map[string]any{"entity_id": ent["entity_id"], "name": 5}), "invalid_format")

	devs := wantSuccess(t, w.call(7, "config/device_registry/list", nil)).([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["id"] != dev.ID {
		t.Fatalf("devices: %v", devs)
	}
	d := wantSuccess(t, w.call(8, "config/device_registry/update", map[string]any{"device_id": dev.ID, "name_by_user": "Mine"})).(map[string]any)
	if d["name_by_user"] != "Mine" {
		t.Fatalf("device update: %v", d)
	}
	wantError(t, w.call(9, "config/device_registry/update", map[string]any{"device_id": "nope"}), "not_found")
}

func TestWSRenderTemplate(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "render_template", map[string]any{"template": "{{ 1 + 2 }} {{ x }}", "variables": map[string]any{"x": "y"}}))
	m := w.recv()
	ev := m["event"].(map[string]any)
	if m["id"] != float64(1) || ev["result"] != "3 y" || ev["listeners"] == nil {
		t.Fatalf("got %v", m)
	}
	wantError(t, w.call(2, "render_template", map[string]any{"template": "{{ 1 +"}), "template_error")
}

// fakeAutomations stands in for the engine: a "states" function over the
// core and a trigger registry the test fires by hand.
type fakeAutomations struct {
	c     *core.Core
	fire  func(map[string]any)
	stops int
}

func (f *fakeAutomations) Summary() []map[string]any { return nil }
func (f *fakeAutomations) Reload() error             { return nil }
func (f *fakeAutomations) SubscribeTrigger(cfg map[string]any, fire func(map[string]any)) (func(), error) {
	if cfg["platform"] == "bogus" {
		return nil, errors.New("unknown trigger platform bogus")
	}
	f.fire = fire
	return func() { f.stops++ }, nil
}
func (f *fakeAutomations) Traces(entityID string) []map[string]any {
	if entityID == "automation.none" {
		return nil
	}
	return []map[string]any{{"run_id": "1", "entity_id": entityID}}
}
func (f *fakeAutomations) ValidateConfig(triggers, conditions, actions any) map[string]any {
	return map[string]any{"triggers": "valid", "actions": actions}
}
func (f *fakeAutomations) TemplateEnv() *template.Env {
	return &template.Env{Globals: map[string]any{"states": template.Func(func(args []any, _ map[string]any) (any, error) {
		if st := f.c.GetState(template.PyStr(args[0])); st != nil {
			return st.State, nil
		}
		return "unknown", nil
	})}}
}

func newAutomationServer(t *testing.T) (*httptest.Server, *core.Core, *fakeAutomations, string) {
	t.Helper()
	c := core.New()
	a, _ := store.LoadAuth(filepath.Join(t.TempDir(), "auth.json"))
	a.SetPassword("password1")
	tok, _, _ := a.CreateToken("test")
	fa := &fakeAutomations{c: c}
	s := New(c, a, Info{Version: "0.0.1"}, nil, func() map[string]any { return nil }, slog.Default())
	s.Automations = fa
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c.SetState("switch.a", "off", nil, core.Context{})
	return ts, c, fa, tok
}

func TestWSRenderTemplateLive(t *testing.T) {
	ts, c, _, tok := newAutomationServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "render_template", map[string]any{"template": "{{ states('switch.a') }}"}))
	if ev := w.recv()["event"].(map[string]any); ev["result"] != "off" {
		t.Fatalf("first: %v", ev)
	}
	c.SetState("switch.a", "on", nil, core.Context{})
	if ev := w.recv()["event"].(map[string]any); ev["result"] != "on" {
		t.Fatalf("live update: %v", ev)
	}
}

func TestWSSubscribeTrigger(t *testing.T) {
	ts, _, fa, tok := newAutomationServer(t)
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_trigger", map[string]any{"trigger": map[string]any{"platform": "state", "entity_id": "switch.a"}}))
	fa.fire(map[string]any{"platform": "state", "entity_id": "switch.a"})
	m := w.recv()
	ev := m["event"].(map[string]any)
	trig := ev["variables"].(map[string]any)["trigger"].(map[string]any)
	if m["id"] != float64(1) || trig["platform"] != "state" || ev["context"].(map[string]any)["id"] == "" {
		t.Fatalf("got %v", m)
	}
	wantError(t, w.call(2, "subscribe_trigger", map[string]any{"trigger": map[string]any{"platform": "bogus"}}), "home_assistant_error")
	wantSuccess(t, w.call(3, "unsubscribe_events", map[string]any{"subscription": 1}))
	if fa.stops != 1 {
		t.Fatalf("trigger not stopped: %d", fa.stops)
	}
}

func TestWSValidateConfig(t *testing.T) {
	ts, _, _, tok := newAutomationServer(t)
	w := authedWS(t, ts, tok)
	res := wantSuccess(t, w.call(1, "validate_config", map[string]any{"triggers": []any{}, "actions": []any{1}})).(map[string]any)
	if res["triggers"] != "valid" {
		t.Fatalf("got %v", res)
	}
}

func TestWSAutomationCommandsWithoutEngine(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	wantError(t, w.call(1, "subscribe_trigger", map[string]any{"trigger": map[string]any{"platform": "state"}}), "unknown_command")
	wantError(t, w.call(2, "validate_config", map[string]any{"triggers": []any{}}), "unknown_command")
}

func TestWSSupportedFeaturesAndCurrentUser(t *testing.T) {
	ts, _, tok := newTestServer(t)
	w := authedWS(t, ts, tok)
	if r := wantSuccess(t, w.call(1, "supported_features", map[string]any{"features": map[string]any{"coalesce_messages": 1}})); r != nil {
		t.Fatalf("got %v", r)
	}
	u := wantSuccess(t, w.call(2, "auth/current_user", nil)).(map[string]any)
	if u["id"] != "admin" || u["is_admin"] != true || u["is_owner"] != true {
		t.Fatalf("got %v", u)
	}
}

func TestWSDisconnectReleasesSubscriptions(t *testing.T) {
	ts, c, tok := newTestServer(t)
	before := c.EventTypes()["state_changed"]
	w := authedWS(t, ts, tok)
	wantSuccess(t, w.call(1, "subscribe_events", map[string]any{"event_type": "state_changed"}))
	wantSuccess(t, w.call(2, "subscribe_entities", nil))
	w.recv()
	if got := c.EventTypes()["state_changed"]; got != before+2 {
		t.Fatalf("listeners while connected: %d (before %d)", got, before)
	}
	w.c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for c.EventTypes()["state_changed"] != before {
		if time.Now().After(deadline) {
			t.Fatalf("listeners leaked after disconnect: %d", c.EventTypes()["state_changed"])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// recorded sets up a server with a real recorder holding a few states:
// a temperature sensor over three finished hours and a switch whose
// attributes change once without a state change.
func recorded(t *testing.T) (*extraEnv, time.Time) {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := newExtraEnv(t)
	rec, now := e.withRecorder(t, t0)
	temp := map[string]any{"state_class": "measurement", "unit_of_measurement": "C", "friendly_name": "Temp"}
	set := func(at time.Duration, id, state string, attrs map[string]any) {
		*now = t0.Add(at)
		if err := e.c.SetState(id, state, attrs, core.Context{}); err != nil {
			t.Fatal(err)
		}
	}
	set(0, "sensor.temp", "10", temp)
	set(30*time.Minute, "sensor.temp", "20", temp)
	set(2*time.Hour+15*time.Minute, "sensor.temp", "30", temp)
	set(time.Minute, "switch.s", "on", map[string]any{"v": 1, "friendly_name": "Sw"})
	set(2*time.Minute, "switch.s", "on", map[string]any{"v": 2, "friendly_name": "Sw"})
	set(3*time.Minute, "switch.s", "off", map[string]any{"v": 2, "friendly_name": "Sw"})
	*now = t0.Add(3*time.Hour + 5*time.Minute)
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	return e, t0
}

const rfc = time.RFC3339

func TestWSNotificationsGetAndSubscribe(t *testing.T) {
	e := newExtraEnv(t)
	e.c.Notify("first", "T1", "M1")
	w := authedWS(t, e.ts, e.tok)
	got := wantSuccess(t, w.call(1, "persistent_notification/get", nil)).([]any)
	if len(got) != 1 || got[0].(map[string]any)["notification_id"] != "first" {
		t.Fatalf("get: %v", got)
	}
	wantSuccess(t, w.call(2, "persistent_notification/subscribe", nil))
	ev := w.recv()["event"].(map[string]any)
	if ev["type"] != "added" || len(ev["notifications"].([]any)) != 1 {
		t.Fatalf("initial: %v", ev)
	}
	e.c.Notify("second", "T2", "M2")
	ev = w.recv()["event"].(map[string]any)
	if ev["type"] != "added" || len(ev["notifications"].([]any)) != 2 {
		t.Fatalf("added: %v", ev)
	}
	e.c.Dismiss("first")
	ev = w.recv()["event"].(map[string]any)
	list := ev["notifications"].([]any)
	if ev["type"] != "removed" || len(list) != 1 || list[0].(map[string]any)["notification_id"] != "second" {
		t.Fatalf("removed: %v", ev)
	}
	e.c.DismissAll()
	ev = w.recv()["event"].(map[string]any)
	if ev["type"] != "removed" || len(ev["notifications"].([]any)) != 0 {
		t.Fatalf("dismiss all: %v", ev)
	}
}

func TestWSHistoryDuringPeriod(t *testing.T) {
	e, t0 := recorded(t)
	w := authedWS(t, e.ts, e.tok)
	args := map[string]any{"start_time": t0.Format(rfc), "entity_ids": []any{"switch.s"}}
	res := wantSuccess(t, w.call(1, "history/history_during_period", args)).(map[string]any)
	list := res["switch.s"].([]any)
	// Significant changes only (HA's default) folds the attribute-only update.
	if len(res) != 1 || len(list) != 2 {
		t.Fatalf("significant: %v", res)
	}
	first := list[0].(map[string]any)
	if first["s"] != "on" || first["lu"] != float64(t0.Add(time.Minute).Unix()) || first["lc"] != nil || first["a"].(map[string]any)["v"] != float64(1) {
		t.Fatalf("first item: %v", first)
	}
	if list[1].(map[string]any)["s"] != "off" {
		t.Fatalf("second item: %v", list[1])
	}

	args["significant_changes_only"] = false
	list = wantSuccess(t, w.call(2, "history/history_during_period", args)).(map[string]any)["switch.s"].([]any)
	attrOnly := list[1].(map[string]any)
	if len(list) != 3 || attrOnly["s"] != "on" || attrOnly["lu"] != float64(t0.Add(2*time.Minute).Unix()) ||
		attrOnly["lc"] != float64(t0.Add(time.Minute).Unix()) || attrOnly["a"].(map[string]any)["v"] != float64(2) {
		t.Fatalf("attribute-only item: %v", list)
	}

	args["minimal_response"] = true
	list = wantSuccess(t, w.call(3, "history/history_during_period", args)).(map[string]any)["switch.s"].([]any)
	if _, has := list[0].(map[string]any)["a"]; has {
		t.Fatalf("minimal response has attributes: %v", list[0])
	}
	delete(args, "minimal_response")
	args["no_attributes"] = true
	list = wantSuccess(t, w.call(4, "history/history_during_period", args)).(map[string]any)["switch.s"].([]any)
	if _, has := list[0].(map[string]any)["a"]; has {
		t.Fatalf("no_attributes has attributes: %v", list[0])
	}

	// Without entity_ids every recorded entity is returned; end_time bounds the window.
	res = wantSuccess(t, w.call(5, "history/history_during_period", map[string]any{
		"start_time": t0.Format(rfc), "end_time": t0.Add(40 * time.Minute).Format(rfc)})).(map[string]any)
	if len(res) != 2 || len(res["sensor.temp"].([]any)) != 2 {
		t.Fatalf("all entities: %v", res)
	}
	wantError(t, w.call(6, "history/history_during_period", map[string]any{"start_time": "garbage"}), "invalid_format")
	wantError(t, w.call(7, "history/history_during_period", map[string]any{}), "invalid_format")
}

func TestWSStatisticsDuringPeriod(t *testing.T) {
	e, t0 := recorded(t)
	w := authedWS(t, e.ts, e.tok)
	res := wantSuccess(t, w.call(1, "recorder/statistics_during_period", map[string]any{
		"start_time": t0.Format(rfc), "statistic_ids": []any{"sensor.temp"}, "period": "hour"})).(map[string]any)
	rows := res["sensor.temp"].([]any)
	if len(res) != 1 || len(rows) != 3 {
		t.Fatalf("hourly: %v", res)
	}
	r0 := rows[0].(map[string]any)
	if r0["start"] != float64(t0.UnixMilli()) || r0["end"] != float64(t0.Add(time.Hour).UnixMilli()) ||
		r0["mean"] != float64(15) || r0["min"] != float64(10) || r0["max"] != float64(20) || r0["sum"] != nil || r0["state"] != nil {
		t.Fatalf("first hour: %v", r0)
	}

	res = wantSuccess(t, w.call(2, "recorder/statistics_during_period", map[string]any{
		"start_time": t0.Format(rfc), "end_time": t0.Add(time.Hour).Format(rfc), "statistic_ids": []any{"sensor.temp"},
		"period": "hour", "types": []any{"mean", "max"}})).(map[string]any)
	rows = res["sensor.temp"].([]any)
	if len(rows) != 1 || len(rows[0].(map[string]any)) != 4 || rows[0].(map[string]any)["max"] != float64(20) {
		t.Fatalf("types and end_time: %v", res)
	}
	if _, has := rows[0].(map[string]any)["min"]; has {
		t.Fatalf("unrequested type returned: %v", rows[0])
	}

	args := map[string]any{"start_time": t0.Format(rfc), "statistic_ids": []any{"sensor.temp"}, "period": "day"}
	if rows := wantSuccess(t, w.call(3, "recorder/statistics_during_period", args)).(map[string]any)["sensor.temp"].([]any); len(rows) == 0 {
		t.Fatal("day aggregation returned nothing")
	}
	args["period"] = "year"
	wantError(t, w.call(4, "recorder/statistics_during_period", args), "invalid_format")
	args["period"] = "hour"
	args["types"] = []any{"change"}
	wantError(t, w.call(5, "recorder/statistics_during_period", args), "not_supported")
	delete(args, "types")
	delete(args, "statistic_ids")
	wantError(t, w.call(6, "recorder/statistics_during_period", args), "invalid_format")

	meta := wantSuccess(t, w.call(7, "recorder/get_statistics_metadata", nil)).([]any)
	if len(meta) != 1 || meta[0].(map[string]any)["statistic_id"] != "sensor.temp" || meta[0].(map[string]any)["has_mean"] != true ||
		meta[0].(map[string]any)["unit_of_measurement"] != "C" || meta[0].(map[string]any)["source"] != "recorder" {
		t.Fatalf("metadata: %v", meta)
	}
	meta = wantSuccess(t, w.call(8, "recorder/get_statistics_metadata", map[string]any{"statistic_ids": []any{"sensor.other"}})).([]any)
	if len(meta) != 0 {
		t.Fatalf("filtered metadata: %v", meta)
	}
}

func TestWSLogbookEvents(t *testing.T) {
	e, t0 := recorded(t)
	w := authedWS(t, e.ts, e.tok)
	list := wantSuccess(t, w.call(1, "logbook/get_events", map[string]any{"start_time": t0.Format(rfc)})).([]any)
	// 3 sensor changes + switch on, off (the attribute-only update is not an event).
	if len(list) != 5 {
		t.Fatalf("all: %v", list)
	}
	for i := 1; i < len(list); i++ {
		if list[i].(map[string]any)["when"].(float64) < list[i-1].(map[string]any)["when"].(float64) {
			t.Fatalf("not sorted: %v", list)
		}
	}
	list = wantSuccess(t, w.call(2, "logbook/get_events", map[string]any{
		"start_time": t0.Format(rfc), "entity_ids": []any{"switch.s"}})).([]any)
	if len(list) != 2 {
		t.Fatalf("filtered: %v", list)
	}
	on := list[0].(map[string]any)
	if on["name"] != "Sw" || on["entity_id"] != "switch.s" || on["state"] != "on" || on["message"] != "turned on" || on["when"] != float64(t0.Add(time.Minute).Unix()) {
		t.Fatalf("entry: %v", on)
	}
	wantError(t, w.call(3, "logbook/get_events", map[string]any{}), "invalid_format")
}

func TestWSHistoryCommandsWithoutRecorder(t *testing.T) {
	e := newExtraEnv(t)
	w := authedWS(t, e.ts, e.tok)
	start := map[string]any{"start_time": "2026-01-01T00:00:00Z", "statistic_ids": []any{"sensor.t"}, "period": "hour", "entity_ids": []any{"sensor.t"}}
	for i, typ := range []string{"recorder/statistics_during_period", "recorder/get_statistics_metadata", "history/history_during_period", "logbook/get_events"} {
		m := w.call(i+1, typ, start)
		wantError(t, m, "not_supported")
		if m["error"].(map[string]any)["message"] == "" {
			t.Errorf("%s: no message", typ)
		}
	}
	info := wantSuccess(t, w.call(10, "recorder/info", nil)).(map[string]any)
	if info["recording"] != false || info["thread_running"] != true || info["migration_in_progress"] != false || info["backlog"] != float64(0) {
		t.Fatalf("info without recorder: %v", info)
	}
	e.s.History = &statsHistory{}
	info = wantSuccess(t, w.call(11, "recorder/info", nil)).(map[string]any)
	if info["recording"] != true {
		t.Fatalf("info with recorder: %v", info)
	}
}

func TestWSLovelaceConfig(t *testing.T) {
	e := newExtraEnv(t)
	w := authedWS(t, e.ts, e.tok)
	wantError(t, w.call(1, "lovelace/config", nil), "config_not_found")
	if code, body := e.do(t, "PUT", "/api/miniha/files/ui-lovelace.yaml", goodDashboard); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	cfg := wantSuccess(t, w.call(2, "lovelace/config", map[string]any{"url_path": nil})).(map[string]any)
	if cfg["title"] != "Home" || len(cfg["views"].([]any)) != 1 {
		t.Fatalf("config: %v", cfg)
	}
	wantError(t, w.call(3, "lovelace/config", map[string]any{"url_path": "other"}), "config_not_found")
	e.do(t, "PUT", "/api/miniha/files/ui-lovelace.yaml", "views: 5\n")
	wantError(t, w.call(4, "lovelace/config", nil), "invalid_config")
}

func TestWSFrontendSupportCommands(t *testing.T) {
	e := newExtraEnv(t)
	w := authedWS(t, e.ts, e.tok)
	if res := wantSuccess(t, w.call(1, "lovelace/resources", nil)).([]any); len(res) != 0 {
		t.Fatalf("resources: %v", res)
	}
	themes := wantSuccess(t, w.call(2, "frontend/get_themes", nil)).(map[string]any)
	if themes["default_theme"] != "default" || len(themes["themes"].(map[string]any)) != 0 {
		t.Fatalf("themes: %v", themes)
	}
	if res := wantSuccess(t, w.call(3, "system_health/info", nil)).(map[string]any); len(res) != 0 {
		t.Fatalf("system health: %v", res)
	}
}

func TestWSCheckConfig(t *testing.T) {
	e := newExtraEnv(t)
	w := authedWS(t, e.ts, e.tok)
	res := wantSuccess(t, w.call(1, "config/core/check_config", nil)).(map[string]any)
	if res["result"] != "valid" || res["errors"] != nil {
		t.Fatalf("no files: %v", res)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "automations.yaml"), []byte("- alias: Fine\n  trigger: []\n  action: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := wantSuccess(t, w.call(2, "config/core/check_config", nil)).(map[string]any); res["result"] != "valid" {
		t.Fatalf("valid file: %v", res)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "scripts.yaml"), []byte("- 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = wantSuccess(t, w.call(3, "config/core/check_config", nil)).(map[string]any)
	msg, _ := res["errors"].(string)
	if res["result"] != "invalid" || !strings.Contains(msg, "scripts.yaml") {
		t.Fatalf("invalid file: %v", res)
	}
}
