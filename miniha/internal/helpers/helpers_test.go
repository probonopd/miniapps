/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

type m = map[string]any

func testConfig() Config {
	return Config{
		Location:     time.UTC,
		InputBoolean: m{"away": m{"name": "Away mode", "icon": "mdi:home"}, "always_on": m{"initial": true}},
		InputNumber: m{
			"volume": m{"name": "Volume", "min": 0, "max": 100, "step": 5, "initial": 20, "unit_of_measurement": "%"},
			"fine":   m{"min": 0, "max": 1, "step": 0.25, "mode": "box"},
		},
		InputSelect: m{"mode": m{"name": "Mode", "options": []any{"eco", "comfort", "boost"}, "initial": "comfort"}},
		InputText:   m{"note": m{"name": "Note", "min": 2, "max": 10, "pattern": "[a-z ]+"}},
		InputDatetime: m{
			"both":  m{"name": "Both"},
			"day":   m{"has_time": false},
			"clock": m{"has_date": false, "initial": "07:30"},
		},
		InputButton: m{"doorbell": m{"name": "Doorbell"}},
		Counter:     m{"visits": m{"name": "Visits", "initial": 5, "step": 2, "minimum": 0, "maximum": 10}},
		Timer:       m{"oven": m{"name": "Oven", "duration": "00:10:00"}},
	}
}

func newEnv(t *testing.T, cfg Config) (*core.Core, *Helpers, string) {
	t.Helper()
	c := core.New()
	dir := t.TempDir()
	h, err := New(c, cfg, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return c, h, dir
}

func call(c *core.Core, domain, service, entity string, data m) error {
	d := m{}
	for k, v := range data {
		d[k] = v
	}
	if entity != "" {
		d["entity_id"] = entity
	}
	_, err := c.CallService(context.Background(), domain, service, d, core.Context{})
	return err
}

func mustCall(t *testing.T, c *core.Core, domain, service, entity string, data m) {
	t.Helper()
	if err := call(c, domain, service, entity, data); err != nil {
		t.Fatalf("%s.%s %s: %v", domain, service, entity, err)
	}
}

func wantState(t *testing.T, c *core.Core, entity, want string) *core.State {
	t.Helper()
	st := c.GetState(entity)
	if st == nil {
		t.Fatalf("%s has no state", entity)
	}
	if st.State != want {
		t.Fatalf("%s = %q, want %q (attributes %v)", entity, st.State, want, st.Attributes)
	}
	return st
}

func wantAttrs(t *testing.T, st *core.State, want m) {
	t.Helper()
	for k, v := range want {
		if got, ok := st.Attributes[k]; !ok || !reflect.DeepEqual(got, v) {
			t.Errorf("%s attribute %s = %#v, want %#v", st.EntityID, k, got, v)
		}
	}
}

func waitEvent(t *testing.T, ch <-chan core.Event) core.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("event did not arrive")
	}
	return core.Event{}
}

func listen(c *core.Core, typ string) <-chan core.Event {
	ch := make(chan core.Event, 16)
	c.Subscribe(typ, func(ev core.Event) { ch <- ev })
	return ch
}

