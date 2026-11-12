/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

type exportOnOff struct {
	mu    sync.Mutex
	calls []string
	data  []map[string]any
}

func (o *exportOnOff) TurnOn(_ context.Context, data map[string]any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, "on")
	o.data = append(o.data, data)
	return nil
}

func (o *exportOnOff) TurnOff(_ context.Context, data map[string]any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, "off")
	o.data = append(o.data, data)
	return nil
}

func (o *exportOnOff) snapshot() ([]string, []map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...), append([]map[string]any(nil), o.data...)
}

type exportValue struct {
	mu   sync.Mutex
	vals []any
}

func (v *exportValue) SetValue(_ context.Context, value any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.vals = append(v.vals, value)
	return nil
}

type exportClimate struct {
	mu    sync.Mutex
	modes []string
	temps []map[string]any
}

func (cl *exportClimate) SetTemperature(_ context.Context, data map[string]any) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.temps = append(cl.temps, data)
	return nil
}

func (cl *exportClimate) SetHVACMode(_ context.Context, mode string) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.modes = append(cl.modes, mode)
	return nil
}

type exportFixture struct {
	c       *core.Core
	tr      *fakeTransport
	x       *Exporter
	sw      *exportOnOff
	light   *exportOnOff
	number  *exportValue
	climate *exportClimate
}

func newExportFixture(t *testing.T, opts ExportOptions) *exportFixture {
	t.Helper()
	f := &exportFixture{c: core.New(), tr: &fakeTransport{}, sw: &exportOnOff{}, light: &exportOnOff{}, number: &exportValue{}, climate: &exportClimate{}}
	set := func(id, state string, attrs map[string]any) {
		t.Helper()
		if err := f.c.SetState(id, state, attrs, core.Context{}); err != nil {
			t.Fatal(err)
		}
	}
	set("switch.lamp", "on", map[string]any{"friendly_name": "Lamp"})
	set("light.kitchen", "on", map[string]any{
		"friendly_name": "Kitchen", "brightness": 128, "color_mode": "rgb",
		"supported_color_modes": []any{"rgb", "color_temp"}, "rgb_color": []any{255, 0, 10},
		"min_color_temp_kelvin": 2000, "max_color_temp_kelvin": 6500,
	})
	set("sensor.outdoor", "21.5", map[string]any{"friendly_name": "Outdoor", "unit_of_measurement": "°C", "device_class": "temperature", "state_class": "measurement"})
	set("sensor.note", "hello", map[string]any{"friendly_name": "Note", "state_class": "measurement"})
	set("number.target", "20", map[string]any{"friendly_name": "Target", "min": 5, "max": 30, "step": 0.5, "unit_of_measurement": "°C"})
	set("climate.hvac", "heat", map[string]any{"friendly_name": "HVAC", "hvac_modes": []any{"off", "heat"}, "temperature": 21.0, "current_temperature": 19.5})
	set("automation.morning", "on", map[string]any{"friendly_name": "Morning"})
	f.c.RegisterController("switch.lamp", f.sw)
	f.c.RegisterController("light.kitchen", f.light)
	f.c.RegisterController("number.target", f.number)
	f.c.RegisterController("climate.hvac", f.climate)
	if opts.Name == "" {
		opts.Name = "Test Hub"
	}
	f.x = NewExporter(f.c, f.tr, opts, "hub1", slog.Default())
	if err := f.x.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.x.Stop)
	return f
}

