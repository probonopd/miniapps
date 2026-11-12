/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package virtual

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// --- test environment ---

type testEnv struct{ c *core.Core }

type tstate struct{ s *core.State }

func (o tstate) String() string { return o.s.State }

func (o tstate) TemplateAttr(name string) (any, bool) {
	switch name {
	case "state":
		return o.s.State, true
	case "entity_id":
		return o.s.EntityID, true
	case "attributes":
		return template.FromGo(o.s.Attributes), true
	}
	return nil, false
}

type tstates struct{ c *core.Core }

func (s tstates) TemplateCall(args []any, _ map[string]any) (any, error) {
	if st := s.c.GetState(fmt.Sprint(args[0])); st != nil {
		return st.State, nil
	}
	return core.StateUnknown, nil
}

func (s tstates) TemplateAttr(name string) (any, bool) {
	if st := s.c.GetState(name); st != nil {
		return tstate{st}, true
	}
	return nil, false
}

func (e testEnv) TemplateEnv() *template.Env {
	return &template.Env{Globals: map[string]any{
		"states": tstates{e.c},
		"is_state": template.Func(func(args []any, _ map[string]any) (any, error) {
			st := e.c.GetState(fmt.Sprint(args[0]))
			return st != nil && st.State == fmt.Sprint(args[1]), nil
		}),
	}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stateOf(c *core.Core, id string) string {
	if st := c.GetState(id); st != nil {
		return st.State
	}
	return "<none>"
}

func waitState(t *testing.T, c *core.Core, id, want string) {
	t.Helper()
	waitFor(t, id+" = "+want, func() bool { return stateOf(c, id) == want })
}

func mustSet(t *testing.T, c *core.Core, id, state string, attrs map[string]any) {
	t.Helper()
	if err := c.SetState(id, state, attrs, core.Context{}); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, c *core.Core, domain, service string, data map[string]any) {
	t.Helper()
	if _, err := c.CallService(context.Background(), domain, service, data, core.Context{}); err != nil {
		t.Fatalf("%s.%s: %v", domain, service, err)
	}
}

// fakeOnOff stands in for a real switch or light: it records the service
// data and moves its own state like a device would.
type fakeOnOff struct {
	c     *core.Core
	id    string
	mu    sync.Mutex
	attrs map[string]any
	ons   []map[string]any
	offs  int
}

func addOnOff(c *core.Core, id, state string, attrs map[string]any) *fakeOnOff {
	f := &fakeOnOff{c: c, id: id, attrs: attrs}
	if f.attrs == nil {
		f.attrs = map[string]any{}
	}
	c.SetState(id, state, f.attrs, core.Context{})
	c.RegisterController(id, f)
	return f
}

func (f *fakeOnOff) TurnOn(ctx context.Context, data map[string]any) error {
	f.mu.Lock()
	f.ons = append(f.ons, data)
	if b, ok := data["brightness"]; ok {
		f.attrs["brightness"] = b
	}
	attrs := map[string]any{}
	for k, v := range f.attrs {
		attrs[k] = v
	}
	f.mu.Unlock()
	return f.c.SetState(f.id, core.StateOn, attrs, core.ContextFrom(ctx))
}

func (f *fakeOnOff) TurnOff(ctx context.Context, _ map[string]any) error {
	f.mu.Lock()
	f.offs++
	attrs := map[string]any{}
	for k, v := range f.attrs {
		if k != "brightness" {
			attrs[k] = v
		}
	}
	f.mu.Unlock()
	return f.c.SetState(f.id, core.StateOff, attrs, core.ContextFrom(ctx))
}

func (f *fakeOnOff) onCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ons)
}

func (f *fakeOnOff) lastOn() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ons[len(f.ons)-1]
}

// recorder registers test.record, which stores the data of each call.
func recorder(c *core.Core) func() []map[string]any {
	var mu sync.Mutex
	var calls []map[string]any
	c.RegisterService("test", "record", func(_ context.Context, call core.ServiceCall) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call.Data)
		return nil
	}, core.ServiceDesc{Name: "Record"})
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any{}, calls...)
	}
}

func newTemplates(t *testing.T, c *core.Core, items ...map[string]any) *Templates {
	t.Helper()
	tp, err := NewTemplates(c, testEnv{c}, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tp.Stop)
	return tp
}

func newGroups(t *testing.T, c *core.Core, old map[string]any, platforms map[string][]map[string]any) *Groups {
	t.Helper()
	g, err := NewGroups(c, old, platforms, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Stop)
	return g
}

// --- template entities ---