func TestEntitiesAndAttributes(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	for _, id := range []string{
		"input_boolean.away", "input_boolean.always_on", "input_number.volume", "input_number.fine",
		"input_select.mode", "input_text.note", "input_datetime.both", "input_datetime.day",
		"input_datetime.clock", "input_button.doorbell", "counter.visits", "timer.oven",
	} {
		if c.GetState(id) == nil {
			t.Errorf("%s is missing", id)
		}
		e := c.Entity(id)
		if e == nil {
			t.Fatalf("%s is not in the registry", id)
		}
		domain, object := core.SplitEntityID(id)
		if e.Platform != domain || e.UniqueID != object {
			t.Errorf("%s registered as %s/%s", id, e.Platform, e.UniqueID)
		}
	}
	st := wantState(t, c, "input_boolean.away", "off")
	wantAttrs(t, st, m{"editable": true, "friendly_name": "Away mode", "icon": "mdi:home"})
	wantState(t, c, "input_boolean.always_on", "on")
	// Without a name the object id is the friendly name.
	wantAttrs(t, c.GetState("input_boolean.always_on"), m{"friendly_name": "always_on"})

	st = wantState(t, c, "input_number.volume", "20")
	wantAttrs(t, st, m{"min": 0.0, "max": 100.0, "step": 5.0, "mode": "slider", "unit_of_measurement": "%", "editable": true, "initial": 20.0, "friendly_name": "Volume"})
	st = wantState(t, c, "input_number.fine", "0.0")
	wantAttrs(t, st, m{"mode": "box", "step": 0.25})
	if _, ok := st.Attributes["unit_of_measurement"]; ok {
		t.Error("unit_of_measurement set without a unit")
	}

	st = wantState(t, c, "input_select.mode", "comfort")
	wantAttrs(t, st, m{"options": []string{"eco", "comfort", "boost"}, "editable": true})

	st = wantState(t, c, "input_text.note", "")
	wantAttrs(t, st, m{"min": 2, "max": 10, "pattern": "[a-z ]+", "mode": "text", "editable": true})

	st = wantState(t, c, "input_datetime.both", "1970-01-01 00:00:00")
	wantAttrs(t, st, m{"has_date": true, "has_time": true, "editable": true, "year": 1970, "month": 1, "day": 1, "hour": 0, "minute": 0, "second": 0, "timestamp": 0.0})
	st = wantState(t, c, "input_datetime.day", "1970-01-01")
	wantAttrs(t, st, m{"has_date": true, "has_time": false, "year": 1970, "timestamp": 0.0})
	if _, ok := st.Attributes["hour"]; ok {
		t.Error("date-only entity has an hour")
	}
	st = wantState(t, c, "input_datetime.clock", "07:30:00")
	wantAttrs(t, st, m{"has_date": false, "has_time": true, "hour": 7, "minute": 30, "second": 0, "timestamp": 27000.0})
	if _, ok := st.Attributes["year"]; ok {
		t.Error("time-only entity has a year")
	}

	wantState(t, c, "input_button.doorbell", "unknown")

	st = wantState(t, c, "counter.visits", "5")
	wantAttrs(t, st, m{"initial": 5, "step": 2, "minimum": 0, "maximum": 10, "editable": true})

	st = wantState(t, c, "timer.oven", "idle")
	wantAttrs(t, st, m{"duration": "0:10:00", "editable": true, "restore": false})
	if _, ok := st.Attributes["remaining"]; ok {
		t.Error("idle timer has remaining")
	}
}

func TestInputBooleanServices(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	mustCall(t, c, "input_boolean", "turn_on", "input_boolean.away", nil)
	wantState(t, c, "input_boolean.away", "on")
	mustCall(t, c, "input_boolean", "toggle", "input_boolean.away", nil)
	wantState(t, c, "input_boolean.away", "off")
	mustCall(t, c, "input_boolean", "toggle", "input_boolean.away", nil)
	mustCall(t, c, "input_boolean", "turn_off", "input_boolean.away", nil)
	wantState(t, c, "input_boolean.away", "off")
	// homeassistant.turn_on reaches helpers through the controller too.
	mustCall(t, c, "homeassistant", "turn_on", "input_boolean.away", nil)
	wantState(t, c, "input_boolean.away", "on")
	if err := call(c, "input_boolean", "turn_on", "input_boolean.nope", nil); err == nil {
		t.Error("unknown entity accepted")
	}
	// The change must be reported to the caller with its context.
	got, err := c.CallService(context.Background(), "input_boolean", "turn_off", m{"entity_id": "input_boolean.away"}, core.Context{})
	if err != nil || len(got) != 1 || got[0].State != "off" {
		t.Errorf("CallService returned %v, %v", got, err)
	}
}

func TestInputNumber(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": 35})
	wantState(t, c, "input_number.volume", "35")
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": "40"})
	wantState(t, c, "input_number.volume", "40")
	for _, bad := range []any{101, -1, 100.5, "loud", true} {
		if err := call(c, "input_number", "set_value", "input_number.volume", m{"value": bad}); err == nil {
			t.Errorf("value %v accepted", bad)
		}
	}
	wantState(t, c, "input_number.volume", "40")
	if err := call(c, "input_number", "set_value", "input_number.volume", nil); err == nil {
		t.Error("missing value accepted")
	}

	mustCall(t, c, "input_number", "increment", "input_number.volume", nil)
	wantState(t, c, "input_number.volume", "45")
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": 98})
	mustCall(t, c, "input_number", "increment", "input_number.volume", nil)
	wantState(t, c, "input_number.volume", "100")
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": 3})
	mustCall(t, c, "input_number", "decrement", "input_number.volume", nil)
	wantState(t, c, "input_number.volume", "0")

	// A fractional step keeps the float form.
	mustCall(t, c, "input_number", "increment", "input_number.fine", nil)
	wantState(t, c, "input_number.fine", "0.25")
	mustCall(t, c, "input_number", "set_value", "input_number.fine", m{"value": 1})
	wantState(t, c, "input_number.fine", "1.0")

	// The controller serves number-style UIs directly.
	setter, ok := c.Controller("input_number.volume").(core.ValueSetter)
	if !ok {
		t.Fatal("input_number is not a core.ValueSetter")
	}
	if err := setter.SetValue(context.Background(), 12.0); err != nil {
		t.Fatal(err)
	}
	wantState(t, c, "input_number.volume", "12")
	if err := setter.SetValue(context.Background(), 500); err == nil {
		t.Error("controller accepted an out-of-range value")
	}
}