func (f *exportFixture) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f.x.idle() {
		if time.Now().After(deadline) {
			t.Fatal("exporter never became idle")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// find returns the last publish on a topic.
func (tr *fakeTransport) find(topic string) (pub, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for i := len(tr.pubs) - 1; i >= 0; i-- {
		if tr.pubs[i].topic == topic {
			return tr.pubs[i], true
		}
	}
	return pub{}, false
}

func (tr *fakeTransport) mustFind(t *testing.T, topic string) pub {
	t.Helper()
	p, ok := tr.find(topic)
	if !ok {
		t.Fatalf("nothing published on %s", topic)
	}
	return p
}

func (tr *fakeTransport) injectRetained(topic, payload string) {
	tr.mu.Lock()
	subs := append([]fakeSub(nil), tr.subs...)
	tr.mu.Unlock()
	for _, s := range subs {
		if MatchTopic(s.filter, topic) {
			s.cb(Message{Topic: topic, Payload: []byte(payload), Retained: true})
		}
	}
}

func decodeConfig(t *testing.T, p pub) map[string]any {
	t.Helper()
	if !p.retain {
		t.Errorf("%s not retained", p.topic)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(p.payload), &m); err != nil {
		t.Fatalf("%s: %v in %q", p.topic, err, p.payload)
	}
	return m
}

func TestExportDiscoveryConfigs(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	if p := f.tr.mustFind(t, "miniha/hub1/status"); p.payload != "online" || !p.retain {
		t.Fatalf("birth: %+v", p)
	}

	sw := decodeConfig(t, f.tr.mustFind(t, "homeassistant/switch/hub1_switch_lamp/config"))
	for k, want := range map[string]string{
		"name": "Lamp", "unique_id": "hub1_switch.lamp", "object_id": "hub1_lamp",
		"state_topic": "miniha/hub1/state/switch.lamp", "command_topic": "miniha/hub1/cmd/switch.lamp",
		"json_attributes_topic": "miniha/hub1/attr/switch.lamp", "availability_topic": "miniha/hub1/status",
		"payload_available": "online", "payload_not_available": "offline", "payload_on": "ON", "state_on": "on",
	} {
		if sw[k] != want {
			t.Errorf("switch %s = %v, want %q", k, sw[k], want)
		}
	}
	dev, _ := sw["device"].(map[string]any)
	ids, _ := dev["identifiers"].([]any)
	if dev["name"] != "Test Hub" || dev["manufacturer"] != "MiniHA" || dev["model"] != "hub" || dev["sw_version"] != Version || len(ids) != 1 || ids[0] != "miniha_hub1" {
		t.Errorf("device block: %v", dev)
	}
	if o, _ := sw["origin"].(map[string]any); o["name"] != "MiniHA" || o["sw"] != Version {
		t.Errorf("origin: %v", sw["origin"])
	}

	light := decodeConfig(t, f.tr.mustFind(t, "homeassistant/light/hub1_light_kitchen/config"))
	if light["schema"] != "json" || light["brightness"] != true || light["color_temp_kelvin"] != true || light["max_color_temp_kelvin"] != float64(6500) {
		t.Errorf("light: %v", light)
	}
	if m, _ := light["supported_color_modes"].([]any); len(m) != 2 || m[0] != "rgb" {
		t.Errorf("light modes: %v", light["supported_color_modes"])
	}

	sensor := decodeConfig(t, f.tr.mustFind(t, "homeassistant/sensor/hub1_sensor_outdoor/config"))
	if sensor["unit_of_measurement"] != "°C" || sensor["device_class"] != "temperature" || sensor["state_class"] != "measurement" {
		t.Errorf("sensor: %v", sensor)
	}
	note := decodeConfig(t, f.tr.mustFind(t, "homeassistant/sensor/hub1_sensor_note/config"))
	if _, ok := note["state_class"]; ok {
		t.Errorf("a sensor without a unit must not carry a state class: %v", note)
	}

	num := decodeConfig(t, f.tr.mustFind(t, "homeassistant/number/hub1_number_target/config"))
	if num["min"] != float64(5) || num["max"] != float64(30) || num["step"] != 0.5 || num["unit_of_measurement"] != "°C" || num["command_topic"] != "miniha/hub1/cmd/number.target" {
		t.Errorf("number: %v", num)
	}

	cl := decodeConfig(t, f.tr.mustFind(t, "homeassistant/climate/hub1_climate_hvac/config"))
	for k, want := range map[string]string{
		"mode_command_topic": "miniha/hub1/mode/climate.hvac", "mode_state_topic": "miniha/hub1/state/climate.hvac",
		"temperature_command_topic": "miniha/hub1/temp/climate.hvac", "temperature_state_topic": "miniha/hub1/temp_state/climate.hvac",
		"current_temperature_topic": "miniha/hub1/curtemp/climate.hvac",
	} {
		if cl[k] != want {
			t.Errorf("climate %s = %v, want %q", k, cl[k], want)
		}
	}

	if _, ok := f.tr.find("homeassistant/automation/hub1_automation_morning/config"); ok {
		t.Error("automation must not be exported")
	}
}

func TestExportStatePublishes(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	if p := f.tr.mustFind(t, "miniha/hub1/state/switch.lamp"); p.payload != "on" || !p.retain {
		t.Errorf("switch state: %+v", p)
	}
	jsonEq(t, f.tr.mustFind(t, "miniha/hub1/state/light.kitchen").payload, `{"state":"ON","brightness":128,"color_mode":"rgb","color":{"r":255,"g":0,"b":10}}`)
	if p := f.tr.mustFind(t, "miniha/hub1/state/sensor.outdoor"); p.payload != "21.5" {
		t.Errorf("sensor state: %+v", p)
	}
	var attrs map[string]any
	p := f.tr.mustFind(t, "miniha/hub1/attr/sensor.outdoor")
	if err := json.Unmarshal([]byte(p.payload), &attrs); err != nil || attrs["unit_of_measurement"] != "°C" || !p.retain {
		t.Errorf("attributes: %+v (%v)", p, err)
	}
	if p := f.tr.mustFind(t, "miniha/hub1/state/climate.hvac"); p.payload != "heat" {
		t.Errorf("climate mode: %+v", p)
	}
	if p := f.tr.mustFind(t, "miniha/hub1/temp_state/climate.hvac"); p.payload != "21" {
		t.Errorf("climate target: %+v", p)
	}
	if p := f.tr.mustFind(t, "miniha/hub1/curtemp/climate.hvac"); p.payload != "19.5" {
		t.Errorf("climate current: %+v", p)
	}

	if err := f.c.SetState("switch.lamp", "off", map[string]any{"friendly_name": "Lamp"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	f.waitIdle(t)
	if p := f.tr.mustFind(t, "miniha/hub1/state/switch.lamp"); p.payload != "off" {
		t.Errorf("after change: %+v", p)
	}
	if err := f.c.SetState("light.kitchen", "off", map[string]any{"friendly_name": "Kitchen", "supported_color_modes": []any{"rgb", "color_temp"}}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	f.waitIdle(t)
	jsonEq(t, f.tr.mustFind(t, "miniha/hub1/state/light.kitchen").payload, `{"state":"OFF"}`)
	if err := f.c.SetState("sensor.outdoor", core.StateUnavailable, map[string]any{"friendly_name": "Outdoor"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	f.waitIdle(t)
	if p := f.tr.mustFind(t, "miniha/hub1/state/sensor.outdoor"); p.payload != "None" {
		t.Errorf("unavailable sensor: %+v", p)
	}
}

func TestExportNewEntityGetsConfig(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	if err := f.c.SetState("binary_sensor.door", "on", map[string]any{"friendly_name": "Door", "device_class": "door"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	f.waitIdle(t)
	cfg := decodeConfig(t, f.tr.mustFind(t, "homeassistant/binary_sensor/hub1_binary_sensor_door/config"))
	if cfg["device_class"] != "door" || cfg["payload_on"] != "on" {
		t.Errorf("binary sensor: %v", cfg)
	}
	if p := f.tr.mustFind(t, "miniha/hub1/state/binary_sensor.door"); p.payload != "on" {
		t.Errorf("state: %+v", p)
	}
}

func TestExportSwitchCommand(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	f.tr.inject("miniha/hub1/cmd/switch.lamp", "OFF")
	f.waitIdle(t)
	f.tr.inject("miniha/hub1/cmd/switch.lamp", "ON")
	f.waitIdle(t)
	calls, _ := f.sw.snapshot()
	if len(calls) != 2 || calls[0] != "off" || calls[1] != "on" {
		t.Fatalf("controller calls: %v", calls)
	}
}

func TestExportRetainedCommandIgnored(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	f.tr.injectRetained("miniha/hub1/cmd/switch.lamp", "OFF")
	f.waitIdle(t)
	if calls, _ := f.sw.snapshot(); len(calls) != 0 {
		t.Fatalf("a retained command was executed: %v", calls)
	}
}

func TestExportCommandForUnexportedEntityIgnored(t *testing.T) {
	f := newExportFixture(t, ExportOptions{Exclude: []string{"switch.lamp"}})
	f.tr.inject("miniha/hub1/cmd/switch.lamp", "OFF")
	f.waitIdle(t)
	if calls, _ := f.sw.snapshot(); len(calls) != 0 {
		t.Fatalf("an excluded entity was controlled: %v", calls)
	}
}

func TestExportLightJSONCommand(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	f.tr.inject("miniha/hub1/cmd/light.kitchen", `{"state":"ON","brightness":200,"color":{"r":1,"g":2,"b":3},"color_temp":3000,"effect":"rainbow"}`)
	f.waitIdle(t)
	calls, data := f.light.snapshot()
	if len(calls) != 1 || calls[0] != "on" {
		t.Fatalf("calls: %v", calls)
	}
	d := data[0]
	if d["brightness"] != float64(200) || d["color_temp_kelvin"] != float64(3000) || d["effect"] != "rainbow" {
		t.Errorf("turn_on data: %v", d)
	}
	if rgb, _ := d["rgb_color"].([]any); len(rgb) != 3 || rgb[2] != float64(3) {
		t.Errorf("rgb_color: %v", d["rgb_color"])
	}
	f.tr.inject("miniha/hub1/cmd/light.kitchen", `{"state":"OFF"}`)
	f.waitIdle(t)
	if calls, _ := f.light.snapshot(); len(calls) != 2 || calls[1] != "off" {
		t.Errorf("calls after OFF: %v", calls)
	}
}

func TestExportNumberAndClimateCommands(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	f.tr.inject("miniha/hub1/cmd/number.target", "22.5")
	f.tr.inject("miniha/hub1/mode/climate.hvac", "off")
	f.tr.inject("miniha/hub1/temp/climate.hvac", "23")
	f.waitIdle(t)
	if len(f.number.vals) != 1 || f.number.vals[0] != 22.5 {
		t.Errorf("number: %v", f.number.vals)
	}
	if len(f.climate.modes) != 1 || f.climate.modes[0] != "off" {
		t.Errorf("modes: %v", f.climate.modes)
	}
	if len(f.climate.temps) != 1 || f.climate.temps[0]["temperature"] != 23.0 {
		t.Errorf("temps: %v", f.climate.temps)
	}
}

func TestExportRemovalClearsConfig(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	cfgTopic := "homeassistant/switch/hub1_switch_lamp/config"
	if !f.c.RemoveState("switch.lamp", core.Context{}) {
		t.Fatal("no state to remove")
	}
	f.waitIdle(t)
	p := f.tr.mustFind(t, cfgTopic)
	if p.payload != "" || !p.retain {
		t.Fatalf("config not cleared: %+v", p)
	}
	if p := f.tr.mustFind(t, "miniha/hub1/state/switch.lamp"); p.payload != "" {
		t.Errorf("retained state not cleared: %+v", p)
	}
}

func TestExportIncludeExclude(t *testing.T) {
	f := newExportFixture(t, ExportOptions{Include: []string{"switch", "sensor.*", "climate.hvac"}, Exclude: []string{"sensor.note"}})
	for _, topic := range []string{
		"homeassistant/switch/hub1_switch_lamp/config",
		"homeassistant/sensor/hub1_sensor_outdoor/config",
		"homeassistant/climate/hub1_climate_hvac/config",
	} {
		if _, ok := f.tr.find(topic); !ok {
			t.Errorf("%s missing", topic)
		}
	}
	for _, topic := range []string{
		"homeassistant/sensor/hub1_sensor_note/config",
		"homeassistant/light/hub1_light_kitchen/config",
		"homeassistant/number/hub1_number_target/config",
	} {
		if _, ok := f.tr.find(topic); ok {
			t.Errorf("%s must not be exported", topic)
		}
	}
}

func TestExportStopPublishesOffline(t *testing.T) {
	f := newExportFixture(t, ExportOptions{Namespace: "home/x", DiscoveryPrefix: "ha"})
	if p := f.tr.mustFind(t, "home/x/status"); p.payload != "online" {
		t.Fatalf("birth: %+v", p)
	}
	if _, ok := f.tr.find("ha/switch/hub1_switch_lamp/config"); !ok {
		t.Error("custom discovery prefix not used")
	}
	f.x.Stop()
	if p := f.tr.mustFind(t, "home/x/status"); p.payload != "offline" || !p.retain {
		t.Fatalf("offline: %+v", p)
	}
	before := f.tr.pubCount()
	f.tr.inject("home/x/cmd/switch.lamp", "OFF")
	if err := f.c.SetState("switch.lamp", "off", map[string]any{"friendly_name": "Lamp"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if f.tr.pubCount() != before {
		t.Error("exporter published after Stop")
	}
	if calls, _ := f.sw.snapshot(); len(calls) != 0 {
		t.Errorf("exporter handled a command after Stop: %v", calls)
	}
}

func TestExportRegistryUpdateResyncs(t *testing.T) {
	f := newExportFixture(t, ExportOptions{})
	f.x.debounceDelay = time.Millisecond
	before := f.tr.pubCount()
	f.c.Fire("registry_updated", nil, core.Context{})
	f.waitIdle(t)
	if n := f.tr.pubCount(); n != before {
		t.Errorf("an unchanged resync republished %d messages", n-before)
	}
	// An entity that appeared without a state_changed event reaching the
	// exporter (the resync is the safety net) is picked up as well.
	f.x.mu.Lock()
	delete(f.x.exported, "sensor.outdoor")
	delete(f.x.last, "homeassistant/sensor/hub1_sensor_outdoor/config")
	f.x.mu.Unlock()
	f.c.Fire("registry_updated", nil, core.Context{})
	f.waitIdle(t)
	if f.tr.pubCount() == before {
		t.Error("resync did not republish the missing entity")
	}
}

func TestExportStartErrors(t *testing.T) {
	cases := map[string]struct {
		hub  string
		opts ExportOptions
		want string
	}{
		"hub id":    {"bad id", ExportOptions{Name: "n"}, "hub id"},
		"name":      {"hub1", ExportOptions{}, "name"},
		"namespace": {"hub1", ExportOptions{Name: "n", Namespace: "a/#"}, "namespace"},
		"glob":      {"hub1", ExportOptions{Name: "n", Include: []string{"sensor.[x"}}, "glob"},
		"empty":     {"hub1", ExportOptions{Name: "n", Exclude: []string{" "}}, "empty entry"},
	}
	for name, tc := range cases {
		x := NewExporter(core.New(), &fakeTransport{}, tc.opts, tc.hub, slog.Default())
		err := x.Start()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}