func TestTemplateSensorFollowsSource(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.src", "10", nil)
	newTemplates(t, c, map[string]any{"sensor": []any{map[string]any{
		"name":                "Double",
		"unique_id":           "double",
		"state":               "{{ states('sensor.src') | float(0) * 2 }}",
		"unit_of_measurement": "W",
		"device_class":        "power",
		"state_class":         "measurement",
		"icon":                "{% if states('sensor.src') | float(0) > 15 %}mdi:flash{% else %}mdi:flash-off{% endif %}",
		"attributes":          map[string]any{"source": "{{ states('sensor.src') }}", "static": 5},
	}}})
	st := c.GetState("sensor.double")
	if st == nil || st.State != "20.0" {
		t.Fatalf("initial: %+v", st)
	}
	if st.Attributes["unit_of_measurement"] != "W" || st.Attributes["device_class"] != "power" || st.Attributes["state_class"] != "measurement" ||
		st.Attributes["icon"] != "mdi:flash-off" || st.Attributes["friendly_name"] != "Double" || st.Attributes["static"] != int64(5) {
		t.Fatalf("attributes: %+v", st.Attributes)
	}
	if st.Attributes["source"] != int64(10) {
		t.Fatalf("typed attribute: %#v", st.Attributes["source"])
	}
	mustSet(t, c, "sensor.src", "21", nil)
	waitState(t, c, "sensor.double", "42.0")
	if got := c.GetState("sensor.double").Attributes["icon"]; got != "mdi:flash" {
		t.Fatalf("icon not re-rendered: %v", got)
	}
	if e := c.EntityByUniqueID("template", "double"); e == nil || e.EntityID != "sensor.double" {
		t.Fatalf("registry: %+v", e)
	}
}

func TestTemplateSensorAvailabilityAndErrors(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.src", "1", nil)
	mustSet(t, c, "input_boolean.ok", "on", nil)
	newTemplates(t, c, map[string]any{"sensor": []any{map[string]any{
		"name": "Guarded", "state": "{{ states('sensor.src') }}",
		"availability": "{{ is_state('input_boolean.ok', 'on') }}",
	}}})
	if s := stateOf(c, "sensor.guarded"); s != "1" {
		t.Fatalf("got %s", s)
	}
	mustSet(t, c, "input_boolean.ok", "off", nil)
	waitState(t, c, "sensor.guarded", core.StateUnavailable)
	mustSet(t, c, "input_boolean.ok", "on", nil)
	waitState(t, c, "sensor.guarded", "1")
}