func TestInputSelect(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	const id = "input_select.mode"
	mustCall(t, c, "input_select", "select_option", id, m{"option": "boost"})
	wantState(t, c, id, "boost")
	if err := call(c, "input_select", "select_option", id, m{"option": "turbo"}); err == nil {
		t.Error("unknown option accepted")
	}
	wantState(t, c, id, "boost")

	mustCall(t, c, "input_select", "select_next", id, nil)
	wantState(t, c, id, "eco")
	mustCall(t, c, "input_select", "select_previous", id, nil)
	wantState(t, c, id, "boost")
	mustCall(t, c, "input_select", "select_next", id, m{"cycle": false})
	wantState(t, c, id, "boost")
	mustCall(t, c, "input_select", "select_first", id, nil)
	wantState(t, c, id, "eco")
	mustCall(t, c, "input_select", "select_previous", id, m{"cycle": false})
	wantState(t, c, id, "eco")
	mustCall(t, c, "input_select", "select_previous", id, m{"cycle": "true"})
	wantState(t, c, id, "boost")
	mustCall(t, c, "input_select", "select_last", id, nil)
	wantState(t, c, id, "boost")

	mustCall(t, c, "input_select", "set_options", id, m{"options": []any{"boost", "sleep"}})
	st := wantState(t, c, id, "boost")
	wantAttrs(t, st, m{"options": []string{"boost", "sleep"}})
	mustCall(t, c, "input_select", "set_options", id, m{"options": []any{"a", "b"}})
	wantState(t, c, id, "a")
	for _, bad := range []any{[]any{}, []any{"x", "x"}, "x", []any{m{}}} {
		if err := call(c, "input_select", "set_options", id, m{"options": bad}); err == nil {
			t.Errorf("options %v accepted", bad)
		}
	}

	sel, ok := c.Controller(id).(core.OptionSelector)
	if !ok {
		t.Fatal("input_select is not a core.OptionSelector")
	}
	if err := sel.SelectOption(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	wantState(t, c, id, "b")
}

func TestInputText(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	const id = "input_text.note"
	mustCall(t, c, "input_text", "set_value", id, m{"value": "hello"})
	wantState(t, c, id, "hello")
	for _, bad := range []string{"x", "this is too long", "UPPER"} {
		if err := call(c, "input_text", "set_value", id, m{"value": bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := call(c, "input_text", "set_value", id, m{"value": 5}); err == nil {
		t.Error("non-string accepted")
	}
	wantState(t, c, id, "hello")
	if _, ok := c.Controller(id).(core.ValueSetter); !ok {
		t.Error("input_text is not a core.ValueSetter")
	}
}

func TestInputDatetime(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"date": "2026-03-04", "time": "05:06:07"})
	st := wantState(t, c, "input_datetime.both", "2026-03-04 05:06:07")
	wantAttrs(t, st, m{"year": 2026, "month": 3, "day": 4, "hour": 5, "minute": 6, "second": 7, "timestamp": float64(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).Unix())})
	// A lone date keeps the time of day and vice versa.
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"date": "2027-01-02"})
	wantState(t, c, "input_datetime.both", "2027-01-02 05:06:07")
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"time": "23:59"})
	wantState(t, c, "input_datetime.both", "2027-01-02 23:59:00")
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"datetime": "2028-05-06 07:08:09"})
	wantState(t, c, "input_datetime.both", "2028-05-06 07:08:09")
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"datetime": "2028-05-06T07:08:10"})
	wantState(t, c, "input_datetime.both", "2028-05-06 07:08:10")
	ts := time.Date(2030, 12, 31, 1, 2, 3, 0, time.UTC).Unix()
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"timestamp": ts})
	wantState(t, c, "input_datetime.both", "2030-12-31 01:02:03")

	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.day", m{"date": "2026-07-08"})
	st = wantState(t, c, "input_datetime.day", "2026-07-08")
	wantAttrs(t, st, m{"timestamp": float64(time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC).Unix())})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.day", m{"datetime": "2026-09-10 11:12:13"})
	wantState(t, c, "input_datetime.day", "2026-09-10")

	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.clock", m{"time": "01:02:03"})
	st = wantState(t, c, "input_datetime.clock", "01:02:03")
	wantAttrs(t, st, m{"timestamp": 3723.0})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.clock", m{"timestamp": ts})
	wantState(t, c, "input_datetime.clock", "01:02:03")

	// HA's validation: parts the entity does not have, nothing, mixtures.
	bad := []struct {
		id   string
		data m
	}{
		{"input_datetime.day", m{"time": "10:00"}},
		{"input_datetime.clock", m{"date": "2026-01-01"}},
		{"input_datetime.both", m{}},
		{"input_datetime.both", m{"date": "2026-01-01", "datetime": "2026-01-01 00:00:00"}},
		{"input_datetime.both", m{"date": "01.02.2026"}},
		{"input_datetime.both", m{"time": "25:00"}},
		{"input_datetime.both", m{"timestamp": "soon"}},
	}
	for _, b := range bad {
		if err := call(c, "input_datetime", "set_datetime", b.id, b.data); err == nil {
			t.Errorf("%s accepted %v", b.id, b.data)
		}
	}
}

