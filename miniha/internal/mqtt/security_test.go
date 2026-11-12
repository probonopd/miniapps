/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"testing"

	"github.com/probonopd/miniha/internal/core"
)

func secCall(t *testing.T, c *core.Core, domain, service string, data map[string]any) error {
	t.Helper()
	_, err := c.CallService(context.Background(), domain, service, data, core.Context{})
	return err
}

func TestLockZigbee2MQTT(t *testing.T) {
	c, tr, _ := setup(t)
	dev := `"device":{"identifiers":["zigbee2mqtt_0x00158d0001234567"],"name":"Front Door","manufacturer":"Yale","model":"YRD226"}`
	tr.inject("homeassistant/lock/0x00158d0001234567/lock/config", `{"name":null,"unique_id":"0x00158d0001234567_lock_zigbee2mqtt","command_topic":"zigbee2mqtt/Front Door/set/state","state_topic":"zigbee2mqtt/Front Door","value_template":"{{ value_json.state }}","payload_lock":"LOCK","payload_unlock":"UNLOCK","state_locked":"LOCK","state_unlocked":"UNLOCK",`+dev+`}`)
	id := "lock.front_door"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["supported_features"] != 0 {
		t.Fatalf("initial %+v", s)
	}
	tr.inject("zigbee2mqtt/Front Door", `{"state":"LOCK","battery":90}`)
	if state(t, c, id).State != "locked" {
		t.Fatalf("not locked: %+v", state(t, c, id))
	}
	tr.inject("zigbee2mqtt/Front Door", `{"state":"UNLOCK"}`)
	if state(t, c, id).State != "unlocked" {
		t.Fatal("not unlocked")
	}
	tr.inject("zigbee2mqtt/Front Door", `{"state":"JAMMED"}`)
	if state(t, c, id).State != "jammed" {
		t.Fatal("not jammed")
	}
	// A state word that is not configured is ignored, the state stays.
	tr.inject("zigbee2mqtt/Front Door", `{"state":"EXPLODED"}`)
	if state(t, c, id).State != "jammed" {
		t.Fatal("unknown payload applied")
	}
	if err := secCall(t, c, "lock", "lock", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/Front Door/set/state" || p.payload != "LOCK" {
		t.Fatalf("%+v", p)
	}
	// Not optimistic: the state follows the device.
	if state(t, c, id).State != "jammed" {
		t.Fatal("state changed before the device reported")
	}
	if err := secCall(t, c, "lock", "unlock", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "UNLOCK" {
		t.Fatalf("%+v", p)
	}
	if err := secCall(t, c, "lock", "open", map[string]any{"entity_id": id}); err == nil {
		t.Fatal("open accepted without payload_open")
	}
}

func TestLockCodeAndOpen(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/lock/keypad/config", `{"name":"Keypad","unique_id":"keypad","command_topic":"keypad/set","command_template":"{\"cmd\":\"{{ value }}\",\"pin\":\"{{ code }}\"}","payload_open":"LATCH","code_format":"^\\d{4}$","retain":false}`)
	id := "lock.keypad"
	s := state(t, c, id)
	if s.Attributes["code_format"] != `^\d{4}$` || s.Attributes["supported_features"] != 1 || s.Attributes["assumed_state"] != true {
		t.Fatalf("attributes %+v", s.Attributes)
	}
	n := tr.pubCount()
	for _, code := range []any{nil, "12", "12345", "abcd"} {
		data := map[string]any{"entity_id": id}
		if code != nil {
			data["code"] = code
		}
		if err := secCall(t, c, "lock", "unlock", data); err == nil {
			t.Fatalf("code %v accepted", code)
		}
	}
	if tr.pubCount() != n {
		t.Fatal("a rejected code was published")
	}
	if err := secCall(t, c, "lock", "unlock", map[string]any{"entity_id": id, "code": "1234"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "keypad/set" || p.payload != `{"cmd":"UNLOCK","pin":"1234"}` {
		t.Fatalf("%+v", p)
	}
	// No state_topic: optimistic.
	if state(t, c, id).State != "unlocked" {
		t.Fatal("optimistic unlock missing")
	}
	if err := secCall(t, c, "lock", "open", map[string]any{"entity_id": id, "code": "0000"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"cmd":"LATCH","pin":"0000"}` {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "open" {
		t.Fatal("optimistic open missing")
	}
}

func TestAlarmControlPanel(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/alarm_control_panel/house/config", `{"name":"House alarm","unique_id":"house_alarm","state_topic":"alarm/state","command_topic":"alarm/command","code":"1234","command_template":"{\"action\":\"{{ action }}\",\"code\":\"{{ code }}\"}","supported_features":["arm_home","arm_away","trigger"],"payload_arm_away":"AWAY"}`)
	id := "alarm_control_panel.house_alarm"
	s := state(t, c, id)
	if s.State != core.StateUnknown || s.Attributes["code_format"] != "number" || s.Attributes["code_arm_required"] != true || s.Attributes["supported_features"] != 1|2|8 {
		t.Fatalf("initial %+v", s)
	}
	if v, ok := s.Attributes["changed_by"]; !ok || v != nil {
		t.Fatalf("changed_by %v %v", v, ok)
	}
	for _, want := range []string{"armed_home", "pending", "triggered", "disarmed"} {
		tr.inject("alarm/state", want)
		if got := state(t, c, id).State; got != want {
			t.Fatalf("%s -> %s", want, got)
		}
	}
	tr.inject("alarm/state", "ARMED")
	if state(t, c, id).State != "disarmed" {
		t.Fatal("invalid state applied")
	}
	n := tr.pubCount()
	for _, svc := range []string{"alarm_arm_home", "alarm_disarm", "alarm_trigger"} {
		if err := secCall(t, c, "alarm_control_panel", svc, map[string]any{"entity_id": id, "code": "9999"}); err == nil {
			t.Fatalf("%s accepted a wrong code", svc)
		}
		if err := secCall(t, c, "alarm_control_panel", svc, map[string]any{"entity_id": id}); err == nil {
			t.Fatalf("%s accepted no code", svc)
		}
	}
	if tr.pubCount() != n {
		t.Fatal("a rejected code was published")
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_arm_away", map[string]any{"entity_id": id, "code": "1234"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "alarm/command" || p.payload != `{"action":"AWAY","code":"1234"}` {
		t.Fatalf("%+v", p)
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_disarm", map[string]any{"entity_id": id, "code": "1234"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"action":"DISARM","code":"1234"}` {
		t.Fatalf("%+v", p)
	}
	// Not in supported_features.
	if err := secCall(t, c, "alarm_control_panel", "alarm_arm_night", map[string]any{"entity_id": id, "code": "1234"}); err == nil {
		t.Fatal("arm_night accepted")
	}
	// Commands do not move the state: the panel reports it.
	if state(t, c, id).State != "disarmed" {
		t.Fatal("state moved without a report")
	}
}

func TestAlarmControlPanelCodeModes(t *testing.T) {
	c, tr, _ := setup(t)
	// No code: nothing to check, payloads go out plain, code_format is null.
	tr.inject("homeassistant/alarm_control_panel/a/config", `{"name":"Plain alarm","unique_id":"plain","state_topic":"a/s","command_topic":"a/c"}`)
	s := state(t, c, "alarm_control_panel.plain_alarm")
	if v, ok := s.Attributes["code_format"]; !ok || v != nil || s.Attributes["supported_features"] != 1|2|4|8|16|32 {
		t.Fatalf("%+v", s.Attributes)
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_arm_vacation", map[string]any{"entity_id": "alarm_control_panel.plain_alarm"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "a/c" || p.payload != "ARM_VACATION" {
		t.Fatalf("%+v", p)
	}
	// The device checks the code: any code is accepted and forwarded.
	tr.inject("homeassistant/alarm_control_panel/b/config", `{"name":"Remote alarm","unique_id":"remote","state_topic":"b/s","command_topic":"b/c","code":"REMOTE_CODE_TEXT","command_template":"{{ action }}:{{ code }}"}`)
	rid := "alarm_control_panel.remote_alarm"
	if state(t, c, rid).Attributes["code_format"] != "text" {
		t.Fatal("REMOTE_CODE_TEXT is not text")
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_arm_home", map[string]any{"entity_id": rid, "code": "secret"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "ARM_HOME:secret" {
		t.Fatalf("%+v", p)
	}
	// code_disarm_required false lets disarm through without a code.
	tr.inject("homeassistant/alarm_control_panel/c/config", `{"name":"Easy alarm","unique_id":"easy","state_topic":"c/s","command_topic":"c/c","code":"abcd","code_disarm_required":false}`)
	eid := "alarm_control_panel.easy_alarm"
	if state(t, c, eid).Attributes["code_format"] != "text" {
		t.Fatal("alphabetic code is not text")
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_disarm", map[string]any{"entity_id": eid}); err != nil {
		t.Fatal(err)
	}
	if err := secCall(t, c, "alarm_control_panel", "alarm_arm_home", map[string]any{"entity_id": eid}); err == nil {
		t.Fatal("arming without a code accepted")
	}
}

func TestValveReportsPosition(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/valve/garden/config", `{"name":"Garden water","unique_id":"garden_water","device_class":"water","command_topic":"garden/valve/set","state_topic":"garden/valve/state","reports_position":true,"payload_stop":"STOP"}`)
	id := "valve.garden_water"
	s := state(t, c, id)
	if s.State != core.StateUnknown || s.Attributes["supported_features"] != 1|2|4|8 || s.Attributes["device_class"] != "water" {
		t.Fatalf("initial %+v", s)
	}
	tr.inject("garden/valve/state", "50")
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 50 {
		t.Fatalf("%+v", s)
	}
	tr.inject("garden/valve/state", "0")
	if s := state(t, c, id); s.State != "closed" || s.Attributes["current_position"] != 0 {
		t.Fatalf("%+v", s)
	}
	tr.inject("garden/valve/state", `{"state":"opening","position":20}`)
	if s := state(t, c, id); s.State != "opening" || s.Attributes["current_position"] != 20 {
		t.Fatalf("%+v", s)
	}
	tr.inject("garden/valve/state", "250")
	if state(t, c, id).Attributes["current_position"] != 20 {
		t.Fatal("out-of-range position applied")
	}
	if err := secCall(t, c, "valve", "set_valve_position", map[string]any{"entity_id": id, "position": 30}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "garden/valve/set" || p.payload != "30" {
		t.Fatalf("%+v", p)
	}
	if err := secCall(t, c, "valve", "set_valve_position", map[string]any{"entity_id": id, "position": 130}); err == nil {
		t.Fatal("position above 100 accepted")
	}
	for svc, want := range map[string]string{"open_valve": "OPEN", "close_valve": "CLOSE", "stop_valve": "STOP"} {
		if err := secCall(t, c, "valve", svc, map[string]any{"entity_id": id}); err != nil {
			t.Fatal(err)
		}
		if p := tr.lastPub(t); p.payload != want {
			t.Fatalf("%s: %+v", svc, p)
		}
	}
	// Not optimistic: the reported state stays.
	if state(t, c, id).State != "opening" {
		t.Fatal("state changed before the device reported")
	}
	// Toggle on an opening valve closes it.
	if err := secCall(t, c, "valve", "toggle", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "CLOSE" {
		t.Fatalf("%+v", p)
	}
	tr.inject("garden/valve/state", "0")
	if err := secCall(t, c, "valve", "toggle", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "OPEN" {
		t.Fatalf("%+v", p)
	}
}

func TestValveScaledAndBinary(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/valve/s/config", `{"name":"Scaled","unique_id":"scaled","command_topic":"v/set","command_template":"{\"pos\":{{ value }}}","state_topic":"v/state","reports_position":true,"position_open":255,"position_closed":0}`)
	id := "valve.scaled"
	tr.inject("v/state", "255")
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 100 {
		t.Fatalf("%+v", s)
	}
	if err := secCall(t, c, "valve", "set_valve_position", map[string]any{"entity_id": id, "position": 50}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"pos":128}` {
		t.Fatalf("%+v", p)
	}
	// Binary valve: state words, no position, no stop.
	tr.inject("homeassistant/valve/b/config", `{"name":"Binary","unique_id":"binary","command_topic":"b/set","state_topic":"b/state","state_open":"ON","state_closed":"OFF"}`)
	bid := "valve.binary"
	if state(t, c, bid).Attributes["supported_features"] != 1|2 {
		t.Fatalf("%+v", state(t, c, bid).Attributes)
	}
	tr.inject("b/state", "ON")
	if state(t, c, bid).State != "open" {
		t.Fatal("not open")
	}
	tr.inject("b/state", "closing")
	if state(t, c, bid).State != "closing" {
		t.Fatal("not closing")
	}
	tr.inject("b/state", "bogus")
	if state(t, c, bid).State != "closing" {
		t.Fatal("bogus applied")
	}
	if err := secCall(t, c, "valve", "set_valve_position", map[string]any{"entity_id": bid, "position": 10}); err == nil {
		t.Fatal("position accepted without reports_position")
	}
	if err := secCall(t, c, "valve", "stop_valve", map[string]any{"entity_id": bid}); err == nil {
		t.Fatal("stop accepted without payload_stop")
	}
	// No state topic: optimistic.
	tr.inject("homeassistant/valve/o/config", `{"name":"Optimistic","unique_id":"opt","command_topic":"o/set"}`)
	if err := secCall(t, c, "valve", "open_valve", map[string]any{"entity_id": "valve.optimistic"}); err != nil {
		t.Fatal(err)
	}
	if state(t, c, "valve.optimistic").State != "open" {
		t.Fatal("optimistic open missing")
	}
}

func TestSirenWithTones(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/siren/hall/config", `{"name":"Hall siren","unique_id":"hall_siren","command_topic":"hall/siren/set","state_topic":"hall/siren","available_tones":["siren","beep"],"support_duration":true,"support_volume_set":true}`)
	id := "siren.hall_siren"
	s := state(t, c, id)
	if s.State != core.StateUnknown || s.Attributes["supported_features"] != 1|2|4|8|16 {
		t.Fatalf("initial %+v", s)
	}
	if tones, _ := s.Attributes["available_tones"].([]string); len(tones) != 2 || tones[0] != "siren" {
		t.Fatalf("tones %v", s.Attributes["available_tones"])
	}
	tr.inject("hall/siren", `{"state":"ON","tone":"siren","duration":10,"volume_level":0.5}`)
	s = state(t, c, id)
	if s.State != "on" || s.Attributes["tone"] != "siren" || s.Attributes["duration"] != 10.0 || s.Attributes["volume_level"] != 0.5 {
		t.Fatalf("%+v", s)
	}
	tr.inject("hall/siren", `{"state":"OFF"}`)
	if state(t, c, id).State != "off" {
		t.Fatal("not off")
	}
	if err := secCall(t, c, "siren", "turn_on", map[string]any{"entity_id": id, "tone": "beep", "duration": 5, "volume_level": 0.25}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "hall/siren/set" || p.payload != `{"state":"ON","tone":"beep","duration":5,"volume_level":0.25}` {
		t.Fatalf("%+v", p)
	}
	if err := secCall(t, c, "siren", "turn_on", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"state":"ON"}` {
		t.Fatalf("%+v", p)
	}
	n := tr.pubCount()
	if err := secCall(t, c, "siren", "turn_on", map[string]any{"entity_id": id, "tone": "foghorn"}); err == nil {
		t.Fatal("unknown tone accepted")
	}
	if err := secCall(t, c, "siren", "turn_on", map[string]any{"entity_id": id, "volume_level": 1.5}); err == nil {
		t.Fatal("volume above 1 accepted")
	}
	if tr.pubCount() != n {
		t.Fatal("an invalid request was published")
	}
	if err := secCall(t, c, "siren", "turn_off", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"state":"OFF"}` {
		t.Fatalf("%+v", p)
	}
	// Not optimistic: still off from the device report.
	if state(t, c, id).State != "off" {
		t.Fatal("state moved")
	}
}

func TestSirenTemplatesAndPlainPayloads(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/siren/garage/config", `{"name":"Garage siren","unique_id":"garage_siren","command_topic":"garage/siren/set","payload_on":"1","payload_off":"0","command_template":"{{ value }}/{{ tone }}","command_off_template":"stop","support_duration":false,"available_tones":["alarm"]}`)
	id := "siren.garage_siren"
	// No state_topic: optimistic. Duration is not supported, so it is dropped.
	if err := secCall(t, c, "siren", "turn_on", map[string]any{"entity_id": id, "tone": "alarm", "duration": 30}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "1/alarm" {
		t.Fatalf("%+v", p)
	}
	s := state(t, c, id)
	if s.State != "on" || s.Attributes["supported_features"] != 1|2|4|16 {
		t.Fatalf("%+v", s)
	}
	if err := secCall(t, c, "siren", "turn_off", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "stop" || state(t, c, id).State != "off" {
		t.Fatalf("%+v", p)
	}
	// A plain state topic with state_on/state_off.
	tr.inject("homeassistant/siren/porch/config", `{"name":"Porch siren","unique_id":"porch_siren","command_topic":"porch/set","state_topic":"porch/state","state_value_template":"{{ value_json.s }}","state_on":"RINGING","state_off":"QUIET"}`)
	tr.inject("porch/state", `{"s":"RINGING"}`)
	if state(t, c, "siren.porch_siren").State != "on" {
		t.Fatal("not on")
	}
	tr.inject("porch/state", `{"s":"QUIET"}`)
	if state(t, c, "siren.porch_siren").State != "off" {
		t.Fatal("not off")
	}
}

func TestLawnMower(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/lawn_mower/robo/config", `{"name":"Garden mower","unique_id":"garden_mower","activity_state_topic":"mower/state","activity_value_template":"{{ value_json.activity }}","start_mowing_command_topic":"mower/cmd/start","pause_command_topic":"mower/cmd/pause","dock_command_topic":"mower/cmd/dock","dock_command_template":"{\"cmd\":\"{{ value }}\"}"}`)
	id := "lawn_mower.garden_mower"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["supported_features"] != 1|2|4 {
		t.Fatalf("initial %+v", s)
	}
	for _, a := range []string{"mowing", "returning", "docked", "error", "paused"} {
		tr.inject("mower/state", `{"activity":"`+a+`"}`)
		if got := state(t, c, id).State; got != a {
			t.Fatalf("%s -> %s", a, got)
		}
	}
	tr.inject("mower/state", `{"activity":"sleeping"}`)
	if state(t, c, id).State != "paused" {
		t.Fatal("invalid activity applied")
	}
	for svc, want := range map[string]struct{ topic, payload string }{
		"start_mowing": {"mower/cmd/start", "start_mowing"},
		"pause":        {"mower/cmd/pause", "pause"},
		"dock":         {"mower/cmd/dock", `{"cmd":"dock"}`},
	} {
		if err := secCall(t, c, "lawn_mower", svc, map[string]any{"entity_id": id}); err != nil {
			t.Fatal(err)
		}
		if p := tr.lastPub(t); p.topic != want.topic || p.payload != want.payload {
			t.Fatalf("%s: %+v", svc, p)
		}
	}
	if state(t, c, id).State != "paused" {
		t.Fatal("state moved without a report")
	}
	// Only a start topic and no activity topic: optimistic, no pause or dock.
	tr.inject("homeassistant/lawn_mower/simple/config", `{"name":"Simple mower","unique_id":"simple_mower","start_mowing_command_topic":"simple/start"}`)
	sid := "lawn_mower.simple_mower"
	if state(t, c, sid).Attributes["supported_features"] != 1 {
		t.Fatalf("%+v", state(t, c, sid).Attributes)
	}
	if err := secCall(t, c, "lawn_mower", "start_mowing", map[string]any{"entity_id": sid}); err != nil {
		t.Fatal(err)
	}
	if state(t, c, sid).State != "mowing" {
		t.Fatal("optimistic start missing")
	}
	if err := secCall(t, c, "lawn_mower", "dock", map[string]any{"entity_id": sid}); err == nil {
		t.Fatal("dock accepted without dock_command_topic")
	}
}

func TestSecurityConfigErrors(t *testing.T) {
	c, tr, _ := setup(t)
	for comp, cfg := range map[string]string{
		"lock/l":                `{"name":"No command","unique_id":"l1"}`,
		"lock/m":                `{"name":"Duplicate states","unique_id":"l2","command_topic":"x","state_locked":"A","state_unlocked":"A"}`,
		"lock/n":                `{"name":"Bad regex","unique_id":"l3","command_topic":"x","code_format":"("}`,
		"siren/s":               `{"name":"No command siren","unique_id":"s1"}`,
		"alarm_control_panel/a": `{"name":"No state","unique_id":"a1","command_topic":"x"}`,
		"alarm_control_panel/b": `{"name":"Bad feature","unique_id":"a2","command_topic":"x","state_topic":"y","supported_features":["arm_moon"]}`,
		"valve/v":               `{"name":"Flat range","unique_id":"v1","command_topic":"x","position_open":5,"position_closed":5}`,
	} {
		tr.inject("homeassistant/"+comp+"/config", cfg)
	}
	if got := ids(c); len(got) != 0 {
		t.Fatalf("invalid configs created entities: %v", got)
	}
}
