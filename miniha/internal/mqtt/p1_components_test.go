/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

func TestZigbee2MQTTActionEvent(t *testing.T) {
	c, tr, _ := setup(t)
	now := time.Date(2026, 11, 6, 20, 15, 30, 123456000, time.UTC)
	c.Now = func() time.Time { return now }
	changes := 0
	c.Subscribe("state_changed", func(ev core.Event) {
		if ev.Data["entity_id"] == "event.hallway_button_action" {
			changes++
		}
	})
	tr.inject("homeassistant/event/0x00158d0002aabbcc/action/config", `{"name":"Action","object_id":"hallway_button_action","unique_id":"0x00158d0002aabbcc_action_zigbee2mqtt","state_topic":"zigbee2mqtt/Hallway button/action","value_template":"{\"event_type\": \"{{ value }}\"}","event_types":["single","double","hold"],"device_class":"button"}`)
	id := "event.hallway_button_action"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["event_type"] != nil || s.Attributes["device_class"] != "button" ||
		!reflect.DeepEqual(s.Attributes["event_types"], []string{"single", "double", "hold"}) {
		t.Fatalf("initial: %+v", s)
	}
	changes = 0
	tr.inject("zigbee2mqtt/Hallway button/action", "single")
	if s := state(t, c, id); s.State != "2026-11-06T20:15:30.123456+00:00" || s.Attributes["event_type"] != "single" {
		t.Fatalf("single: %+v", s)
	}
	// The same event twice in the same instant still fires twice.
	tr.inject("zigbee2mqtt/Hallway button/action", "single")
	if changes != 2 {
		t.Fatalf("state_changed fired %d times, want 2", changes)
	}
	tr.inject("zigbee2mqtt/Hallway button/action", "triple")
	if s := state(t, c, id); s.Attributes["event_type"] != "single" || changes != 2 {
		t.Fatalf("unknown event type accepted: %+v", s)
	}
}

func TestEventExtraAttributes(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/event/bell/config", `{"name":"Doorbell","object_id":"doorbell","unique_id":"bell1","state_topic":"home/doorbell/state","event_types":["press","hold"]}`)
	tr.inject("home/doorbell/state", `{"event_type":"press","duration":0.1}`)
	s := state(t, c, "event.doorbell")
	if s.Attributes["event_type"] != "press" || s.Attributes["duration"] != 0.1 {
		t.Fatalf("%+v", s)
	}
	// A retained message is history, not a new event.
	before := s.State
	tr.mu.Lock()
	subs := append([]fakeSub(nil), tr.subs...)
	tr.mu.Unlock()
	for _, sub := range subs {
		if MatchTopic(sub.filter, "home/doorbell/state") {
			sub.cb(Message{Topic: "home/doorbell/state", Payload: []byte(`{"event_type":"hold"}`), Retained: true})
		}
	}
	if s := state(t, c, "event.doorbell"); s.State != before || s.Attributes["event_type"] != "press" {
		t.Fatalf("retained event applied: %+v", s)
	}
	tr.inject("homeassistant/event/bad/config", `{"name":"Bad","object_id":"bad_event","unique_id":"bad1","event_types":["press"]}`)
	if c.GetState("event.bad_event") != nil {
		t.Fatal("event without state_topic created")
	}
}