func TestInputButton(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	c.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC) }
	mustCall(t, c, "input_button", "press", "input_button.doorbell", nil)
	wantState(t, c, "input_button.doorbell", "2026-01-02T03:04:05.123456+00:00")
	p, ok := c.Controller("input_button.doorbell").(core.Presser)
	if !ok {
		t.Fatal("input_button is not a core.Presser")
	}
	c.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC) }
	if err := p.Press(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantState(t, c, "input_button.doorbell", "2026-01-02T03:04:06.000000+00:00")
}

func TestCounter(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	const id = "counter.visits"
	mustCall(t, c, "counter", "increment", id, nil)
	wantState(t, c, id, "7")
	mustCall(t, c, "counter", "increment", id, nil)
	mustCall(t, c, "counter", "increment", id, nil)
	wantState(t, c, id, "10")
	mustCall(t, c, "counter", "increment", id, nil)
	wantState(t, c, id, "10")
	mustCall(t, c, "counter", "reset", id, nil)
	wantState(t, c, id, "5")
	mustCall(t, c, "counter", "set_value", id, m{"value": 99})
	wantState(t, c, id, "10")
	mustCall(t, c, "counter", "set_value", id, m{"value": 1})
	mustCall(t, c, "counter", "decrement", id, nil)
	wantState(t, c, id, "0")
	mustCall(t, c, "counter", "decrement", id, nil)
	wantState(t, c, id, "0")
	if err := call(c, "counter", "set_value", id, m{"value": 1.5}); err == nil {
		t.Error("fractional value accepted")
	}
	if err := call(c, "counter", "set_value", id, nil); err == nil {
		t.Error("missing value accepted")
	}
}

func TestTimerFinish(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	const id = "timer.oven"
	started := listen(c, "timer.started")
	finished := listen(c, "timer.finished")
	mustCall(t, c, "timer", "start", id, m{"duration": "00:00:00.1"})
	ev := waitEvent(t, started)
	if ev.Data["entity_id"] != id || ev.Data["finishes_at"] == "" {
		t.Errorf("started event %v", ev.Data)
	}
	st := wantState(t, c, id, "active")
	wantAttrs(t, st, m{"duration": "0:00:00", "remaining": "0:00:00"})
	end, err := time.Parse(time.RFC3339, st.Attributes["finishes_at"].(string))
	if err != nil {
		t.Fatalf("finishes_at: %v", err)
	}
	ev = waitEvent(t, finished)
	if ev.Data["entity_id"] != id || ev.Data["finishes_at"] != st.Attributes["finishes_at"] {
		t.Errorf("finished event %v", ev.Data)
	}
	if time.Since(end) < -time.Second {
		t.Error("finished long before the deadline")
	}
	st = wantState(t, c, id, "idle")
	for _, k := range []string{"remaining", "finishes_at"} {
		if _, ok := st.Attributes[k]; ok {
			t.Errorf("idle timer still has %s", k)
		}
	}
	// The duration attribute returns to the configured one.
	wantAttrs(t, st, m{"duration": "0:10:00"})
}

