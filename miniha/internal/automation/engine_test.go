/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// recorder is a light/switch that records service calls and mirrors
// the requested state.
type recorder struct {
	c  *core.Core
	id string
	mu sync.Mutex
	on []map[string]any
}

func (r *recorder) TurnOn(ctx context.Context, data map[string]any) error {
	r.mu.Lock()
	cp := map[string]any{}
	for k, v := range data {
		cp[k] = v
	}
	r.on = append(r.on, cp)
	r.mu.Unlock()
	return r.c.SetState(r.id, "on", nil, core.ContextFrom(ctx))
}

func (r *recorder) TurnOff(ctx context.Context, _ map[string]any) error {
	return r.c.SetState(r.id, "off", nil, core.ContextFrom(ctx))
}

func (r *recorder) calls() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any{}, r.on...)
}

type valueRec struct {
	mu   sync.Mutex
	vals []any
}

func (v *valueRec) SetValue(_ context.Context, val any) error {
	v.mu.Lock()
	v.vals = append(v.vals, val)
	v.mu.Unlock()
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
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

func never(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	c     *core.Core
	e     *Engine
	dir   string
	light *recorder
	sw    *recorder
}

func setup(t *testing.T, automations, scripts, scenes string) *fixture {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"automations.yaml": automations, "scripts.yaml": scripts, "scenes.yaml": scenes} {
		if content != "" {
			os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		}
	}
	c := core.New()
	f := &fixture{c: c, dir: dir}
	f.light = &recorder{c: c, id: "light.bulb"}
	f.sw = &recorder{c: c, id: "switch.tv"}
	c.SetState("light.bulb", "off", map[string]any{"friendly_name": "Bulb"}, core.Context{})
	c.SetState("switch.tv", "off", nil, core.Context{})
	c.SetState("sensor.temp", "20", map[string]any{"unit_of_measurement": "C"}, core.Context{})
	c.RegisterController("light.bulb", f.light)
	c.RegisterController("switch.tv", f.sw)
	loc := time.FixedZone("CET", 3600)
	e := New(c, Options{Dir: dir, Location: loc, Latitude: 52.52, Longitude: 13.405}, slog.Default())
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	f.e = e
	return f
}

func TestStateTriggerWithTemplateAndCondition(t *testing.T) {
	f := setup(t, `
- alias: Dim when TV on
  trigger:
    - platform: state
      entity_id: switch.tv
      to: "on"
  condition:
    - condition: state
      entity_id: light.bulb
      state: "off"
  action:
    - service: light.turn_on
      target:
        entity_id: light.bulb
      data:
        brightness_pct: "{{ 10 + 10 }}"
        note: "{{ 'from ' ~ trigger.from_state.state ~ ' to ' ~ trigger.to_state.state ~ ' via ' ~ trigger.entity_id if trigger.platform == 'state' else 'manual' }}"
`, "", "")
	if st := f.c.GetState("automation.dim_when_tv_on"); st == nil || st.State != "on" || st.Attributes["mode"] != "single" {
		t.Fatalf("automation entity: %+v", st)
	}
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	eventually(t, "light call", func() bool { return len(f.light.calls()) == 1 })
	call := f.light.calls()[0]
	if call["brightness_pct"] != int64(20) || call["note"] != "from off to on via switch.tv" {
		t.Fatalf("call %+v", call)
	}
	if st := f.c.GetState("automation.dim_when_tv_on"); st.Attributes["last_triggered"] == nil {
		t.Fatal("last_triggered not set")
	}
	// Condition now false (light on): no second call.
	f.c.SetState("switch.tv", "off", nil, core.Context{})
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	never(t, "second call", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 1 })
	// automation.trigger skips conditions by default.
	f.c.CallService(context.Background(), "automation", "trigger", map[string]any{"entity_id": "automation.dim_when_tv_on"}, core.Context{})
	eventually(t, "manual trigger", func() bool { return len(f.light.calls()) == 2 })
	if f.light.calls()[1]["note"] != "manual" {
		t.Fatalf("manual call %+v", f.light.calls()[1])
	}
	// Disabled automations do not fire.
	f.c.CallService(context.Background(), "automation", "turn_off", map[string]any{"entity_id": "automation.dim_when_tv_on"}, core.Context{})
	if f.c.GetState("automation.dim_when_tv_on").State != "off" {
		t.Fatal("not off")
	}
	f.c.SetState("light.bulb", "off", nil, core.Context{})
	f.c.SetState("switch.tv", "off", nil, core.Context{})
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	never(t, "call while disabled", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 2 })
}