func TestDeviceTracker(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/device_tracker/phone/config", `{"name":"Phone","object_id":"phone","unique_id":"phone1","state_topic":"location/phone/state","json_attributes_topic":"location/phone/attrs","payload_home":"present","payload_not_home":"absent","source_type":"bluetooth_le"}`)
	id := "device_tracker.phone"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["source_type"] != "bluetooth_le" {
		t.Fatalf("initial: %+v", s)
	}
	for payload, want := range map[string]string{"present": "home", "absent": "not_home", "Office": "Office"} {
		tr.inject("location/phone/state", payload)
		if s := state(t, c, id); s.State != want {
			t.Fatalf("%s: %+v", payload, s)
		}
	}
	tr.inject("location/phone/attrs", `{"latitude":52.52,"longitude":13.405,"gps_accuracy":12}`)
	if s := state(t, c, id); s.Attributes["latitude"] != 52.52 || s.Attributes["longitude"] != 13.405 || s.Attributes["source_type"] != "bluetooth_le" {
		t.Fatalf("attributes: %+v", s)
	}
	tr.inject("location/phone/state", "None")
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("reset: %+v", s)
	}

	// Defaults: gps source, home/not_home payloads.
	tr.inject("homeassistant/device_tracker/car/config", `{"name":"Car","object_id":"car","unique_id":"car1","state_topic":"car/loc","value_template":"{{ value_json.zone }}"}`)
	tr.inject("car/loc", `{"zone":"home"}`)
	if s := state(t, c, "device_tracker.car"); s.State != "home" || s.Attributes["source_type"] != "gps" {
		t.Fatalf("car: %+v", s)
	}

	// A GPS tracker with only coordinates gets its zone from the core.
	c.SetLocator(func(lat, lon, accuracy float64) string {
		if lat == 52.52 && lon == 13.405 {
			return "home"
		}
		if accuracy == 500 {
			return "Office"
		}
		return "not_home"
	})
	tr.inject("homeassistant/device_tracker/gpsphone/config", `{"name":"GPS phone","object_id":"gps_phone","unique_id":"gps1","json_attributes_topic":"gps/phone/attrs","source_type":"gps"}`)
	gps := "device_tracker.gps_phone"
	if s := state(t, c, gps); s.State != core.StateUnknown {
		t.Fatalf("gps initial: %+v", s)
	}
	tr.inject("gps/phone/attrs", `{"latitude":52.52,"longitude":13.405,"gps_accuracy":10}`)
	if s := state(t, c, gps); s.State != "home" || s.Attributes["latitude"] != 52.52 || s.Attributes["source_type"] != "gps" {
		t.Fatalf("gps home: %+v", s)
	}
	tr.inject("gps/phone/attrs", `{"latitude":48.1,"longitude":11.5,"gps_accuracy":500}`)
	if s := state(t, c, gps); s.State != "Office" {
		t.Fatalf("gps zone: %+v", s)
	}
	tr.inject("gps/phone/attrs", `{"latitude":48.1,"longitude":11.5}`)
	if s := state(t, c, gps); s.State != "not_home" {
		t.Fatalf("gps not_home: %+v", s)
	}
	// Attributes without coordinates leave the state alone.
	tr.inject("gps/phone/attrs", `{"battery":50}`)
	if s := state(t, c, gps); s.State != "not_home" {
		t.Fatalf("gps without coordinates: %+v", s)
	}

	tr.inject("homeassistant/device_tracker/bad/config", `{"name":"Bad","object_id":"bad_tracker","unique_id":"bad2","state_topic":"x","source_type":"magic"}`)
	if c.GetState("device_tracker.bad_tracker") != nil {
		t.Fatal("invalid source_type accepted")
	}
}

