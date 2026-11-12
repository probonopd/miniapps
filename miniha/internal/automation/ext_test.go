/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

func lastNote(r *recorder) any {
	calls := r.calls()
	if len(calls) == 0 {
		return nil
	}
	return calls[len(calls)-1]["note"]
}

func TestWebhookTrigger(t *testing.T) {
	f := setup(t, `
- alias: Hook
  trigger:
    - platform: webhook
      webhook_id: hook-one
  action:
    - service: light.turn_on
      entity_id: light.bulb
      data:
        note: "{{ trigger.platform }}:{{ trigger.json.name }}:{{ trigger.query.x }}"
- alias: Open hook
  trigger:
    - trigger: webhook
      webhook_id: hook-open
      allowed_methods: [GET]
      local_only: false
  action:
    - service: switch.turn_on
      entity_id: switch.tv
      data:
        note: "{{ trigger.webhook_id }}"
`, "", "")
	fire := func(id, method, remote string) {
		f.c.Fire("webhook", map[string]any{"webhook_id": id, "method": method, "remote": remote,
			"query": map[string]any{"x": "1"}, "json": map[string]any{"name": "bob"}}, core.NewContext())
	}
	// Default methods are POST and PUT, and only private or loopback
	// peers are accepted.
	fire("hook-one", "GET", "192.168.1.5:1234")
	fire("hook-one", "POST", "8.8.8.8:1234")
	fire("hook-one", "POST", "garbage")
	fire("hook-other", "POST", "127.0.0.1:1")
	never(t, "rejected webhook", 150*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	fire("hook-one", "POST", "192.168.1.5:1234")
	eventually(t, "private webhook", func() bool { return len(f.light.calls()) == 1 })
	fire("hook-one", "PUT", "[::1]:99")
	eventually(t, "loopback webhook", func() bool { return len(f.light.calls()) == 2 })
	if lastNote(f.light) != "webhook:bob:1" {
		t.Fatalf("variables: %v", lastNote(f.light))
	}
	// local_only: false accepts a public peer, but only for its methods.
	fire("hook-open", "POST", "8.8.8.8:1")
	never(t, "wrong method", 100*time.Millisecond, func() bool { return len(f.sw.calls()) > 0 })
	fire("hook-open", "GET", "8.8.8.8:1")
	eventually(t, "public webhook", func() bool { return len(f.sw.calls()) == 1 })
}

func TestWebhookTriggerErrors(t *testing.T) {
	f := setup(t, "", "", "")
	for _, cfg := range []map[string]any{
		{"platform": "webhook"},
		{"platform": "webhook", "webhook_id": "x", "allowed_methods": []any{"TRACE"}},
	} {
		if _, err := f.e.SubscribeTrigger(cfg, func(map[string]any) {}); err == nil {
			t.Errorf("accepted %v", cfg)
		}
	}
}

func TestTagTrigger(t *testing.T) {
	f := setup(t, `
- alias: Tag
  trigger:
    - platform: tag
      tag_id: [tag-a, tag-b]
      device_id: dev1
  action:
    - service: light.turn_on
      entity_id: light.bulb
      data:
        note: "{{ trigger.tag_id }}@{{ trigger.device_id }}"
`, "", "")
	f.c.Fire("tag_scanned", map[string]any{"tag_id": "tag-c", "device_id": "dev1"}, core.NewContext())
	f.c.Fire("tag_scanned", map[string]any{"tag_id": "tag-a", "device_id": "dev2"}, core.NewContext())
	never(t, "unmatched tag", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	f.c.Fire("tag_scanned", map[string]any{"tag_id": "tag-b", "device_id": "dev1"}, core.NewContext())
	eventually(t, "tag call", func() bool { return len(f.light.calls()) == 1 })
	if lastNote(f.light) != "tag-b@dev1" {
		t.Fatalf("note %v", lastNote(f.light))
	}
}

func setZones(c *core.Core) {
	c.SetState("zone.home", "0", map[string]any{"friendly_name": "Home", "latitude": 52.52, "longitude": 13.405, "radius": 100.0}, core.Context{})
	c.SetState("zone.work", "0", map[string]any{"friendly_name": "Work"}, core.Context{})
}

func TestZoneTrigger(t *testing.T) {
	f := setup(t, `
- alias: Arrive
  trigger:
    - platform: zone
      entity_id: device_tracker.phone
      zone: zone.home
      event: enter
  action:
    - service: light.turn_on
      entity_id: light.bulb
      data:
        note: "{{ trigger.event }} {{ trigger.zone.attributes.friendly_name }} {{ trigger.entity_id }} {{ trigger.to_state.state }}"
- alias: Depart
  trigger:
    - platform: zone
      entity_id: device_tracker.phone
      zone: zone.home
      event: leave
  action:
    - service: switch.turn_on
      entity_id: switch.tv
- alias: Work arrival
  trigger:
    - platform: zone
      entity_id: person.me
      zone: zone.work
      event: enter
  action:
    - service: switch.turn_off
      entity_id: switch.tv
`, "", "")
	setZones(f.c)
	far := map[string]any{"latitude": 52.60, "longitude": 13.405}
	near := map[string]any{"latitude": 52.5205, "longitude": 13.4051}
	f.c.SetState("device_tracker.phone", "not_home", far, core.Context{})
	f.c.SetState("person.me", "not_home", nil, core.Context{})
	never(t, "far tracker", 100*time.Millisecond, func() bool { return len(f.light.calls())+len(f.sw.calls()) > 0 })
	f.c.SetState("device_tracker.phone", "home", near, core.Context{})
	eventually(t, "enter", func() bool { return len(f.light.calls()) == 1 })
	if lastNote(f.light) != "enter Home device_tracker.phone home" {
		t.Fatalf("note %v", lastNote(f.light))
	}
	// Staying inside while attributes move does not re-enter.
	f.c.SetState("device_tracker.phone", "home", map[string]any{"latitude": 52.5206, "longitude": 13.4051}, core.Context{})
	never(t, "second enter", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 1 || len(f.sw.calls()) > 0 })
	f.c.SetState("device_tracker.phone", "not_home", far, core.Context{})
	eventually(t, "leave", func() bool { return len(f.sw.calls()) == 1 })
	// A tracker without coordinates is matched by the zone's name.
	f.c.SetState("person.me", "Work", nil, core.Context{})
	eventually(t, "work enter", func() bool { return f.c.GetState("switch.tv").State == "off" && len(f.sw.calls()) == 1 })
}

func TestZoneCondition(t *testing.T) {
	f := setup(t, `
- alias: Only at home
  trigger:
    - platform: event
      event_type: ping
  condition:
    - condition: zone
      entity_id: device_tracker.phone
      zone: zone.home
  action:
    - service: light.turn_on
      entity_id: light.bulb
`, "", "")
	setZones(f.c)
	f.c.SetState("device_tracker.phone", "not_home", map[string]any{"latitude": 40.0, "longitude": 10.0}, core.Context{})
	f.c.Fire("ping", nil, core.NewContext())
	never(t, "outside zone", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	f.c.SetState("device_tracker.phone", "home", map[string]any{"latitude": 52.52, "longitude": 13.405}, core.Context{})
	f.c.Fire("ping", nil, core.NewContext())
	eventually(t, "inside zone", func() bool { return len(f.light.calls()) == 1 })
	res := f.e.ValidateConfig(nil, []any{map[string]any{"condition": "zone", "entity_id": "light.bulb", "zone": "zone.home"}}, nil)
	if res["conditions"] == "valid" {
		t.Fatal("zone condition accepted a light")
	}
	res = f.e.ValidateConfig(nil, []any{map[string]any{"condition": "device", "device_id": "x"}}, nil)
	if s, _ := res["conditions"].(string); !strings.Contains(s, "device conditions are not supported") {
		t.Fatalf("device condition: %v", res)
	}
}

func TestDeviceTrigger(t *testing.T) {
	f := setup(t, `
- alias: Button
  trigger:
    - platform: device
      domain: mqtt
      device_id: abc123
      type: button_short_press
      subtype: button_1
  action:
    - service: light.turn_on
      entity_id: light.bulb
      data:
        note: "{{ trigger.type }}/{{ trigger.subtype }}/{{ trigger.payload }}"
`, "", "")
	fire := func(dev, typ, sub string) {
		f.c.Fire("device_trigger", map[string]any{"device_id": dev, "type": typ, "subtype": sub, "payload": "PRESS", "topic": "t"}, core.NewContext())
	}
	fire("other", "button_short_press", "button_1")
	fire("abc123", "button_long_press", "button_1")
	fire("abc123", "button_short_press", "button_2")
	never(t, "unmatched device trigger", 100*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	fire("abc123", "button_short_press", "button_1")
	eventually(t, "device call", func() bool { return len(f.light.calls()) == 1 })
	if lastNote(f.light) != "button_short_press/button_1/PRESS" {
		t.Fatalf("note %v", lastNote(f.light))
	}
	_, err := f.e.SubscribeTrigger(map[string]any{"platform": "device", "domain": "zha", "device_id": "a", "type": "b", "subtype": "c"}, func(map[string]any) {})
	if err == nil || err.Error() != "device triggers are only supported for domain mqtt" {
		t.Fatalf("other domain: %v", err)
	}
}

func TestTimeTriggerInputDatetime(t *testing.T) {
	f := setup(t, `
- alias: Alarm
  trigger:
    - platform: time
      at: input_datetime.alarm
  action:
    - service: light.turn_on
      entity_id: light.bulb
`, "", "")
	clock := func(d time.Duration) string { return f.e.now().Add(d).Format("15:04:05") }
	// The entity does not exist yet, then holds a far-away time: no fire.
	f.c.SetState("input_datetime.alarm", clock(-time.Hour), nil, core.Context{})
	never(t, "early alarm", 200*time.Millisecond, func() bool { return len(f.light.calls()) > 0 })
	// Changing the entity re-arms the timer.
	f.c.SetState("input_datetime.alarm", clock(2*time.Second), nil, core.Context{})
	eventually(t, "alarm", func() bool { return len(f.light.calls()) == 1 })
	// A date and time is an absolute moment.
	f.c.SetState("input_datetime.alarm", f.e.now().Add(2*time.Second).Format("2006-01-02 15:04:05"), nil, core.Context{})
	eventually(t, "dated alarm", func() bool { return len(f.light.calls()) == 2 })
	clk, abs, err := f.e.resolveAt("07:30:15")
	if err != nil || clk == nil || clk.h != 7 || clk.m != 30 || clk.s != 15 || !abs.IsZero() {
		t.Fatalf("resolveAt clock: %v %v %v", clk, abs, err)
	}
	if _, _, err := f.e.resolveAt("tomorrow"); err == nil {
		t.Fatal("resolveAt accepted garbage")
	}
}

const motionBlueprint = `blueprint:
  name: Motion light
  description: Turns lights on
  domain: automation
  input:
    motion_entity:
      name: Motion
      selector:
        entity:
          domain: switch
    lights:
      name: Lights
    brightness:
      name: Brightness
      default: 50
    note:
      name: Note
      default: "plain {{ trigger.platform }}"
trigger:
  - platform: state
    entity_id: !input motion_entity
    to: "on"
action:
  # !input in a comment is ignored
  - service: light.turn_on
    target:
      entity_id: !input lights
    data:
      brightness_pct: !input brightness
      note: !input note
`

func writeBlueprint(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, "blueprints", "automation", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBlueprint(t *testing.T) {
	f := setup(t, "", "", "")
	writeBlueprint(t, f.dir, "homeassistant/motion_light.yaml", motionBlueprint)
	autos := func(body string) {
		os.WriteFile(filepath.Join(f.dir, "automations.yaml"), []byte(body), 0o644)
	}
	autos(`
- alias: Hallway motion
  description: from a blueprint
  use_blueprint:
    path: homeassistant/motion_light.yaml
    input:
      motion_entity: switch.tv
      lights: [light.bulb]
      note: "from {{ trigger.entity_id }}"
`)
	if err := f.e.Reload(); err != nil {
		t.Fatal(err)
	}
	sum := f.e.Summary()
	if len(sum) != 1 || sum[0]["alias"] != "Hallway motion" || sum[0]["description"] != "from a blueprint" {
		t.Fatalf("summary %v", sum)
	}
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	eventually(t, "blueprint run", func() bool { return len(f.light.calls()) == 1 })
	call := f.light.calls()[0]
	// The default brightness arrives as the YAML integer it is.
	if str(call["brightness_pct"]) != "50" || call["note"] != "from switch.tv" {
		t.Fatalf("call %+v", call)
	}

	// Missing required input.
	autos(`
- alias: Broken
  use_blueprint:
    path: homeassistant/motion_light.yaml
    input:
      lights: light.bulb
`)
	if err := f.e.Reload(); err == nil || !strings.Contains(err.Error(), `"motion_entity"`) {
		t.Fatalf("missing input: %v", err)
	}
	// Unknown input and path escapes are rejected.
	autos("- alias: A\n  use_blueprint: {path: homeassistant/motion_light.yaml, input: {motion_entity: a.b, lights: c.d, nope: 1}}\n")
	if err := f.e.Reload(); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("unknown input: %v", err)
	}
	autos("- alias: A\n  use_blueprint: {path: ../../x.yaml}\n")
	if err := f.e.Reload(); err == nil {
		t.Fatal("path escape accepted")
	}
	// Previous definitions survive the failed reloads.
	if len(f.e.Summary()) != 1 || f.e.Summary()[0]["alias"] != "Hallway motion" {
		t.Fatalf("summary after failures: %v", f.e.Summary())
	}

	// Validate checks shape only; ValidateWithBlueprints loads the file.
	good := []byte("- alias: A\n  use_blueprint: {path: homeassistant/motion_light.yaml, input: {motion_entity: a.b, lights: c.d}}\n")
	if err := Validate("automations.yaml", good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWithBlueprints(f.dir, "automations.yaml", good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWithBlueprints(f.dir, "automations.yaml", []byte("- alias: A\n  use_blueprint: {path: missing.yaml}\n")); err == nil {
		t.Fatal("missing blueprint file accepted")
	}
	if err := Validate("automations.yaml", []byte("- alias: A\n  use_blueprint: {input: {}}\n")); err == nil {
		t.Fatal("use_blueprint without path accepted")
	}
}

func TestBlueprintInputRendering(t *testing.T) {
	got, err := substituteInputs("a: !input x\nb: [!input y]\n# !input z\n", func(name string) (any, error) {
		switch name {
		case "x":
			return map[string]any{"k": []any{"a<b", int64(2)}}, nil
		case "y":
			return "q\"uote", nil
		}
		return nil, os.ErrInvalid
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "a: {\"k\": [\"a<b\", 2]}\nb: [\"q\\\"uote\"]\n# !input z\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTraces(t *testing.T) {
	f := setup(t, `
- alias: Traced
  trigger:
    - platform: event
      event_type: go
  condition:
    - condition: state
      entity_id: switch.tv
      state: "off"
  action:
    - service: light.turn_on
      entity_id: light.bulb
      data:
        brightness_pct: "{{ 5 * 2 }}"
    - choose:
        - conditions: "{{ true }}"
          sequence:
            - delay: 0
            - service: nosuch.thing
              data:
                a: 1
    - service: switch.turn_on
      entity_id: switch.tv
`, "", "")
	f.c.Fire("go", nil, core.NewContext())
	var tr map[string]any
	eventually(t, "finished trace", func() bool {
		traces := f.e.Traces("automation.traced")
		if len(traces) == 1 && traces[0]["finished"] != nil {
			tr = traces[0]
			return true
		}
		return false
	})
	if tr["conditions_passed"] != true || tr["run_id"] == "" || !strings.Contains(tr["error"].(string), "nosuch.thing") {
		t.Fatalf("trace %+v", tr)
	}
	if trig := tr["trigger"].(map[string]any); trig["platform"] != "event" {
		t.Fatalf("trigger %+v", trig)
	}
	steps := tr["steps"].([]any)
	paths := map[string]map[string]any{}
	for _, s := range steps {
		m := s.(map[string]any)
		paths[m["path"].(string)] = m
	}
	for _, p := range []string{"action/0", "action/1", "action/1/choose/0/sequence/0", "action/1/choose/0/sequence/1"} {
		if paths[p] == nil || paths[p]["finished"] == nil {
			t.Fatalf("missing step %s in %v", p, paths)
		}
	}
	if paths["action/2"] != nil {
		t.Fatal("run continued after the failing step")
	}
	if paths["action/0"]["type"] != "service" || paths["action/0"]["error"] != nil {
		t.Fatalf("step 0: %v", paths["action/0"])
	}
	detail := paths["action/0"]["detail"].(map[string]any)
	if detail["service"] != "light.turn_on" || detail["data"].(map[string]any)["brightness_pct"] != int64(10) {
		t.Fatalf("detail %v", detail)
	}
	failing := paths["action/1/choose/0/sequence/1"]
	if e, _ := failing["error"].(string); !strings.Contains(e, "nosuch.thing") {
		t.Fatalf("failing step: %v", failing)
	}
	if e, _ := paths["action/1"]["error"].(string); e == "" {
		t.Fatal("parent step lost the error")
	}
	if f.e.Summary()[0]["traces"] != 1 {
		t.Fatalf("summary %v", f.e.Summary())
	}

	// A failed condition is recorded without steps.
	f.c.SetState("switch.tv", "on", nil, core.Context{})
	f.c.Fire("go", nil, core.NewContext())
	eventually(t, "condition trace", func() bool { return len(f.e.Traces("automation.traced")) == 2 })
	newest := f.e.Traces("automation.traced")[0]
	if newest["conditions_passed"] != false || len(newest["steps"].([]any)) != 0 || newest["finished"] == nil {
		t.Fatalf("condition trace %+v", newest)
	}

	// The ring keeps the last five, newest first.
	for i := 0; i < 7; i++ {
		f.c.CallService(context.Background(), "automation", "trigger", map[string]any{"entity_id": "automation.traced", "skip_condition": false}, core.Context{})
	}
	eventually(t, "ring full", func() bool { return f.e.Summary()[0]["traces"] == 5 })
	if got := len(f.e.Traces("automation.traced")); got != 5 {
		t.Fatalf("%d traces", got)
	}
	if f.e.Traces("light.bulb") != nil {
		t.Fatal("traces for a non-automation")
	}
}
