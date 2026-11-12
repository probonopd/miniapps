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

func callCover(t *testing.T, c *core.Core, service, id string, extra map[string]any) error {
	t.Helper()
	data := map[string]any{"entity_id": id}
	for k, v := range extra {
		data[k] = v
	}
	_, err := c.CallService(context.Background(), "cover", service, data, core.Context{})
	return err
}

func TestZigbee2MQTTCoverPosition(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/cover/0x00158d0001abcdef/cover/config", `{`+z2mAvail+`,"name":"Blind","object_id":"blind","unique_id":"0x00158d0001abcdef_cover_zigbee2mqtt","command_topic":"zigbee2mqtt/Blind/set","payload_open":"OPEN","payload_close":"CLOSE","payload_stop":"STOP","position_topic":"zigbee2mqtt/Blind","position_template":"{{ value_json.position }}","set_position_topic":"zigbee2mqtt/Blind/set","set_position_template":"{ \"position\": {{ position }} }","state_topic":"zigbee2mqtt/Blind","value_template":"{{ value_json.state }}","state_open":"OPEN","state_closed":"CLOSE","state_stopped":"STOP","device_class":"blind"}`)
	id := "cover.blind"
	if s := state(t, c, id); s.State != core.StateUnavailable {
		t.Fatalf("before availability: %q", s.State)
	}
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["supported_features"] != 15 || s.Attributes["device_class"] != "blind" {
		t.Fatalf("after availability: %+v", s)
	}
	tr.inject("zigbee2mqtt/Blind", `{"position":40,"state":"OPEN"}`)
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 40 {
		t.Fatalf("open: %+v", s)
	}
	tr.inject("zigbee2mqtt/Blind", `{"position":0,"state":"CLOSE"}`)
	if s := state(t, c, id); s.State != "closed" || s.Attributes["current_position"] != 0 {
		t.Fatalf("closed: %+v", s)
	}
	// A stopped blind with a position topic is closed only at position 0.
	tr.inject("zigbee2mqtt/Blind", `{"position":60,"state":"OPEN"}`)
	tr.inject("zigbee2mqtt/Blind", `{"position":60,"state":"STOP"}`)
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 60 {
		t.Fatalf("stopped: %+v", s)
	}
	tr.inject("zigbee2mqtt/Blind", `{"position":140,"state":"OPEN"}`)
	if s := state(t, c, id); s.Attributes["current_position"] != 60 {
		t.Fatalf("out-of-range position applied: %+v", s)
	}
	if err := callCover(t, c, "set_cover_position", id, map[string]any{"position": 25}); err != nil {
		t.Fatal(err)
	}
	p := tr.lastPub(t)
	if p.topic != "zigbee2mqtt/Blind/set" {
		t.Fatalf("%+v", p)
	}
	jsonEq(t, p.payload, `{"position":25}`)
	for service, want := range map[string]string{"open_cover": "OPEN", "close_cover": "CLOSE", "stop_cover": "STOP"} {
		if err := callCover(t, c, service, id, nil); err != nil {
			t.Fatal(err)
		}
		if p := tr.lastPub(t); p.topic != "zigbee2mqtt/Blind/set" || p.payload != want {
			t.Fatalf("%s: %+v", service, p)
		}
	}
	// Not optimistic: the device decides.
	if s := state(t, c, id); s.State != "open" {
		t.Fatalf("state moved without a report: %+v", s)
	}
	if err := callCover(t, c, "open_cover_tilt", id, nil); err == nil {
		t.Fatal("tilt accepted without a tilt topic")
	}
}

func TestTasmotaShutterCover(t *testing.T) {
	c, tr, _ := setup(t)
	// Tasmota's own abbreviated discovery for a shutter.
	tr.inject("homeassistant/cover/9E1A3C_SHT_1/config", `{"name":"Living Shutter","cmd_t":"cmnd/living_shutter/backlog","pos_t":"stat/living_shutter/RESULT","pos_tpl":"{{ value_json.Shutter1.Position }}","set_pos_t":"cmnd/living_shutter/ShutterPosition1","set_pos_tpl":"{{ value }}","pl_open":"ShutterOpen1","pl_cls":"ShutterClose1","pl_stop":"ShutterStop1","uniq_id":"9E1A3C_SHT_1","obj_id":"living_shutter"}`)
	id := "cover.living_shutter"
	tr.inject("stat/living_shutter/RESULT", `{"Shutter1":{"Position":100,"Direction":0,"Target":100}}`)
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 100 || s.Attributes["supported_features"] != 15 {
		t.Fatalf("%+v", s)
	}
	tr.inject("stat/living_shutter/RESULT", `{"Shutter1":{"Position":0,"Direction":0,"Target":0}}`)
	if s := state(t, c, id); s.State != "closed" {
		t.Fatalf("%+v", s)
	}
	if err := callCover(t, c, "set_cover_position", id, map[string]any{"position": 70}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "cmnd/living_shutter/ShutterPosition1" || p.payload != "70" {
		t.Fatalf("%+v", p)
	}
	if err := callCover(t, c, "open_cover", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "cmnd/living_shutter/backlog" || p.payload != "ShutterOpen1" {
		t.Fatalf("%+v", p)
	}
	// Toggle on a closed cover opens it.
	if err := callCover(t, c, "toggle", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "ShutterOpen1" {
		t.Fatalf("toggle: %+v", p)
	}
}

func TestCoverReversedRangeAndTilt(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/cover/venetian/config", `{"name":"Venetian","object_id":"venetian","unique_id":"venetian1","position_topic":"blinds/pos","position_open":0,"position_closed":100,"set_position_topic":"blinds/pos/set","command_topic":"blinds/cmd","tilt_command_topic":"blinds/tilt/set","tilt_status_topic":"blinds/tilt","tilt_min":0,"tilt_max":180,"tilt_opened_value":90,"tilt_closed_value":0,"tilt_command_template":"{{ value }}","payload_close":null}`)
	id := "cover.venetian"
	tr.inject("blinds/pos", "30")
	// 30 on a device where 0 is open and 100 closed is 70 percent open.
	if s := state(t, c, id); s.State != "open" || s.Attributes["current_position"] != 70 {
		t.Fatalf("%+v", s)
	}
	tr.inject("blinds/pos", "100")
	if s := state(t, c, id); s.State != "closed" || s.Attributes["current_position"] != 0 {
		t.Fatalf("%+v", s)
	}
	// Open 1 + SET_POSITION 4 + STOP 8 + tilt 16+32+64+128; close is disabled by payload_close: null.
	if s := state(t, c, id); s.Attributes["supported_features"] != 1+4+8+16+32+64+128 {
		t.Fatalf("features %v", s.Attributes["supported_features"])
	}
	if err := callCover(t, c, "close_cover", id, nil); err == nil {
		t.Fatal("close accepted although payload_close is null")
	}
	if err := callCover(t, c, "set_cover_position", id, map[string]any{"position": 70}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "blinds/pos/set" || p.payload != "30" {
		t.Fatalf("%+v", p)
	}
	tr.inject("blinds/tilt", "90")
	if s := state(t, c, id); s.Attributes["current_tilt_position"] != 50 {
		t.Fatalf("%+v", s)
	}
	tr.inject("blinds/tilt", "300")
	if s := state(t, c, id); s.Attributes["current_tilt_position"] != 50 {
		t.Fatalf("out-of-range tilt applied: %+v", s)
	}
	if err := callCover(t, c, "open_cover_tilt", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "blinds/tilt/set" || p.payload != "90" {
		t.Fatalf("%+v", p)
	}
	if err := callCover(t, c, "set_cover_tilt_position", id, map[string]any{"tilt_position": 100}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "180" {
		t.Fatalf("%+v", p)
	}
	if err := callCover(t, c, "stop_cover_tilt", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "blinds/tilt/set" || p.payload != "stop" {
		t.Fatalf("%+v", p)
	}
	// A tilt status topic means the device reports, so no optimistic guess.
	if s := state(t, c, id); s.Attributes["current_tilt_position"] != 50 {
		t.Fatalf("tilt moved without a report: %+v", s)
	}
}

func TestOptimisticCover(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/cover/garage/config", `{"name":"Garage door","object_id":"garage_door","unique_id":"garage1","command_topic":"garage/set","payload_open":"UP","payload_close":"DOWN","payload_stop":"HALT","device_class":"garage"}`)
	id := "cover.garage_door"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["assumed_state"] != true || s.Attributes["supported_features"] != 11 {
		t.Fatalf("%+v", s)
	}
	if err := callCover(t, c, "open_cover", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "garage/set" || p.payload != "UP" {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "open" {
		t.Fatal("optimistic open not applied")
	}
	// Toggle on an open cover closes it.
	if err := callCover(t, c, "toggle", id, nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "DOWN" {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "closed" {
		t.Fatal("optimistic close not applied")
	}
	if err := callCover(t, c, "set_cover_position", id, map[string]any{"position": 50}); err == nil {
		t.Fatal("set_position accepted without set_position_topic")
	}
}

func TestCoverStateTransitionsAndStopped(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/cover/gate/config", `{"name":"Gate","object_id":"gate","unique_id":"gate1","command_topic":"gate/set","state_topic":"gate/state"}`)
	id := "cover.gate"
	// Ordered: "closing" must be the last payload before "stopped".
	for _, payload := range []string{"opening", "open", "closing"} {
		tr.inject("gate/state", payload)
		if s := state(t, c, id); s.State != payload {
			t.Fatalf("%s: %+v", payload, s)
		}
	}
	// Stopped while closing means closed, as HA documents it.
	tr.inject("gate/state", "stopped")
	if s := state(t, c, id); s.State != "closed" {
		t.Fatalf("stopped after closing: %+v", s)
	}
	tr.inject("gate/state", "opening")
	tr.inject("gate/state", "stopped")
	if s := state(t, c, id); s.State != "open" {
		t.Fatalf("stopped after opening: %+v", s)
	}
	tr.inject("gate/state", "None")
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("reset: %+v", s)
	}
	tr.inject("gate/state", "ajar")
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("garbage payload applied: %+v", s)
	}
}

func TestCoverRejectsBadConfig(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/cover/a/config", `{"name":"No topics","object_id":"no_topics","unique_id":"c_a","state_topic":"x/state"}`)
	tr.inject("homeassistant/cover/b/config", `{"name":"Setpos only","object_id":"setpos_only","unique_id":"c_b","set_position_topic":"x/set"}`)
	for _, id := range []string{"cover.no_topics", "cover.setpos_only"} {
		if c.GetState(id) != nil {
			t.Fatalf("%s created from an invalid config", id)
		}
	}
}