func TestTimerLifecycle(t *testing.T) {
	c, _, _ := newEnv(t, testConfig())
	const id = "timer.oven"
	var mu sync.Mutex
	var types []string
	c.Subscribe("", func(ev core.Event) {
		if strings.HasPrefix(ev.Type, "timer.") {
			mu.Lock()
			types = append(types, ev.Type)
			mu.Unlock()
		}
	})

	mustCall(t, c, "timer", "start", id, nil)
	st := wantState(t, c, id, "active")
	wantAttrs(t, st, m{"duration": "0:10:00", "remaining": "0:09:59"})
	if _, ok := st.Attributes["finishes_at"]; !ok {
		t.Error("active timer has no finishes_at")
	}
	mustCall(t, c, "timer", "pause", id, nil)
	st = wantState(t, c, id, "paused")
	if _, ok := st.Attributes["remaining"]; !ok {
		t.Error("paused timer has no remaining")
	}
	if _, ok := st.Attributes["finishes_at"]; ok {
		t.Error("paused timer has finishes_at")
	}
	mustCall(t, c, "timer", "start", id, nil)
	wantState(t, c, id, "active")
	mustCall(t, c, "timer", "change", id, m{"duration": "00:05:00"})
	st = wantState(t, c, id, "active")
	if r := st.Attributes["remaining"]; r != "0:14:59" && r != "0:15:00" {
		t.Errorf("remaining after change = %v", r)
	}
	mustCall(t, c, "timer", "cancel", id, nil)
	st = wantState(t, c, id, "idle")
	wantAttrs(t, st, m{"duration": "0:10:00"})

	mustCall(t, c, "timer", "start", id, m{"duration": 30})
	st = wantState(t, c, id, "active")
	wantAttrs(t, st, m{"duration": "0:00:30"})
	mustCall(t, c, "timer", "finish", id, nil)
	wantState(t, c, id, "idle")

	// Calls that do not apply to the state are ignored, as in HA.
	mustCall(t, c, "timer", "pause", id, nil)
	mustCall(t, c, "timer", "cancel", id, nil)
	mustCall(t, c, "timer", "change", id, m{"duration": 10})
	mustCall(t, c, "timer", "finish", id, nil)
	wantState(t, c, id, "idle")

	// Shrinking an active timer past its deadline finishes it.
	mustCall(t, c, "timer", "start", id, m{"duration": 60})
	mustCall(t, c, "timer", "change", id, m{"duration": "-00:02:00"})
	wantState(t, c, id, "idle")

	mu.Lock()
	got := append([]string(nil), types...)
	mu.Unlock()
	want := []string{"timer.started", "timer.paused", "timer.restarted", "timer.changed", "timer.cancelled", "timer.started", "timer.finished", "timer.started", "timer.finished"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events\n got %v\nwant %v", got, want)
	}
	for _, bad := range []m{{"duration": "soon"}, {"duration": -5}, {"duration": m{"weeks": 1}}} {
		if err := call(c, "timer", "start", id, bad); err == nil {
			t.Errorf("start accepted %v", bad)
		}
	}
	if err := call(c, "timer", "change", id, nil); err == nil {
		t.Error("change without duration accepted")
	}
	mustCall(t, c, "timer", "start", id, m{"duration": m{"minutes": 1, "seconds": 30}})
	wantAttrs(t, wantState(t, c, id, "active"), m{"duration": "0:01:30"})
}