func TestESPHomeUpdate(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/update/livingroom/firmware/config", `{"name":"Firmware","object_id":"livingroom_firmware","unique_id":"livingroom_firmware","device_class":"firmware","state_topic":"livingroom/update/firmware/state","command_topic":"livingroom/update/firmware/command","payload_install":"INSTALL","display_precision":1}`)
	id := "update.livingroom_firmware"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["supported_features"] != 1 || s.Attributes["skipped_version"] != nil || s.Attributes["device_class"] != "firmware" {
		t.Fatalf("initial: %+v", s)
	}
	tr.inject("livingroom/update/firmware/state", `{"installed_version":"2024.5.0","latest_version":"2024.6.1","title":"ESPHome firmware","release_summary":"Bug fixes","release_url":"https://esphome.io/changelog/2024.6.0.html"}`)
	s := state(t, c, id)
	if s.State != core.StateOn || s.Attributes["installed_version"] != "2024.5.0" || s.Attributes["latest_version"] != "2024.6.1" ||
		s.Attributes["title"] != "ESPHome firmware" || s.Attributes["release_url"] != "https://esphome.io/changelog/2024.6.0.html" ||
		s.Attributes["in_progress"] != false || s.Attributes["display_precision"] != 1 {
		t.Fatalf("update available: %+v", s)
	}
	tr.inject("livingroom/update/firmware/state", `{"in_progress":true,"update_percentage":42.5}`)
	if s := state(t, c, id); s.Attributes["in_progress"] != true || s.Attributes["update_percentage"] != 42.5 || s.Attributes["supported_features"] != 1|4 {
		t.Fatalf("progress: %+v", s)
	}
	if _, err := c.CallService(context.Background(), "update", "install", map[string]any{"entity_id": id}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "livingroom/update/firmware/command" || p.payload != "INSTALL" {
		t.Fatalf("%+v", p)
	}
	if _, err := c.CallService(context.Background(), "update", "install", map[string]any{"entity_id": id, "version": "1.0"}, core.Context{}); err == nil {
		t.Fatal("install of a version other than the latest accepted")
	}
	tr.inject("livingroom/update/firmware/state", `{"installed_version":"2024.6.1","in_progress":false,"update_percentage":null}`)
	if s := state(t, c, id); s.State != core.StateOff || s.Attributes["update_percentage"] != nil {
		t.Fatalf("up to date: %+v", s)
	}
}

func TestUpdatePlainVersionAndLatestTopic(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/update/nas/config", `{"name":"NAS","object_id":"nas_update","unique_id":"nas_update","state_topic":"nas/version","latest_version_topic":"nas/latest","latest_version_template":"{{ value_json.tag_name }}"}`)
	id := "update.nas_update"
	tr.inject("nas/version", "7.2")
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["installed_version"] != "7.2" || s.Attributes["supported_features"] != 0 {
		t.Fatalf("installed only: %+v", s)
	}
	tr.inject("nas/latest", `{"tag_name":"7.3"}`)
	if s := state(t, c, id); s.State != core.StateOn || s.Attributes["latest_version"] != "7.3" {
		t.Fatalf("latest: %+v", s)
	}
	if _, err := c.CallService(context.Background(), "update", "install", map[string]any{"entity_id": id}, core.Context{}); err == nil {
		t.Fatal("install accepted without command_topic")
	}
	tr.inject("homeassistant/update/bad/config", `{"name":"Bad","object_id":"bad_update","unique_id":"bad3","state_topic":"x","command_topic":"y"}`)
	if c.GetState("update.bad_update") != nil {
		t.Fatal("command_topic without payload_install accepted")
	}
}

const templateLightCfg = `{"schema":"template","name":"Desk lamp","object_id":"desk_lamp","unique_id":"desklamp1","command_topic":"cmnd/desklamp/set","state_topic":"stat/desklamp/state",` +
	`"command_on_template":"on,{{ brightness|default }},{{ red|default }}-{{ green|default }}-{{ blue|default }},{{ color_temp|default }}",` +
	`"command_off_template":"off",` +
	`"state_template":"{{ value.split(',')[0] }}","brightness_template":"{{ value.split(',')[1] }}",` +
	`"red_template":"{{ value.split(',')[2].split('-')[0] }}","green_template":"{{ value.split(',')[2].split('-')[1] }}","blue_template":"{{ value.split(',')[2].split('-')[2] }}",` +
	`"color_temp_template":"{{ value.split(',')[3] }}","effect_template":"{{ value.split(',')[4] }}","effect_list":["rainbow","pulse"]}`

func TestTasmotaStyleTemplateLight(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/light/desklamp/config", templateLightCfg)
	id := "light.desk_lamp"
	s := state(t, c, id)
	if !reflect.DeepEqual(s.Attributes["supported_color_modes"], []string{"color_temp", "hs"}) || s.Attributes["supported_features"] != featEffect|featFlash|featTransition {
		t.Fatalf("initial: %+v", s)
	}
	if s.State != core.StateUnknown {
		t.Fatalf("state with state_topic should start unknown, got %q", s.State)
	}
	tr.inject("stat/desklamp/state", "on,128,255-0-0,,pulse")
	s = state(t, c, id)
	if s.State != "on" || s.Attributes["brightness"] != 128 || s.Attributes["color_mode"] != "hs" || s.Attributes["effect"] != "pulse" ||
		!reflect.DeepEqual(s.Attributes["rgb_color"], []int{255, 0, 0}) {
		t.Fatalf("rgb report: %+v", s)
	}
	tr.inject("stat/desklamp/state", "on,200,,250,")
	s = state(t, c, id)
	if s.Attributes["color_mode"] != "color_temp" || s.Attributes["color_temp"] != 250 || s.Attributes["brightness"] != 200 {
		t.Fatalf("color temp report: %+v", s)
	}
	tr.inject("stat/desklamp/state", "off,,,,")
	if s := state(t, c, id); s.State != "off" || s.Attributes["brightness"] != nil {
		t.Fatalf("off: %+v", s)
	}
	// Not optimistic: commands do not change the state by themselves.
	if _, err := c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "brightness": 90, "rgb_color": []any{255.0, 0.0, 0.0}}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "cmnd/desklamp/set" || p.payload != "on,90,255-0-0," {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "off" {
		t.Fatal("state changed without a report")
	}
	if _, err := c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "color_temp_kelvin": 4000}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "on,,--,250" {
		t.Fatalf("%+v", p)
	}
	if _, err := c.CallService(context.Background(), "light", "turn_off", map[string]any{"entity_id": id}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "off" {
		t.Fatalf("%+v", p)
	}
}

func TestTemplateLightOptimisticJSON(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/light/shelly/config", `{"schema":"template","name":"Shelly bulb","object_id":"shelly_bulb","unique_id":"shelly1","command_topic":"shellies/bulb/set","command_on_template":"{\"turn\":\"on\"{% if brightness is defined %},\"brightness\":{{ (brightness / 2.55) | round }}{% endif %}{% if transition is defined %},\"transition\":{{ (transition * 1000) | int }}{% endif %}}","command_off_template":"{\"turn\":\"off\"}","brightness_template":"{{ value_json.brightness }}"}`)
	id := "light.shelly_bulb"
	s := state(t, c, id)
	if !reflect.DeepEqual(s.Attributes["supported_color_modes"], []string{"brightness"}) {
		t.Fatalf("modes: %+v", s)
	}
	if _, err := c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "brightness": 255, "transition": 2}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	p := tr.lastPub(t)
	jsonEq(t, p.payload, `{"turn":"on","brightness":100,"transition":2000}`)
	if s := state(t, c, id); s.State != "on" || s.Attributes["brightness"] != 255 {
		t.Fatalf("optimistic on: %+v", s)
	}
	if _, err := c.CallService(context.Background(), "light", "turn_off", map[string]any{"entity_id": id}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != `{"turn":"off"}` {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "off" {
		t.Fatal("optimistic off not applied")
	}
	tr.inject("homeassistant/light/bad/config", `{"schema":"template","name":"Bad","object_id":"bad_light","unique_id":"bad4","command_topic":"x","command_on_template":"on"}`)
	tr.inject("homeassistant/light/bad2/config", `{"schema":"template","name":"Bad2","object_id":"bad_light2","unique_id":"bad5","command_topic":"x","command_on_template":"on","command_off_template":"off","red_template":"{{ 1 }}"}`)
	for _, bad := range []string{"light.bad_light", "light.bad_light2"} {
		if c.GetState(bad) != nil {
			t.Fatalf("%s created from an invalid config", bad)
		}
	}
}