func TestTemplateNoRewriteWhenUnchanged(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.src", "1", nil)
	newTemplates(t, c, map[string]any{"sensor": []any{map[string]any{"name": "Copy", "state": "{{ states('sensor.src') }}"}}})
	var mu sync.Mutex
	copies := 0
	c.Subscribe("state_changed", func(ev core.Event) {
		if ev.Data["entity_id"] == "sensor.copy" {
			mu.Lock()
			copies++
			mu.Unlock()
		}
	})
	for i := 0; i < 5; i++ {
		mustSet(t, c, "sensor.noise", fmt.Sprint(i), nil)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if copies != 0 {
		t.Fatalf("unchanged template entity was written %d times", copies)
	}
}

func TestTemplateBinarySensorDelayOff(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.motion", "off", nil)
	newTemplates(t, c, map[string]any{"binary_sensor": []any{map[string]any{
		"name": "Occupied", "state": "{{ is_state('sensor.motion', 'on') }}",
		"delay_off": map[string]any{"milliseconds": 200}, "device_class": "occupancy",
	}}})
	if s := stateOf(c, "binary_sensor.occupied"); s != "off" {
		t.Fatalf("initial %s", s)
	}
	mustSet(t, c, "sensor.motion", "on", nil)
	waitState(t, c, "binary_sensor.occupied", "on")
	if c.GetState("binary_sensor.occupied").Attributes["device_class"] != "occupancy" {
		t.Fatalf("device_class missing")
	}
	start := time.Now()
	mustSet(t, c, "sensor.motion", "off", nil)
	time.Sleep(60 * time.Millisecond)
	if s := stateOf(c, "binary_sensor.occupied"); s != "on" {
		t.Fatalf("turned off before delay_off: %s", s)
	}
	waitState(t, c, "binary_sensor.occupied", "off")
	if time.Since(start) < 190*time.Millisecond {
		t.Fatalf("delay_off honored for only %v", time.Since(start))
	}
	// Flapping back before the delay ends keeps the sensor on.
	mustSet(t, c, "sensor.motion", "on", nil)
	waitState(t, c, "binary_sensor.occupied", "on")
	mustSet(t, c, "sensor.motion", "off", nil)
	time.Sleep(50 * time.Millisecond)
	mustSet(t, c, "sensor.motion", "on", nil)
	time.Sleep(300 * time.Millisecond)
	if s := stateOf(c, "binary_sensor.occupied"); s != "on" {
		t.Fatalf("pending delay not cancelled: %s", s)
	}
}

func TestTemplateBinarySensorTruthiness(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.v", "yes", nil)
	newTemplates(t, c, map[string]any{"binary_sensor": []any{map[string]any{"name": "Truthy", "state": "{{ states('sensor.v') }}"}}})
	for in, want := range map[string]string{"yes": "on", "No": "off", "1": "on", "0": "off", "True": "on", "off": "off", "unknown": "unknown"} {
		mustSet(t, c, "sensor.v", in, nil)
		waitState(t, c, "binary_sensor.truthy", want)
	}
}

func TestTemplateSwitchCallsRealSwitch(t *testing.T) {
	c := core.New()
	real := addOnOff(c, "switch.real", "off", nil)
	newTemplates(t, c, map[string]any{"switch": []any{map[string]any{
		"name": "Proxy", "unique_id": "proxy",
		"state":    "{{ is_state('switch.real', 'on') }}",
		"turn_on":  []any{map[string]any{"service": "switch.turn_on", "target": map[string]any{"entity_id": "switch.real"}}},
		"turn_off": []any{map[string]any{"action": "switch.turn_off", "target": map[string]any{"entity_id": "switch.real"}}},
	}}})
	if s := stateOf(c, "switch.proxy"); s != "off" {
		t.Fatalf("initial %s", s)
	}
	call(t, c, "switch", "turn_on", map[string]any{"entity_id": "switch.proxy"})
	if real.onCount() != 1 {
		t.Fatalf("real switch not turned on")
	}
	waitState(t, c, "switch.proxy", "on")
	call(t, c, "switch", "turn_off", map[string]any{"entity_id": "switch.proxy"})
	if real.offs != 1 {
		t.Fatalf("real switch not turned off")
	}
	waitState(t, c, "switch.proxy", "off")
}

func TestTemplateSwitchOptimistic(t *testing.T) {
	c := core.New()
	rec := recorder(c)
	newTemplates(t, c, map[string]any{"switch": []any{map[string]any{
		"name":     "Blind",
		"turn_on":  map[string]any{"service": "test.record", "data": map[string]any{"cmd": "on"}},
		"turn_off": map[string]any{"service": "test.record", "data": map[string]any{"cmd": "off"}},
	}}})
	if s := stateOf(c, "switch.blind"); s != "unknown" {
		t.Fatalf("initial %s", s)
	}
	call(t, c, "switch", "turn_on", map[string]any{"entity_id": "switch.blind"})
	if s := stateOf(c, "switch.blind"); s != "on" {
		t.Fatalf("optimistic on: %s", s)
	}
	if got := c.GetState("switch.blind").Attributes["assumed_state"]; got != true {
		t.Fatalf("assumed_state: %v", got)
	}
	call(t, c, "switch", "turn_off", map[string]any{"entity_id": "switch.blind"})
	if s := stateOf(c, "switch.blind"); s != "off" || len(rec()) != 2 {
		t.Fatalf("optimistic off: %s, %v", s, rec())
	}
}

func TestTemplateNumberSelectButton(t *testing.T) {
	c := core.New()
	rec := recorder(c)
	mustSet(t, c, "sensor.mode", "eco", nil)
	newTemplates(t, c, map[string]any{
		"number": []any{map[string]any{
			"name": "Target", "min": "5", "max": "{{ 30 }}", "step": 0.5,
			"set_value": []any{map[string]any{"service": "test.record", "data": map[string]any{"kind": "number", "v": "{{ value }}", "twice": "{{ value * 2 }}"}}},
		}},
		"select": []any{map[string]any{
			"name": "Mode", "options": "{{ ['eco', 'comfort'] }}", "state": "{{ states('sensor.mode') }}",
			"select_option": []any{map[string]any{"service": "test.record", "data": map[string]any{"kind": "select", "o": "{{ option }}"}}},
		}},
		"button": []any{map[string]any{
			"name":  "Ping",
			"press": []any{map[string]any{"service": "test.record", "data": map[string]any{"kind": "button"}}},
		}},
	})
	if s := stateOf(c, "number.target"); s != "unknown" {
		t.Fatalf("number initial %s", s)
	}
	call(t, c, "number", "set_value", map[string]any{"entity_id": "number.target", "value": 21.5})
	if s := stateOf(c, "number.target"); s != "21.5" {
		t.Fatalf("number state %s", s)
	}
	attrs := c.GetState("number.target").Attributes
	if attrs["min"] != 5.0 || attrs["max"] != 30.0 || attrs["step"] != 0.5 {
		t.Fatalf("number attrs %+v", attrs)
	}
	if _, err := c.CallService(context.Background(), "number", "set_value", map[string]any{"entity_id": "number.target", "value": 99}, core.Context{}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out of range accepted: %v", err)
	}

	if s := stateOf(c, "select.mode"); s != "eco" {
		t.Fatalf("select state %s", s)
	}
	if opts := c.GetState("select.mode").Attributes["options"]; !reflect.DeepEqual(opts, []any{"eco", "comfort"}) {
		t.Fatalf("options %#v", opts)
	}
	call(t, c, "select", "select_option", map[string]any{"entity_id": "select.mode", "option": "comfort"})
	if _, err := c.CallService(context.Background(), "select", "select_option", map[string]any{"entity_id": "select.mode", "option": "party"}, core.Context{}); err == nil {
		t.Fatalf("invalid option accepted")
	}

	if s := stateOf(c, "button.ping"); s != "unknown" {
		t.Fatalf("button initial %s", s)
	}
	call(t, c, "button", "press", map[string]any{"entity_id": "button.ping"})
	if s := stateOf(c, "button.ping"); !strings.Contains(s, "T") || !strings.HasSuffix(s, "+00:00") {
		t.Fatalf("button state %s", s)
	}

	calls := rec()
	if len(calls) != 3 {
		t.Fatalf("recorded %v", calls)
	}
	if calls[0]["v"] != 21.5 || calls[0]["twice"] != 43.0 {
		t.Fatalf("value variable: %#v", calls[0])
	}
	if calls[1]["o"] != "comfort" || calls[2]["kind"] != "button" {
		t.Fatalf("calls %v", calls)
	}
}

func TestTemplateUsesThis(t *testing.T) {
	c := core.New()
	newTemplates(t, c, map[string]any{"sensor": []any{map[string]any{
		"name": "Self", "state": "x", "attributes": map[string]any{"me": "{{ this.entity_id }}"},
	}}})
	waitFor(t, "this attribute", func() bool {
		st := c.GetState("sensor.self")
		return st != nil && st.Attributes["me"] == "sensor.self"
	})
}

func TestTemplateConfigErrors(t *testing.T) {
	c := core.New()
	cases := map[string]map[string]any{
		"trigger":        {"trigger": []any{map[string]any{"platform": "time"}}, "sensor": []any{}},
		"light":          {"light": []any{map[string]any{"name": "x"}}},
		"missing state":  {"sensor": []any{map[string]any{"name": "x"}}},
		"missing name":   {"sensor": []any{map[string]any{"state": "1"}}},
		"unknown option": {"sensor": []any{map[string]any{"name": "x", "state": "1", "bogus": 1}}},
		"bad template":   {"sensor": []any{map[string]any{"name": "x", "state": "{{ 1 +"}}},
		"flow control": {"switch": []any{map[string]any{"name": "x",
			"turn_on":  []any{map[string]any{"delay": 5}},
			"turn_off": []any{map[string]any{"service": "a.b"}}}}},
		"duplicate": {"sensor": []any{
			map[string]any{"name": "a", "unique_id": "u", "state": "1"},
			map[string]any{"name": "b", "unique_id": "u", "state": "1"}}},
	}
	for name, item := range cases {
		if tp, err := NewTemplates(c, testEnv{c}, []map[string]any{item}, nil); err == nil {
			tp.Stop()
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPyLiteral(t *testing.T) {
	cases := map[string]any{
		"['a', 'b']":       []any{"a", "b"},
		`["a", 1, 2.5]`:    []any{"a", int64(1), 2.5},
		"('x',)":           []any{"x"},
		"{'k': [1, None]}": map[string]any{"k": []any{int64(1), nil}},
		"[True, False]":    []any{true, false},
		`['it\'s']`:        []any{"it's"},
		"[]":               []any{},
		"21":               int64(21),
		"  None ":          nil,
		"[unterminated":    "[unterminated",
		"[1, 2] trailing":  "[1, 2] trailing",
		"hello world":      "hello world",
	}
	for in, want := range cases {
		if got := parseResult(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %#v want %#v", in, got, want)
		}
	}
}

// --- groups ---

func lightAttrs(brightness int, modes ...string) map[string]any {
	a := map[string]any{"brightness": brightness}
	if len(modes) > 0 {
		a["supported_color_modes"] = anyList(modes)
		a["color_mode"] = modes[0]
	}
	return a
}

func TestLightGroup(t *testing.T) {
	c := core.New()
	a := addOnOff(c, "light.a", "on", lightAttrs(100, "hs"))
	b := addOnOff(c, "light.b", "on", lightAttrs(200, "color_temp"))
	c.SetState("light.a", "on", map[string]any{"brightness": 100, "supported_color_modes": []any{"hs", "color_temp"}, "color_mode": "hs", "hs_color": []any{10.0, 50.0}, "supported_features": 4}, core.Context{})
	newGroups(t, c, nil, map[string][]map[string]any{"light": {
		{"platform": "group", "name": "Both", "unique_id": "both", "entities": []any{"light.a", "light.b"}},
		{"platform": "mqtt", "name": "Not mine", "foo": "bar"},
	}})
	st := c.GetState("light.both")
	if st == nil || st.State != "on" {
		t.Fatalf("group state: %+v", st)
	}
	if st.Attributes["brightness"] != 150 || st.Attributes["color_mode"] != "hs" ||
		!reflect.DeepEqual(st.Attributes["supported_color_modes"], []any{"color_temp", "hs"}) ||
		!reflect.DeepEqual(st.Attributes["hs_color"], []any{10.0, 50.0}) || st.Attributes["supported_features"] != 4 ||
		!reflect.DeepEqual(st.Attributes["entity_id"], []any{"light.a", "light.b"}) {
		t.Fatalf("attributes: %+v", st.Attributes)
	}
	if c.GetState("light.not_mine") != nil {
		t.Fatalf("foreign platform handled")
	}

	call(t, c, "light", "turn_on", map[string]any{"entity_id": "light.both", "brightness": 50})
	if a.onCount() != 1 || b.onCount() != 1 || a.lastOn()["brightness"] != 50 || b.lastOn()["brightness"] != 50 {
		t.Fatalf("not forwarded: %v %v", a.ons, b.ons)
	}
	if _, bad := a.lastOn()["entity_id"].([]any); !bad {
		t.Fatalf("member call must target the members: %v", a.lastOn())
	}
	if st := c.GetState("light.both"); st.Attributes["brightness"] != 50 {
		t.Fatalf("group brightness after turn_on: %+v", st.Attributes)
	}
	call(t, c, "light", "turn_off", map[string]any{"entity_id": "light.both"})
	if a.offs != 1 || b.offs != 1 {
		t.Fatalf("turn_off not forwarded")
	}
	if s := stateOf(c, "light.both"); s != "off" {
		t.Fatalf("group %s", s)
	}
	mustSet(t, c, "light.a", "on", lightAttrs(10))
	if s := stateOf(c, "light.both"); s != "on" {
		t.Fatalf("group any-on: %s", s)
	}
}

func TestLightGroupAll(t *testing.T) {
	c := core.New()
	addOnOff(c, "light.a", "on", nil)
	addOnOff(c, "light.b", "off", nil)
	newGroups(t, c, nil, map[string][]map[string]any{"light": {
		{"platform": "group", "name": "Strict", "entities": "light.a, light.b", "all": true},
	}})
	if s := stateOf(c, "light.strict"); s != "off" {
		t.Fatalf("all: %s", s)
	}
	mustSet(t, c, "light.b", "on", nil)
	if s := stateOf(c, "light.strict"); s != "on" {
		t.Fatalf("all: %s", s)
	}
	mustSet(t, c, "light.b", "unavailable", nil)
	if s := stateOf(c, "light.strict"); s != "on" {
		t.Fatalf("unavailable member voted: %s", s)
	}
	mustSet(t, c, "light.a", "unavailable", nil)
	if s := stateOf(c, "light.strict"); s != "unavailable" {
		t.Fatalf("all unavailable: %s", s)
	}
}

func TestSensorGroups(t *testing.T) {
	c := core.New()
	mustSet(t, c, "sensor.a", "10", map[string]any{"unit_of_measurement": "C"})
	mustSet(t, c, "sensor.b", "20", map[string]any{"unit_of_measurement": "C"})
	mustSet(t, c, "sensor.c", "60", map[string]any{"unit_of_measurement": "C"})
	var entries []map[string]any
	for _, typ := range []string{"mean", "min", "max", "median", "sum", "range", "product", "count", "last"} {
		entries = append(entries, map[string]any{"platform": "group", "name": "G " + typ, "type": typ, "entities": []any{"sensor.a", "sensor.b", "sensor.c"}})
	}
	entries = append(entries,
		map[string]any{"platform": "group", "name": "Strict", "type": "sum", "entities": []any{"sensor.a", "sensor.text"}},
		map[string]any{"platform": "group", "name": "Lenient", "type": "sum", "ignore_non_numeric": true, "unit_of_measurement": "K", "state_class": "measurement", "entities": []any{"sensor.a", "sensor.text"}},
	)
	mustSet(t, c, "sensor.text", "hello", nil)
	newGroups(t, c, nil, map[string][]map[string]any{"sensor": entries})
	want := map[string]string{"mean": "30.0", "min": "10.0", "max": "60.0", "median": "20.0", "sum": "90.0", "range": "50.0", "product": "12000.0", "count": "3", "last": "60.0"}
	for typ, w := range want {
		if got := stateOf(c, "sensor.g_"+typ); got != w {
			t.Errorf("%s: got %s want %s", typ, got, w)
		}
	}
	if st := c.GetState("sensor.g_min"); st.Attributes["min_entity_id"] != "sensor.a" || st.Attributes["unit_of_measurement"] != "C" {
		t.Errorf("min attrs %+v", st.Attributes)
	}
	if st := c.GetState("sensor.g_max"); st.Attributes["max_entity_id"] != "sensor.c" {
		t.Errorf("max attrs %+v", st.Attributes)
	}
	if got := stateOf(c, "sensor.strict"); got != "unknown" {
		t.Errorf("strict: %s", got)
	}
	if st := c.GetState("sensor.lenient"); st.State != "10.0" || st.Attributes["unit_of_measurement"] != "K" || st.Attributes["state_class"] != "measurement" {
		t.Errorf("lenient: %+v", st)
	}
	mustSet(t, c, "sensor.a", "40", map[string]any{"unit_of_measurement": "C"})
	if got := stateOf(c, "sensor.g_mean"); got != "40.0" {
		t.Errorf("mean after change: %s", got)
	}
	if got := stateOf(c, "sensor.g_last"); got != "40.0" {
		t.Errorf("last after change: %s", got)
	}
	mustSet(t, c, "sensor.b", "unavailable", nil)
	if got := stateOf(c, "sensor.g_mean"); got != "unavailable" {
		t.Errorf("unavailable member: %s", got)
	}
}

func TestOldStyleGroup(t *testing.T) {
	c := core.New()
	a := addOnOff(c, "switch.a", "on", nil)
	b := addOnOff(c, "light.b", "off", nil)
	mustSet(t, c, "device_tracker.phone", "home", nil)
	g := newGroups(t, c, map[string]any{
		"living": map[string]any{"name": "Living Room", "entities": []any{"switch.a", "light.b"}, "icon": "mdi:sofa"},
		"strict": map[string]any{"entities": "switch.a, light.b", "all": true},
		"family": []any{"device_tracker.phone"},
	}, nil)
	st := c.GetState("group.living")
	if st == nil || st.State != "on" || st.Attributes["friendly_name"] != "Living Room" || st.Attributes["icon"] != "mdi:sofa" ||
		!reflect.DeepEqual(st.Attributes["entity_id"], []any{"switch.a", "light.b"}) {
		t.Fatalf("living: %+v", st)
	}
	if st.Attributes["order"] != 1 {
		t.Fatalf("order: %v", st.Attributes["order"])
	}
	if s := stateOf(c, "group.strict"); s != "off" {
		t.Fatalf("strict %s", s)
	}
	if s := stateOf(c, "group.family"); s != "home" {
		t.Fatalf("family %s", s)
	}
	mustSet(t, c, "device_tracker.phone", "not_home", nil)
	if s := stateOf(c, "group.family"); s != "not_home" {
		t.Fatalf("family %s", s)
	}
	mustSet(t, c, "light.b", "on", nil)
	if s := stateOf(c, "group.strict"); s != "on" {
		t.Fatalf("strict %s", s)
	}
	call(t, c, "homeassistant", "turn_off", map[string]any{"entity_id": "group.living"})
	if a.offs != 1 || b.offs != 1 {
		t.Fatalf("turn_off not forwarded: %d %d", a.offs, b.offs)
	}
	if s := stateOf(c, "group.living"); s != "off" {
		t.Fatalf("living after turn_off: %s", s)
	}
	call(t, c, "homeassistant", "turn_on", map[string]any{"entity_id": "group.living"})
	if a.onCount() != 1 || b.onCount() != 1 {
		t.Fatalf("turn_on not forwarded")
	}
	call(t, c, "group", "turn_off", map[string]any{"entity_id": "group.living"})
	if s := stateOf(c, "group.living"); s != "off" {
		t.Fatalf("group.turn_off: %s", s)
	}

	reloaded := false
	g.OnReload = func(context.Context) error { reloaded = true; return nil }
	call(t, c, "group", "reload", nil)
	if !reloaded {
		t.Fatalf("OnReload not called")
	}
}

func TestOldStyleGroupNested(t *testing.T) {
	c := core.New()
	addOnOff(c, "switch.a", "off", nil)
	newGroups(t, c, map[string]any{
		"inner": map[string]any{"entities": []any{"switch.a"}},
		"outer": map[string]any{"entities": []any{"group.inner", "switch.missing"}},
	}, nil)
	if s := stateOf(c, "group.outer"); s != "off" {
		t.Fatalf("outer %s", s)
	}
	mustSet(t, c, "switch.a", "on", nil)
	if s := stateOf(c, "group.outer"); s != "on" {
		t.Fatalf("outer %s", s)
	}
}

func TestBinarySensorAndSwitchGroup(t *testing.T) {
	c := core.New()
	mustSet(t, c, "binary_sensor.door1", "off", nil)
	mustSet(t, c, "binary_sensor.door2", "off", nil)
	sw := addOnOff(c, "switch.s1", "off", nil)
	sw2 := addOnOff(c, "switch.s2", "off", nil)
	newGroups(t, c, nil, map[string][]map[string]any{
		"binary_sensor": {{"platform": "group", "name": "Any door", "device_class": "door", "entities": []any{"binary_sensor.door1", "binary_sensor.door2"}}},
		"switch":        {{"platform": "group", "name": "Both", "entities": []any{"switch.s1", "switch.s2"}}},
	})
	if st := c.GetState("binary_sensor.any_door"); st.State != "off" || st.Attributes["device_class"] != "door" {
		t.Fatalf("%+v", st)
	}
	mustSet(t, c, "binary_sensor.door2", "on", nil)
	if s := stateOf(c, "binary_sensor.any_door"); s != "on" {
		t.Fatalf("any_door %s", s)
	}
	call(t, c, "switch", "turn_on", map[string]any{"entity_id": "switch.both"})
	if sw.onCount() != 1 || sw2.onCount() != 1 {
		t.Fatalf("switch group not forwarded")
	}
	if s := stateOf(c, "switch.both"); s != "on" {
		t.Fatalf("switch group %s", s)
	}
}

type fakeCover struct {
	c     *core.Core
	id    string
	mu    sync.Mutex
	calls []string
}

func addCover(c *core.Core, id, state string, pos int) *fakeCover {
	f := &fakeCover{c: c, id: id}
	c.SetState(id, state, map[string]any{"current_position": pos, "supported_features": 15}, core.Context{})
	c.RegisterController(id, f)
	return f
}

func (f *fakeCover) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeCover) OpenCover(ctx context.Context) error {
	f.record("open")
	return f.c.SetState(f.id, "open", map[string]any{"current_position": 100, "supported_features": 15}, core.ContextFrom(ctx))
}

func (f *fakeCover) CloseCover(ctx context.Context) error {
	f.record("close")
	return f.c.SetState(f.id, "closed", map[string]any{"current_position": 0, "supported_features": 15}, core.ContextFrom(ctx))
}

func (f *fakeCover) StopCover(context.Context) error { f.record("stop"); return nil }

func (f *fakeCover) SetCoverPosition(ctx context.Context, p int) error {
	f.record(fmt.Sprintf("pos%d", p))
	return f.c.SetState(f.id, "open", map[string]any{"current_position": p, "supported_features": 15}, core.ContextFrom(ctx))
}

func TestCoverGroup(t *testing.T) {
	c := core.New()
	a := addCover(c, "cover.a", "closed", 0)
	b := addCover(c, "cover.b", "open", 80)
	newGroups(t, c, nil, map[string][]map[string]any{"cover": {
		{"platform": "group", "name": "Blinds", "entities": []any{"cover.a", "cover.b"}},
	}})
	st := c.GetState("cover.blinds")
	if st.State != "open" || st.Attributes["current_position"] != 40 || st.Attributes["supported_features"] != 15 {
		t.Fatalf("%+v", st)
	}
	call(t, c, "cover", "close_cover", map[string]any{"entity_id": "cover.blinds"})
	if !reflect.DeepEqual(a.calls, []string{"close"}) || !reflect.DeepEqual(b.calls, []string{"close"}) {
		t.Fatalf("close not forwarded: %v %v", a.calls, b.calls)
	}
	if st := c.GetState("cover.blinds"); st.State != "closed" || st.Attributes["current_position"] != 0 {
		t.Fatalf("after close: %+v", st)
	}
	call(t, c, "cover", "set_cover_position", map[string]any{"entity_id": "cover.blinds", "position": 30})
	if a.calls[len(a.calls)-1] != "pos30" || b.calls[len(b.calls)-1] != "pos30" {
		t.Fatalf("position not forwarded: %v", a.calls)
	}
	call(t, c, "cover", "stop_cover", map[string]any{"entity_id": "cover.blinds"})
	call(t, c, "cover", "open_cover", map[string]any{"entity_id": "cover.blinds"})
	if s := stateOf(c, "cover.blinds"); s != "open" {
		t.Fatalf("after open: %s", s)
	}
	mustSet(t, c, "cover.a", "opening", map[string]any{"current_position": 50})
	if s := stateOf(c, "cover.blinds"); s != "opening" {
		t.Fatalf("opening: %s", s)
	}
	mustSet(t, c, "cover.a", "closing", map[string]any{"current_position": 50})
	if s := stateOf(c, "cover.blinds"); s != "closing" {
		t.Fatalf("closing: %s", s)
	}
	// toggle on a closing cover opens it
	call(t, c, "cover", "toggle", map[string]any{"entity_id": "cover.blinds"})
	if a.calls[len(a.calls)-1] != "open" {
		t.Fatalf("toggle: %v", a.calls)
	}
}

type fakeFan struct {
	*fakeOnOff
	pct []int
}

func (f *fakeFan) SetPercentage(ctx context.Context, p int) error {
	f.pct = append(f.pct, p)
	return f.c.SetState(f.id, "on", map[string]any{"percentage": p}, core.ContextFrom(ctx))
}

func TestFanGroup(t *testing.T) {
	c := core.New()
	a := &fakeFan{fakeOnOff: addOnOff(c, "fan.a", "on", map[string]any{"percentage": 30})}
	b := &fakeFan{fakeOnOff: addOnOff(c, "fan.b", "on", map[string]any{"percentage": 70})}
	c.RegisterController("fan.a", a)
	c.RegisterController("fan.b", b)
	newGroups(t, c, nil, map[string][]map[string]any{"fan": {
		{"platform": "group", "name": "Fans", "entities": []any{"fan.a", "fan.b"}},
	}})
	if st := c.GetState("fan.fans"); st.State != "on" || st.Attributes["percentage"] != 50 {
		t.Fatalf("%+v", st)
	}
	call(t, c, "fan", "set_percentage", map[string]any{"entity_id": "fan.fans", "percentage": 90})
	if !reflect.DeepEqual(a.pct, []int{90}) || !reflect.DeepEqual(b.pct, []int{90}) {
		t.Fatalf("percentage not forwarded")
	}
	call(t, c, "fan", "turn_off", map[string]any{"entity_id": "fan.fans"})
	if a.offs != 1 || b.offs != 1 {
		t.Fatalf("turn_off not forwarded")
	}
}

func TestGroupConfigErrors(t *testing.T) {
	c := core.New()
	bad := map[string]map[string][]map[string]any{
		"no name":      {"light": {{"platform": "group", "entities": []any{"light.a"}}}},
		"no entities":  {"light": {{"platform": "group", "name": "x"}}},
		"bad entity":   {"light": {{"platform": "group", "name": "x", "entities": []any{"nope"}}}},
		"bad type":     {"sensor": {{"platform": "group", "name": "x", "type": "stdev", "entities": []any{"sensor.a"}}}},
		"unknown key":  {"light": {{"platform": "group", "name": "x", "entities": []any{"light.a"}, "bogus": 1}}},
		"domain":       {"lock": {{"platform": "group", "name": "x", "entities": []any{"lock.a"}}}},
		"duplicate id": {"light": {{"platform": "group", "name": "x", "unique_id": "u", "entities": []any{"light.a"}}, {"platform": "group", "name": "y", "unique_id": "u", "entities": []any{"light.a"}}}},
	}
	for name, p := range bad {
		if g, err := NewGroups(c, nil, p, nil); err == nil {
			g.Stop()
			t.Errorf("%s: accepted", name)
		}
	}
	if g, err := NewGroups(c, map[string]any{"x": map[string]any{"entities": []any{"group.x"}}}, nil, nil); err == nil {
		g.Stop()
		t.Errorf("self-containing group accepted")
	}
	if g, err := NewGroups(c, map[string]any{"x": map[string]any{"entities": []any{"switch.a"}, "bogus": 1}}, nil, nil); err == nil {
		g.Stop()
		t.Errorf("unknown old-style key accepted")
	}
	// Foreign platforms in any domain are none of our business.
	g, err := NewGroups(c, nil, map[string][]map[string]any{"climate": {{"platform": "generic_thermostat"}}, "light": {{"platform": "hue"}}}, nil)
	if err != nil {
		t.Fatalf("foreign platform rejected: %v", err)
	}
	g.Stop()
}