func TestTimerRestore(t *testing.T) {
	cfg := testConfig()
	cfg.Timer = m{
		"keep": m{"duration": "1:00:00", "restore": true},
		"drop": m{"duration": "1:00:00"},
		"soon": m{"duration": "1:00:00", "restore": true},
		"naps": m{"duration": "0:30:00", "restore": true},
	}
	c, h, dir := newEnv(t, cfg)
	mustCall(t, c, "timer", "start", "timer.keep", nil)
	mustCall(t, c, "timer", "start", "timer.drop", nil)
	mustCall(t, c, "timer", "start", "timer.soon", nil)
	mustCall(t, c, "timer", "start", "timer.naps", nil)
	mustCall(t, c, "timer", "pause", "timer.naps", nil)
	// Move the deadline of "soon" into the past, as if the hub had been down.
	h.mu.Lock()
	h.entities["timer.soon"].(*timer).end = time.Now().Add(-time.Minute)
	h.mu.Unlock()
	end := c.GetState("timer.keep").Attributes["finishes_at"]
	remaining := c.GetState("timer.naps").Attributes["remaining"]
	h.Stop()

	c2 := core.New()
	finished := listen(c2, "timer.finished")
	h2, err := New(c2, cfg, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Stop()
	st := wantState(t, c2, "timer.keep", "active")
	wantAttrs(t, st, m{"finishes_at": end})
	wantState(t, c2, "timer.drop", "idle")
	st = wantState(t, c2, "timer.naps", "paused")
	wantAttrs(t, st, m{"remaining": remaining})
	// The deadline of "soon" passed while the hub was down.
	wantState(t, c2, "timer.soon", "idle")
	if ev := waitEvent(t, finished); ev.Data["entity_id"] != "timer.soon" {
		t.Errorf("finished event %v", ev.Data)
	}
	mustCall(t, c2, "timer", "cancel", "timer.keep", nil)
}

func TestPersistence(t *testing.T) {
	cfg := testConfig()
	c, h, dir := newEnv(t, cfg)
	mustCall(t, c, "input_boolean", "turn_on", "input_boolean.away", nil)
	mustCall(t, c, "input_boolean", "turn_off", "input_boolean.always_on", nil)
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": 55})
	mustCall(t, c, "input_number", "set_value", "input_number.fine", m{"value": 0.5})
	mustCall(t, c, "input_select", "select_option", "input_select.mode", m{"option": "eco"})
	mustCall(t, c, "input_text", "set_value", "input_text.note", m{"value": "kept"})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.both", m{"datetime": "2026-03-04 05:06:07"})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.day", m{"date": "2026-03-04"})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.clock", m{"time": "08:09:10"})
	c.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	mustCall(t, c, "input_button", "press", "input_button.doorbell", nil)
	mustCall(t, c, "counter", "set_value", "counter.visits", m{"value": 8})
	h.Stop()
	if _, err := os.Stat(filepath.Join(dir, "helpers_state.json")); err != nil {
		t.Fatal(err)
	}

	c2 := core.New()
	h2, err := New(c2, cfg, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Stop()
	// "initial" wins over the persisted value, everything else comes back.
	wantState(t, c2, "input_boolean.away", "on")
	wantState(t, c2, "input_boolean.always_on", "on")
	wantState(t, c2, "input_number.fine", "0.5")
	wantState(t, c2, "input_number.volume", "20")
	wantState(t, c2, "input_select.mode", "comfort")
	wantState(t, c2, "input_text.note", "kept")
	wantState(t, c2, "input_datetime.both", "2026-03-04 05:06:07")
	wantState(t, c2, "input_datetime.day", "2026-03-04")
	wantState(t, c2, "input_datetime.clock", "07:30:00")
	wantState(t, c2, "input_button.doorbell", "2026-01-02T03:04:05.000000+00:00")
	wantState(t, c2, "counter.visits", "8")
}

func TestPersistenceWithoutInitial(t *testing.T) {
	cfg := Config{
		Location:      time.UTC,
		InputNumber:   m{"n": m{"min": 0, "max": 10}},
		InputSelect:   m{"s": m{"options": []any{"a", "b", "c"}}},
		InputDatetime: m{"clock": m{"has_date": false}},
		Counter:       m{"keep": m{"initial": 1}, "fresh": m{"initial": 1, "restore": false}},
	}
	c, h, dir := newEnv(t, cfg)
	mustCall(t, c, "input_number", "set_value", "input_number.n", m{"value": 7})
	mustCall(t, c, "input_select", "select_option", "input_select.s", m{"option": "c"})
	mustCall(t, c, "input_datetime", "set_datetime", "input_datetime.clock", m{"time": "11:12:13"})
	mustCall(t, c, "counter", "set_value", "counter.keep", m{"value": 4})
	mustCall(t, c, "counter", "set_value", "counter.fresh", m{"value": 4})
	h.Stop()

	// A removed option makes the persisted selection invalid.
	cfg.InputSelect = m{"s": m{"options": []any{"a", "b"}}}
	c2 := core.New()
	h2, err := New(c2, cfg, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Stop()
	wantState(t, c2, "input_number.n", "7")
	wantState(t, c2, "input_select.s", "a")
	wantState(t, c2, "input_datetime.clock", "11:12:13")
	wantState(t, c2, "counter.keep", "4")
	wantState(t, c2, "counter.fresh", "1")
}

func TestDebouncedSave(t *testing.T) {
	c, _, dir := newEnv(t, testConfig())
	mustCall(t, c, "counter", "increment", "counter.visits", nil)
	// The file appears by itself after the debounce, without Stop.
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "helpers_state.json"))
		if err == nil && strings.Contains(string(raw), `"7"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state file not written: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestCorruptStateFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helpers_state.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(core.New(), testConfig(), dir, nil); err == nil {
		t.Fatal("corrupt state file accepted")
	}
}

func TestReload(t *testing.T) {
	cfg := testConfig()
	c, h, _ := newEnv(t, cfg)
	mustCall(t, c, "input_number", "set_value", "input_number.volume", m{"value": 55})
	mustCall(t, c, "input_select", "select_option", "input_select.mode", m{"option": "boost"})
	mustCall(t, c, "timer", "start", "timer.oven", nil)
	mustCall(t, c, "counter", "set_value", "counter.visits", m{"value": 9})

	next := testConfig()
	delete(next.InputBoolean, "away")
	next.InputBoolean["fresh"] = m{"name": "Fresh"}
	// A changed definition keeps the value if it is still valid, even
	// though initial would apply at startup.
	next.InputNumber["volume"] = m{"name": "Volume", "min": 0, "max": 50, "step": 5, "initial": 20}
	next.InputSelect["mode"] = m{"name": "Mode", "options": []any{"boost", "eco"}, "initial": "eco"}
	next.Counter["visits"] = m{"name": "Visits", "initial": 5, "step": 3, "minimum": 0, "maximum": 10}
	if err := h.Reload(next); err != nil {
		t.Fatal(err)
	}
	if c.GetState("input_boolean.away") != nil || c.Entity("input_boolean.away") != nil || c.Controller("input_boolean.away") != nil {
		t.Error("removed entity is still there")
	}
	wantState(t, c, "input_boolean.fresh", "off")
	st := wantState(t, c, "input_number.volume", "20")
	wantAttrs(t, st, m{"max": 50.0})
	wantAttrs(t, wantState(t, c, "input_select.mode", "boost"), m{"options": []string{"boost", "eco"}})
	wantAttrs(t, wantState(t, c, "counter.visits", "9"), m{"step": 3})
	// The untouched timer kept running.
	wantState(t, c, "timer.oven", "active")
	mustCall(t, c, "counter", "increment", "counter.visits", nil)
	wantState(t, c, "counter.visits", "10")
	mustCall(t, c, "input_boolean", "turn_on", "input_boolean.fresh", nil)
	if err := call(c, "input_boolean", "turn_on", "input_boolean.away", nil); err == nil {
		t.Error("removed entity still answers services")
	}

	// An invalid configuration changes nothing.
	bad := testConfig()
	bad.InputNumber["volume"] = m{"min": 5, "max": 1}
	if err := h.Reload(bad); err == nil {
		t.Fatal("invalid reload accepted")
	}
	wantState(t, c, "input_number.volume", "20")
	wantState(t, c, "input_boolean.fresh", "on")

	// Emptying a section removes its entities and the state file forgets them.
	next.Timer = nil
	if err := h.Reload(next); err != nil {
		t.Fatal(err)
	}
	if c.GetState("timer.oven") != nil {
		t.Error("timer survived the removal of its section")
	}
}

func TestReloadService(t *testing.T) {
	cfg := testConfig()
	c, h, _ := newEnv(t, cfg)
	for _, d := range Domains {
		if !c.HasService(d, "reload") {
			t.Errorf("%s.reload missing", d)
		}
	}
	if err := call(c, "counter", "reload", "", nil); err == nil {
		t.Error("reload without a loader succeeded")
	}
	next := testConfig()
	next.Counter["extra"] = m{}
	next.InputBoolean["ignored"] = m{}
	h.SetLoader(func() (Config, error) { return next, nil })
	mustCall(t, c, "counter", "reload", "", nil)
	wantState(t, c, "counter.extra", "0")
	// Only the called domain is reloaded.
	if c.GetState("input_boolean.ignored") != nil {
		t.Error("counter.reload touched input_boolean")
	}
	mustCall(t, c, "input_boolean", "reload", "", nil)
	wantState(t, c, "input_boolean.ignored", "off")
}

func TestInvalidConfig(t *testing.T) {
	cases := map[string]Config{
		"unknown option":      {InputBoolean: m{"a": m{"initail": true}}},
		"bad object id":       {InputBoolean: m{"Not Valid": nil}},
		"not a mapping":       {InputBoolean: m{"a": 5}},
		"bad initial type":    {InputBoolean: m{"a": m{"initial": "yes"}}},
		"number without min":  {InputNumber: m{"a": m{"max": 5}}},
		"number without max":  {InputNumber: m{"a": m{"min": 5}}},
		"number min>=max":     {InputNumber: m{"a": m{"min": 5, "max": 5}}},
		"number initial out":  {InputNumber: m{"a": m{"min": 0, "max": 5, "initial": 6}}},
		"number step too big": {InputNumber: m{"a": m{"min": 0, "max": 5, "step": 10}}},
		"number bad mode":     {InputNumber: m{"a": m{"min": 0, "max": 5, "mode": "dial"}}},
		"select no options":   {InputSelect: m{"a": nil}},
		"select empty":        {InputSelect: m{"a": m{"options": []any{}}}},
		"select duplicates":   {InputSelect: m{"a": m{"options": []any{"x", "x"}}}},
		"select bad initial":  {InputSelect: m{"a": m{"options": []any{"x"}, "initial": "y"}}},
		"text min>max":        {InputText: m{"a": m{"min": 5, "max": 2}}},
		"text bad pattern":    {InputText: m{"a": m{"pattern": "("}}},
		"text bad mode":       {InputText: m{"a": m{"mode": "rich"}}},
		"text initial length": {InputText: m{"a": m{"max": 2, "initial": "abc"}}},
		"datetime neither":    {InputDatetime: m{"a": m{"has_date": false, "has_time": false}}},
		"datetime bad init":   {InputDatetime: m{"a": m{"initial": "tomorrow"}}},
		"counter step 0":      {Counter: m{"a": m{"step": 0}}},
		"counter min>max":     {Counter: m{"a": m{"minimum": 5, "maximum": 1}}},
		"counter initial out": {Counter: m{"a": m{"maximum": 5, "initial": 6}}},
		"timer bad duration":  {Timer: m{"a": m{"duration": "soon"}}},
		"timer negative":      {Timer: m{"a": m{"duration": -5}}},
		"button option":       {InputButton: m{"a": m{"initial": 1}}},
	}
	for name, cfg := range cases {
		c := core.New()
		if _, err := New(c, cfg, t.TempDir(), nil); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.HasPrefix(err.Error(), "input_") && !strings.HasPrefix(err.Error(), "counter") && !strings.HasPrefix(err.Error(), "timer") {
			t.Errorf("%s: error does not name the helper: %v", name, err)
		}
		if len(c.Entities()) != 0 {
			t.Errorf("%s: entities registered despite the error", name)
		}
	}
	// Every problem is reported, not just the first.
	_, err := New(core.New(), Config{InputBoolean: m{"a": m{"x": 1}}, Counter: m{"b": m{"y": 1}}}, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "input_boolean.a") || !strings.Contains(err.Error(), "counter.b") {
		t.Errorf("combined error %v", err)
	}
}

func TestOptionHelpers(t *testing.T) {
	durations := map[string]time.Duration{
		"0:10:00": 10 * time.Minute, "00:00:05": 5 * time.Second, "1:30": 90 * time.Minute,
		"-0:00:10": -10 * time.Second, "90": 90 * time.Second,
	}
	for in, want := range durations {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := parseDuration(1.5); err != nil || got != 1500*time.Millisecond {
		t.Errorf("parseDuration(1.5) = %v, %v", got, err)
	}
	if got, err := parseDuration(m{"hours": 1, "minutes": 2, "seconds": 3}); err != nil || got != time.Hour+2*time.Minute+3*time.Second {
		t.Errorf("parseDuration(mapping) = %v, %v", got, err)
	}
	if got, err := parseDuration(map[any]any{"minutes": 1}); err != nil || got != time.Minute {
		t.Errorf("parseDuration(map[any]any) = %v, %v", got, err)
	}
	for _, bad := range []any{"1:99", "a:b", "x", true, m{"fortnights": 1}} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%v) accepted", bad)
		}
	}
	if s := formatDuration(25*time.Hour + 61*time.Second + 900*time.Millisecond); s != "25:01:01" {
		t.Errorf("formatDuration = %s", s)
	}
}