func TestStateTriggerFor(t *testing.T) {
	f := setup(t, `
- alias: Held
  triggers:
    - trigger: state
      entity_id: switch.tv
      to: "on"
      for:
        milliseconds: 80
  actions:
    - action: light.turn_on
      target: { entity_id: light.bulb }
      data: { held: "{{ trigger.for.total_seconds() }}" }
`, "", "")
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	time.Sleep(30 * time.Millisecond)
	f.c.SetState("switch.tv", "off", nil, core.Context{})
	never(t, "fire after flap", 150*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	eventually(t, "fire after hold", func() bool { return len(f.light.calls()) == 1 })
	if f.light.calls()[0]["held"] != 0.08 {
		t.Fatalf("%+v", f.light.calls()[0])
	}
}

func TestNumericAndTemplateTriggers(t *testing.T) {
	f := setup(t, `
- alias: Hot
  trigger:
    platform: numeric_state
    entity_id: sensor.temp
    above: 25
  action:
    service: switch.turn_on
    entity_id: switch.tv
- alias: Tpl
  trigger:
    platform: template
    value_template: "{{ states('sensor.temp') | float < 10 }}"
  action:
    - service: light.turn_on
      target: { entity_id: light.bulb }
`, "", "")
	f.c.SetState("sensor.temp", "26", nil, core.Context{})
	eventually(t, "numeric fire", func() bool { return len(f.sw.calls()) == 1 })
	f.c.SetState("sensor.temp", "27", nil, core.Context{})
	never(t, "refire while above", 100*time.Millisecond, func() bool { return len(f.sw.calls()) > 1 })
	f.c.SetState("sensor.temp", "20", nil, core.Context{})
	f.c.SetState("sensor.temp", "30", nil, core.Context{})
	eventually(t, "numeric refire after leaving range", func() bool { return len(f.sw.calls()) == 2 })
	f.c.SetState("sensor.temp", "5", nil, core.Context{})
	eventually(t, "template fire", func() bool { return len(f.light.calls()) == 1 })
	f.c.SetState("sensor.temp", "4", nil, core.Context{})
	never(t, "template refire while true", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 1 })
}

func TestActionsFlow(t *testing.T) {
	f := setup(t, `
- alias: Flow
  mode: restart
  trigger:
    - platform: event
      event_type: go
  variables:
    level: 42
  action:
    - variables:
        doubled: "{{ level * 2 }}"
    - choose:
        - conditions: "{{ doubled == 84 }}"
          sequence:
            - service: light.turn_on
              target: { entity_id: light.bulb }
              data: { step: choose }
      default:
        - service: light.turn_on
          data: { step: default }
          target: { entity_id: light.bulb }
    - if:
        - condition: template
          value_template: "{{ is_state('light.bulb', 'on') }}"
      then:
        - repeat:
            count: 2
            sequence:
              - service: light.turn_on
                target: { entity_id: light.bulb }
                data: { step: "repeat {{ repeat.index }}" }
      else:
        - stop: never
    - delay: "00:00:00.03"
    - wait_template: "{{ is_state('switch.tv', 'on') }}"
      timeout: "00:00:00.05"
      continue_on_timeout: true
    - service: light.turn_on
      target: { entity_id: light.bulb }
      data: { step: "wait {{ wait.completed }}" }
    - condition: state
      entity_id: switch.tv
      state: "on"
    - service: light.turn_on
      target: { entity_id: light.bulb }
      data: { step: unreachable }
`, "", "")
	f.c.Fire("go", nil, core.NewContext())
	eventually(t, "sequence", func() bool { return len(f.light.calls()) == 4 })
	steps := []string{}
	for _, c := range f.light.calls() {
		steps = append(steps, c["step"].(string))
	}
	want := []string{"choose", "repeat 1", "repeat 2", "wait False"}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps %v", steps)
		}
	}
	never(t, "after failed condition", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 4 })
}

func TestModes(t *testing.T) {
	f := setup(t, `
- alias: Single
  trigger: { platform: event, event_type: s }
  action:
    - delay: { milliseconds: 100 }
    - service: light.turn_on
      target: { entity_id: light.bulb }
- alias: Restart
  mode: restart
  trigger: { platform: event, event_type: r }
  action:
    - delay: { milliseconds: 100 }
    - service: switch.turn_on
      target: { entity_id: switch.tv }
`, "", "")
	f.c.Fire("s", nil, core.NewContext())
	f.c.Fire("s", nil, core.NewContext())
	eventually(t, "single run", func() bool { return len(f.light.calls()) == 1 })
	never(t, "second single run", 150*time.Millisecond, func() bool { return len(f.light.calls()) > 1 })
	f.c.Fire("r", nil, core.NewContext())
	time.Sleep(50 * time.Millisecond)
	f.c.Fire("r", nil, core.NewContext())
	eventually(t, "restart run", func() bool { return len(f.sw.calls()) == 1 })
	never(t, "cancelled run finishing", 150*time.Millisecond, func() bool { return len(f.sw.calls()) > 1 })
}

func TestScriptsAndScenes(t *testing.T) {
	f := setup(t, "", `
greet:
  alias: Greet
  sequence:
    - service: light.turn_on
      target: { entity_id: light.bulb }
      data: { who: "{{ name | default('nobody') }}" }
`, `
- name: Movie time
  entities:
    light.bulb:
      state: "on"
      brightness: 30
    switch.tv: "on"
    number.vol: 5
`)
	vol := &valueRec{}
	f.c.SetState("number.vol", "1", nil, core.Context{})
	f.c.RegisterController("number.vol", vol)
	if _, err := f.c.CallService(context.Background(), "script", "greet", map[string]any{"name": "Bob"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "script run", func() bool { return len(f.light.calls()) == 1 })
	if f.light.calls()[0]["who"] != "Bob" {
		t.Fatalf("%+v", f.light.calls())
	}
	f.c.CallService(context.Background(), "script", "turn_on", map[string]any{"entity_id": "script.greet"}, core.Context{})
	eventually(t, "script.turn_on", func() bool { return len(f.light.calls()) == 2 && f.light.calls()[1]["who"] == "nobody" })
	if f.c.GetState("script.greet") == nil {
		t.Fatal("script entity missing")
	}
	if _, err := f.c.CallService(context.Background(), "scene", "turn_on", map[string]any{"entity_id": "scene.movie_time"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if len(f.light.calls()) != 3 || f.light.calls()[2]["brightness"] != int64(30) && f.light.calls()[2]["brightness"] != uint64(30) {
		t.Fatalf("scene light call %+v", f.light.calls())
	}
	if f.c.GetState("switch.tv").State != "on" || len(vol.vals) != 1 || vol.vals[0] != int64(5) {
		t.Fatalf("scene: tv %s vol %v", f.c.GetState("switch.tv").State, vol.vals)
	}
	if st := f.c.GetState("scene.movie_time"); st.State == "unknown" {
		t.Fatal("scene activation time not recorded")
	}
}

func TestReload(t *testing.T) {
	f := setup(t, `
- alias: One
  trigger: { platform: event, event_type: x }
  action: { service: light.turn_on, entity_id: light.bulb }
`, "", "")
	if f.c.GetState("automation.one") == nil {
		t.Fatal("missing")
	}
	os.WriteFile(filepath.Join(f.dir, "automations.yaml"), []byte(`
- alias: Two
  trigger: { platform: event, event_type: y }
  action: { service: switch.turn_on, entity_id: switch.tv }
`), 0o644)
	if err := f.e.Reload(); err != nil {
		t.Fatal(err)
	}
	if f.c.GetState("automation.one") != nil || f.c.GetState("automation.two") == nil {
		t.Fatal("reload did not replace automations")
	}
	f.c.Fire("x", nil, core.NewContext())
	f.c.Fire("y", nil, core.NewContext())
	eventually(t, "new automation", func() bool { return len(f.sw.calls()) == 1 })
	never(t, "old automation", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	// Broken file: error returned, previous definitions kept.
	os.WriteFile(filepath.Join(f.dir, "automations.yaml"), []byte("- alias: Bad\n  action: []\n"), 0o644)
	if err := f.e.Reload(); err == nil || f.c.GetState("automation.two") == nil {
		t.Fatalf("broken reload: err=%v", err)
	}
	if len(f.e.Summary()) != 1 || f.e.Summary()[0]["alias"] != "Two" {
		t.Fatalf("summary %v", f.e.Summary())
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		"- alias: x\n  action: []\n",
		"- alias: x\n  trigger: {platform: state, entity_id: a.b}\n",
		"- alias: x\n  mode: weird\n  trigger: {platform: state, entity_id: a.b}\n  action: []\n",
		"- alias: x\n  nonsense: 1\n  trigger: {platform: state, entity_id: a.b}\n  action: []\n",
		"- alias: x\n  trigger: {entity_id: a.b}\n  action: []\n",
	} {
		if _, err := parseAutomations([]byte(bad), ""); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for in, want := range map[string]time.Duration{"00:01:30": 90 * time.Second, "5": 5 * time.Second, "02:30": 150 * time.Second, "1.5": 1500 * time.Millisecond} {
		d, err := parseDuration(in)
		if err != nil || d != want {
			t.Errorf("parseDuration(%q) = %v %v", in, d, err)
		}
	}
}

func TestTimeMath(t *testing.T) {
	loc := time.FixedZone("CEST", 7200)
	rise, set, ok := sunTimes(time.Date(2026, 6, 21, 12, 0, 0, 0, loc), 52.52, 13.405)
	if !ok {
		t.Fatal("no sun")
	}
	wantRise := time.Date(2026, 6, 21, 4, 43, 0, 0, loc)
	wantSet := time.Date(2026, 6, 21, 21, 33, 0, 0, loc)
	if d := rise.Sub(wantRise); d < -3*time.Minute || d > 3*time.Minute {
		t.Errorf("sunrise %v, want about %v", rise, wantRise)
	}
	if d := set.Sub(wantSet); d < -3*time.Minute || d > 3*time.Minute {
		t.Errorf("sunset %v, want about %v", set, wantSet)
	}
	h, _ := parsePattern("/5", true)
	m, _ := parsePattern("*", true)
	if !h.match(10) || h.match(11) || !m.match(59) {
		t.Error("pattern match")
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, loc)
	for _, c := range []struct{ h, m, s string }{{"", "/15", ""}} {
		cfg := map[string]any{}
		if c.m != "" {
			cfg["minutes"] = c.m
		}
		_ = cfg
	}
	_ = now
}
