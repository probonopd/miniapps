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

	"github.com/probonopd/miniha/internal/core"
)

func hvacCall(t *testing.T, c *core.Core, domain, service string, data map[string]any) error {
	t.Helper()
	_, err := c.CallService(context.Background(), domain, service, data, core.Context{})
	return err
}

func hvacMustCall(t *testing.T, c *core.Core, domain, service string, data map[string]any) {
	t.Helper()
	if err := hvacCall(t, c, domain, service, data); err != nil {
		t.Fatalf("%s.%s %v: %v", domain, service, data, err)
	}
}

func hvacMustFail(t *testing.T, c *core.Core, domain, service string, data map[string]any) {
	t.Helper()
	if err := hvacCall(t, c, domain, service, data); err == nil {
		t.Fatalf("%s.%s %v was accepted", domain, service, data)
	}
}

// hvacPubsSince returns what was published after the first n messages.
func hvacPubsSince(tr *fakeTransport, n int) []pub {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]pub(nil), tr.pubs[n:]...)
}

func hvacAttr(t *testing.T, c *core.Core, id, key string, want any) {
	t.Helper()
	got := state(t, c, id).Attributes[key]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s attribute %s = %#v, want %#v", id, key, got, want)
	}
}

func TestClimateZigbee2MQTTTRV(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/climate/0x00158d0001abcd12/climate/config", `{`+z2mAvail+`,"action_template":"{% set values = {None:None,'idle':'idle','heat':'heating','cool':'cooling','fan_only':'fan'} %}{{ values[value_json.running_state] }}","action_topic":"zigbee2mqtt/TRV","current_temperature_template":"{{ value_json.local_temperature }}","current_temperature_topic":"zigbee2mqtt/TRV","device":{"identifiers":["zigbee2mqtt_0x00158d0001abcd12"],"manufacturer":"Aqara","model":"Radiator thermostat E1 (SRTS-A01)","name":"TRV","sw_version":"0.0.0_0032"},"max_temp":"30","min_temp":"5","mode_command_topic":"zigbee2mqtt/TRV/set/system_mode","mode_state_template":"{{ value_json.system_mode }}","mode_state_topic":"zigbee2mqtt/TRV","modes":["off","auto","heat"],"name":null,"object_id":"trv","origin":{"name":"Zigbee2MQTT","sw":"1.35.1","url":"https://www.zigbee2mqtt.io"},"preset_mode_command_topic":"zigbee2mqtt/TRV/set/preset","preset_mode_state_topic":"zigbee2mqtt/TRV","preset_mode_value_template":"{{ value_json.preset }}","preset_modes":["manual","away"],"temp_step":0.5,"temperature_command_topic":"zigbee2mqtt/TRV/set/occupied_heating_setpoint","temperature_state_template":"{{ value_json.occupied_heating_setpoint }}","temperature_state_topic":"zigbee2mqtt/TRV","temperature_unit":"C","unique_id":"0x00158d0001abcd12_climate_zigbee2mqtt"}`)
	id := "climate.trv"
	if s := state(t, c, id); s.State != core.StateUnavailable {
		t.Fatalf("before the bridge is online: %+v", s)
	}
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("before the first report: %+v", s)
	}
	hvacAttr(t, c, id, "current_temperature", nil)
	hvacAttr(t, c, id, "temperature", nil)

	tr.inject("zigbee2mqtt/TRV", `{"system_mode":"heat","local_temperature":19.7,"occupied_heating_setpoint":21,"running_state":"heat","preset":"manual"}`)
	if s := state(t, c, id); s.State != "heat" {
		t.Fatalf("state %+v", s)
	}
	for k, want := range map[string]any{
		"hvac_modes":          []string{"off", "auto", "heat"},
		"min_temp":            5.0,
		"max_temp":            30.0,
		"target_temp_step":    0.5,
		"temperature":         21.0,
		"current_temperature": 19.7,
		"hvac_action":         "heating",
		"preset_mode":         "manual",
		"preset_modes":        []string{"manual", "away"},
		// target temperature, preset, turn off, turn on
		"supported_features": 1 + 16 + 128 + 256,
	} {
		hvacAttr(t, c, id, k, want)
	}
	if _, ok := state(t, c, id).Attributes["fan_mode"]; ok {
		t.Error("fan_mode reported without fan_mode_command_topic")
	}

	// The device is not optimistic: state follows its report only.
	hvacMustCall(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 21.5})
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/TRV/set/occupied_heating_setpoint" || p.payload != "21.5" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "temperature", 21.0)
	tr.inject("zigbee2mqtt/TRV", `{"system_mode":"heat","local_temperature":19.9,"occupied_heating_setpoint":21.5,"running_state":"idle","preset":"manual"}`)
	hvacAttr(t, c, id, "temperature", 21.5)
	hvacAttr(t, c, id, "hvac_action", "idle")

	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 31})
	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 4.5})
	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id})
	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "target_temp_high": 25, "target_temp_low": 18})
	hvacMustFail(t, c, "climate", "set_fan_mode", map[string]any{"entity_id": id, "fan_mode": "high"})
	hvacMustFail(t, c, "climate", "set_humidity", map[string]any{"entity_id": id, "humidity": 50})
	hvacMustFail(t, c, "climate", "set_hvac_mode", map[string]any{"entity_id": id, "hvac_mode": "cool"})

	n := tr.pubCount()
	hvacMustCall(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 22, "hvac_mode": "auto"})
	got := hvacPubsSince(tr, n)
	if len(got) != 2 || got[0].topic != "zigbee2mqtt/TRV/set/system_mode" || got[0].payload != "auto" || got[1].topic != "zigbee2mqtt/TRV/set/occupied_heating_setpoint" || got[1].payload != "22.0" {
		t.Fatalf("%+v", got)
	}
	hvacMustCall(t, c, "climate", "set_preset_mode", map[string]any{"entity_id": id, "preset_mode": "away"})
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/TRV/set/preset" || p.payload != "away" {
		t.Fatalf("%+v", p)
	}
	hvacMustFail(t, c, "climate", "set_preset_mode", map[string]any{"entity_id": id, "preset_mode": "eco"})

	// turn_off has no power topic and goes through the mode topic; turn_on
	// restores the last active mode the device reported.
	hvacMustCall(t, c, "climate", "turn_off", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/TRV/set/system_mode" || p.payload != "off" {
		t.Fatalf("%+v", p)
	}
	hvacMustCall(t, c, "climate", "turn_on", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/TRV/set/system_mode" || p.payload != "heat" {
		t.Fatalf("%+v", p)
	}

	// Bad reports are ignored, the reset payload clears a value.
	tr.inject("zigbee2mqtt/TRV", `{"system_mode":"turbo","local_temperature":"abc","occupied_heating_setpoint":21.5,"running_state":"heat","preset":"manual"}`)
	if s := state(t, c, id); s.State != "heat" || s.Attributes["current_temperature"] != 19.9 {
		t.Fatalf("bad report applied: %+v", s)
	}
	tr.inject("zigbee2mqtt/TRV", `{"system_mode":"off","local_temperature":null,"occupied_heating_setpoint":21.5,"running_state":"idle","preset":"none"}`)
	if s := state(t, c, id); s.State != "off" || s.Attributes["preset_mode"] != "none" {
		t.Fatalf("%+v", s)
	}
}

func TestClimateOptimisticHeatPump(t *testing.T) {
	c, tr, _ := setup(t)
	// An infrared bridge: nothing is reported back, so the entity assumes
	// every command took effect.
	tr.inject("homeassistant/climate/bedroom_ac/config", `{"name":"Bedroom AC","unique_id":"bedroom_ac","modes":["auto","off","cool","heat","dry","fan_only"],"mode_command_topic":"cmnd/bedroom_ac/mode","power_command_topic":"cmnd/bedroom_ac/power","power_command_template":"{{ value }}","payload_on":"ON","payload_off":"OFF","temperature_command_topic":"cmnd/bedroom_ac/temp","temperature_command_template":"{{ value | int }}","fan_mode_command_topic":"cmnd/bedroom_ac/fan","swing_mode_command_topic":"cmnd/bedroom_ac/swing","target_humidity_command_topic":"cmnd/bedroom_ac/humidity","current_temperature_topic":"tele/bedroom_ac/SENSOR","current_temperature_template":"{{ value_json.DS18B20.Temperature }}","qos":1}`)
	id := "climate.bedroom_ac"
	for k, want := range map[string]any{
		"temperature":        21.0,
		"fan_mode":           "low",
		"fan_modes":          []string{"auto", "low", "medium", "high"},
		"swing_mode":         "off",
		"swing_modes":        []string{"on", "off"},
		"humidity":           nil,
		"min_humidity":       30.0,
		"max_humidity":       99.0,
		"supported_features": 1 + 4 + 8 + 32 + 128 + 256,
	} {
		hvacAttr(t, c, id, k, want)
	}
	if s := state(t, c, id); s.State != "off" {
		t.Fatalf("assumed start state %+v", s)
	}
	tr.inject("tele/bedroom_ac/SENSOR", `{"Time":"2026-11-05T12:00:00","DS18B20":{"Id":"0000051B0A5F","Temperature":23.46},"TempUnit":"C"}`)
	hvacAttr(t, c, id, "current_temperature", 23.5)

	// No active mode is known yet: HA's order puts heat first in this list.
	hvacMustCall(t, c, "climate", "turn_on", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/power" || p.payload != "ON" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, id); s.State != "heat" {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "climate", "set_hvac_mode", map[string]any{"entity_id": id, "hvac_mode": "cool"})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/mode" || p.payload != "cool" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, id); s.State != "cool" {
		t.Fatalf("%+v", s)
	}
	hvacMustFail(t, c, "climate", "set_hvac_mode", map[string]any{"entity_id": id, "hvac_mode": "heat_cool"})
	hvacMustCall(t, c, "climate", "turn_off", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/power" || p.payload != "OFF" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, id); s.State != "off" {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "climate", "turn_on", map[string]any{"entity_id": id})
	if s := state(t, c, id); s.State != "cool" {
		t.Fatalf("turn_on did not restore the last mode: %+v", s)
	}

	hvacMustCall(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 23.4})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/temp" || p.payload != "23" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "temperature", 23.4)
	hvacMustCall(t, c, "climate", "set_fan_mode", map[string]any{"entity_id": id, "fan_mode": "high"})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/fan" || p.payload != "high" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "fan_mode", "high")
	hvacMustFail(t, c, "climate", "set_fan_mode", map[string]any{"entity_id": id, "fan_mode": "turbo"})
	hvacMustCall(t, c, "climate", "set_swing_mode", map[string]any{"entity_id": id, "swing_mode": "on"})
	hvacAttr(t, c, id, "swing_mode", "on")
	hvacMustCall(t, c, "climate", "set_humidity", map[string]any{"entity_id": id, "humidity": 50})
	if p := tr.lastPub(t); p.topic != "cmnd/bedroom_ac/humidity" || p.payload != "50" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "humidity", 50.0)
	hvacMustFail(t, c, "climate", "set_humidity", map[string]any{"entity_id": id, "humidity": 10})
	hvacMustFail(t, c, "climate", "set_humidity", map[string]any{"entity_id": id, "humidity": 100})
}

func TestClimateTemperatureRange(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/climate/hall/config", `{"name":"Hall","unique_id":"hall_climate","modes":["off","heat_cool"],"mode_command_topic":"hall/mode/set","mode_state_topic":"hall/state","mode_state_template":"{{ value_json.mode }}","temperature_low_command_topic":"hall/low/set","temperature_low_state_topic":"hall/state","temperature_low_state_template":"{{ value_json.low }}","temperature_high_command_topic":"hall/high/set","temperature_high_state_topic":"hall/state","temperature_high_state_template":"{{ value_json.high }}","min_temp":10,"max_temp":30}`)
	id := "climate.hall"
	tr.inject("hall/state", `{"mode":"heat_cool","low":19,"high":24}`)
	hvacAttr(t, c, id, "target_temp_low", 19.0)
	hvacAttr(t, c, id, "target_temp_high", 24.0)
	hvacAttr(t, c, id, "supported_features", 2+128+256)
	if _, ok := state(t, c, id).Attributes["temperature"]; ok {
		t.Error("single target reported on a range-only climate")
	}
	n := tr.pubCount()
	hvacMustCall(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "target_temp_low": 18.5, "target_temp_high": 25})
	got := hvacPubsSince(tr, n)
	if len(got) != 2 || got[0].topic != "hall/high/set" || got[0].payload != "25.0" || got[1].topic != "hall/low/set" || got[1].payload != "18.5" {
		t.Fatalf("%+v", got)
	}
	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "target_temp_high": 31})
	hvacMustFail(t, c, "climate", "set_temperature", map[string]any{"entity_id": id, "temperature": 20})
}

func TestFanTasmotaPercentage(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/fan/ceilingfan/config", `{"name":"Ceiling fan","unique_id":"ceilingfan_fan","command_topic":"cmnd/ceilingfan/POWER","state_topic":"stat/ceilingfan/POWER","availability_topic":"tele/ceilingfan/LWT","payload_available":"Online","payload_not_available":"Offline","percentage_command_topic":"cmnd/ceilingfan/FanSpeed","percentage_state_topic":"stat/ceilingfan/RESULT","percentage_value_template":"{{ value_json.FanSpeed }}","speed_range_min":1,"speed_range_max":3,"preset_mode_command_topic":"cmnd/ceilingfan/Preset","preset_modes":["breeze","sleep"],"oscillation_command_topic":"cmnd/ceilingfan/Oscillate","oscillation_state_topic":"stat/ceilingfan/OSC","direction_command_topic":"cmnd/ceilingfan/Direction","qos":1}`)
	id := "fan.ceiling_fan"
	tr.inject("tele/ceilingfan/LWT", "Online")
	tr.inject("stat/ceilingfan/POWER", "ON")
	tr.inject("stat/ceilingfan/RESULT", `{"FanSpeed":2}`)
	tr.inject("stat/ceilingfan/OSC", "oscillate_off")
	s := state(t, c, id)
	if s.State != "on" {
		t.Fatalf("%+v", s)
	}
	for k, want := range map[string]any{
		"percentage":         66,
		"percentage_step":    100.0 / 3,
		"oscillating":        false,
		"direction":          nil,
		"preset_mode":        nil,
		"preset_modes":       []string{"breeze", "sleep"},
		"supported_features": 1 + 2 + 4 + 8 + 16 + 32,
	} {
		hvacAttr(t, c, id, k, want)
	}

	// Speeds round-trip through HA's ranged-value conversion.
	for pct, device := range map[int]string{33: "1", 34: "2", 66: "2", 67: "3", 100: "3"} {
		hvacMustCall(t, c, "fan", "set_percentage", map[string]any{"entity_id": id, "percentage": pct})
		if p := tr.lastPub(t); p.topic != "cmnd/ceilingfan/FanSpeed" || p.payload != device {
			t.Fatalf("%d%% published %+v, want %s", pct, p, device)
		}
	}
	// The state topic is authoritative, so nothing changed yet.
	hvacAttr(t, c, id, "percentage", 66)
	hvacMustCall(t, c, "fan", "increase_speed", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.payload != "3" {
		t.Fatalf("increase from 66%%: %+v", p)
	}
	hvacMustCall(t, c, "fan", "decrease_speed", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.payload != "2" {
		t.Fatalf("decrease from 66%%: %+v", p)
	}
	hvacMustFail(t, c, "fan", "set_percentage", map[string]any{"entity_id": id, "percentage": 101})

	n := tr.pubCount()
	hvacMustCall(t, c, "fan", "turn_on", map[string]any{"entity_id": id, "percentage": 33, "preset_mode": "sleep"})
	got := hvacPubsSince(tr, n)
	if len(got) != 3 || got[0].topic != "cmnd/ceilingfan/POWER" || got[0].payload != "ON" || got[1].topic != "cmnd/ceilingfan/Preset" || got[1].payload != "sleep" || got[2].topic != "cmnd/ceilingfan/FanSpeed" || got[2].payload != "1" {
		t.Fatalf("%+v", got)
	}
	hvacMustFail(t, c, "fan", "turn_on", map[string]any{"entity_id": id, "preset_mode": "storm"})
	hvacMustFail(t, c, "fan", "turn_on", map[string]any{"entity_id": id, "percentage": 150})
	hvacMustCall(t, c, "fan", "turn_off", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "cmnd/ceilingfan/POWER" || p.payload != "OFF" || p.retain {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "on" {
		t.Error("state changed before the device confirmed")
	}
	tr.inject("stat/ceilingfan/POWER", "OFF")
	if state(t, c, id).State != "off" {
		t.Error("state did not follow the device")
	}

	hvacMustCall(t, c, "fan", "oscillate", map[string]any{"entity_id": id, "oscillating": true})
	if p := tr.lastPub(t); p.topic != "cmnd/ceilingfan/Oscillate" || p.payload != "oscillate_on" {
		t.Fatalf("%+v", p)
	}
	tr.inject("stat/ceilingfan/OSC", "oscillate_on")
	hvacAttr(t, c, id, "oscillating", true)

	// No direction state topic: the command is assumed to take effect.
	hvacMustCall(t, c, "fan", "set_direction", map[string]any{"entity_id": id, "direction": "reverse"})
	if p := tr.lastPub(t); p.topic != "cmnd/ceilingfan/Direction" || p.payload != "reverse" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "direction", "reverse")
	hvacMustFail(t, c, "fan", "set_direction", map[string]any{"entity_id": id, "direction": "sideways"})
	hvacMustCall(t, c, "fan", "set_preset_mode", map[string]any{"entity_id": id, "preset_mode": "breeze"})
	hvacAttr(t, c, id, "preset_mode", "breeze")

	// Out of range reports are dropped, the reset payload clears the speed.
	tr.inject("stat/ceilingfan/RESULT", `{"FanSpeed":9}`)
	hvacAttr(t, c, id, "percentage", 66)
	tr.inject("stat/ceilingfan/RESULT", `{"FanSpeed":0}`)
	hvacAttr(t, c, id, "percentage", 0)
	tr.inject("stat/ceilingfan/RESULT", `{"FanSpeed":"None"}`)
	hvacAttr(t, c, id, "percentage", nil)
	tr.inject("tele/ceilingfan/LWT", "Offline")
	if state(t, c, id).State != core.StateUnavailable {
		t.Error("LWT offline ignored")
	}
}

func TestFanOptimisticTemplates(t *testing.T) {
	c, tr, _ := setup(t)
	// Only a command topic: everything is assumed. The speed range is the
	// default 1..100 and a command template wraps the payload.
	tr.inject("homeassistant/fan/desk/config", `{"name":"Desk fan","unique_id":"desk_fan","command_topic":"desk/fan/set","command_template":"{\"power\": \"{{ value }}\"}","payload_on":"on","payload_off":"off","percentage_command_topic":"desk/fan/speed/set","percentage_command_template":"{\"speed\": {{ value }}}"}`)
	id := "fan.desk_fan"
	if s := state(t, c, id); s.State != "off" {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "fan", "turn_on", map[string]any{"entity_id": id, "percentage": 40})
	got := hvacPubsSince(tr, tr.pubCount()-2)
	if len(got) != 2 || got[0].topic != "desk/fan/set" || got[1].topic != "desk/fan/speed/set" {
		t.Fatalf("%+v", got)
	}
	jsonEq(t, got[0].payload, `{"power":"on"}`)
	jsonEq(t, got[1].payload, `{"speed":40}`)
	if s := state(t, c, id); s.State != "on" || s.Attributes["percentage"] != 40 || s.Attributes["percentage_step"] != 1.0 || s.Attributes["supported_features"] != 1+16+32 {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "fan", "turn_off", map[string]any{"entity_id": id})
	if state(t, c, id).State != "off" {
		t.Fatal("optimistic off")
	}
	hvacMustFail(t, c, "fan", "oscillate", map[string]any{"entity_id": id, "oscillating": true})
	hvacMustFail(t, c, "fan", "set_preset_mode", map[string]any{"entity_id": id, "preset_mode": "auto"})
}

func TestHumidifier(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/humidifier/bedroom/config", `{"name":"Bedroom humidifier","unique_id":"bedroom_humidifier","device_class":"humidifier","command_topic":"hum/bedroom/power/set","state_topic":"hum/bedroom/state","state_value_template":"{{ value_json.state }}","payload_on":"on","payload_off":"off","target_humidity_command_topic":"hum/bedroom/target/set","target_humidity_command_template":"{\"humidity\": {{ value }}}","target_humidity_state_topic":"hum/bedroom/state","target_humidity_state_template":"{{ value_json.target }}","current_humidity_topic":"hum/bedroom/state","current_humidity_template":"{{ value_json.humidity }}","action_topic":"hum/bedroom/state","action_template":"{{ value_json.action }}","mode_command_topic":"hum/bedroom/mode/set","mode_state_topic":"hum/bedroom/state","mode_state_template":"{{ value_json.mode }}","modes":["normal","eco","sleep"],"min_humidity":30,"max_humidity":80}`)
	id := "humidifier.bedroom_humidifier"
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("%+v", s)
	}
	tr.inject("hum/bedroom/state", `{"state":"on","target":55,"humidity":42.5,"action":"humidifying","mode":"eco"}`)
	if s := state(t, c, id); s.State != "on" {
		t.Fatalf("%+v", s)
	}
	for k, want := range map[string]any{
		"humidity":           55,
		"current_humidity":   42.5,
		"min_humidity":       30.0,
		"max_humidity":       80.0,
		"mode":               "eco",
		"available_modes":    []string{"normal", "eco", "sleep"},
		"action":             "humidifying",
		"device_class":       "humidifier",
		"supported_features": 1,
	} {
		hvacAttr(t, c, id, k, want)
	}

	hvacMustCall(t, c, "humidifier", "turn_off", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "hum/bedroom/power/set" || p.payload != "off" {
		t.Fatalf("%+v", p)
	}
	hvacMustCall(t, c, "humidifier", "turn_on", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "hum/bedroom/power/set" || p.payload != "on" {
		t.Fatalf("%+v", p)
	}
	hvacMustCall(t, c, "humidifier", "set_humidity", map[string]any{"entity_id": id, "humidity": 60})
	p := tr.lastPub(t)
	if p.topic != "hum/bedroom/target/set" {
		t.Fatalf("%+v", p)
	}
	jsonEq(t, p.payload, `{"humidity":60}`)
	hvacMustFail(t, c, "humidifier", "set_humidity", map[string]any{"entity_id": id, "humidity": 20})
	hvacMustFail(t, c, "humidifier", "set_humidity", map[string]any{"entity_id": id, "humidity": 90})
	hvacMustCall(t, c, "humidifier", "set_mode", map[string]any{"entity_id": id, "mode": "sleep"})
	if p := tr.lastPub(t); p.topic != "hum/bedroom/mode/set" || p.payload != "sleep" {
		t.Fatalf("%+v", p)
	}
	hvacMustFail(t, c, "humidifier", "set_mode", map[string]any{"entity_id": id, "mode": "boost"})
	if state(t, c, id).Attributes["mode"] != "eco" {
		t.Error("mode changed before the device confirmed")
	}

	// 100 is the default reset payload for the target; an unknown mode and
	// an out-of-range target are dropped.
	tr.inject("hum/bedroom/state", `{"state":"off","target":100,"humidity":41,"action":"idle","mode":"turbo"}`)
	s := state(t, c, id)
	if s.State != "off" || s.Attributes["humidity"] != nil || s.Attributes["mode"] != "eco" || s.Attributes["action"] != "idle" {
		t.Fatalf("%+v", s)
	}
	tr.inject("hum/bedroom/state", `{"state":"off","target":85}`)
	if state(t, c, id).Attributes["humidity"] != nil {
		t.Error("out of range target applied")
	}
}

func TestHumidifierOptimisticDehumidifier(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/humidifier/cellar/config", `{"name":"Cellar dehumidifier","unique_id":"cellar_dehumidifier","device_class":"dehumidifier","command_topic":"cellar/power","target_humidity_command_topic":"cellar/target"}`)
	id := "humidifier.cellar_dehumidifier"
	if s := state(t, c, id); s.State != "off" || s.Attributes["device_class"] != "dehumidifier" || s.Attributes["min_humidity"] != 0.0 || s.Attributes["max_humidity"] != 100.0 {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "humidifier", "turn_on", map[string]any{"entity_id": id})
	hvacMustCall(t, c, "humidifier", "set_humidity", map[string]any{"entity_id": id, "humidity": 45})
	if p := tr.lastPub(t); p.topic != "cellar/target" || p.payload != "45" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, id); s.State != "on" || s.Attributes["humidity"] != 45 {
		t.Fatalf("%+v", s)
	}
	hvacMustFail(t, c, "humidifier", "set_mode", map[string]any{"entity_id": id, "mode": "eco"})
}

func TestWaterHeater(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/water_heater/boiler/config", `{"name":"Boiler","unique_id":"boiler_water_heater","modes":["off","eco","electric"],"mode_command_topic":"boiler/mode/set","mode_state_topic":"boiler/state","mode_state_template":"{{ value_json.mode }}","temperature_command_topic":"boiler/temp/set","temperature_state_topic":"boiler/state","temperature_state_template":"{{ value_json.target }}","current_temperature_topic":"boiler/state","current_temperature_template":"{{ value_json.current }}","power_command_topic":"boiler/power/set","min_temp":30,"max_temp":75,"precision":0.5}`)
	id := "water_heater.boiler"
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("%+v", s)
	}
	tr.inject("boiler/state", `{"mode":"eco","target":55,"current":48.26}`)
	if s := state(t, c, id); s.State != "eco" {
		t.Fatalf("%+v", s)
	}
	for k, want := range map[string]any{
		"operation_mode":      "eco",
		"operation_list":      []string{"off", "eco", "electric"},
		"temperature":         55.0,
		"current_temperature": 48.5,
		"min_temp":            30.0,
		"max_temp":            75.0,
		// target temperature, operation mode, on/off
		"supported_features": 1 + 2 + 8,
	} {
		hvacAttr(t, c, id, k, want)
	}
	hvacMustCall(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 60.5})
	if p := tr.lastPub(t); p.topic != "boiler/temp/set" || p.payload != "60.5" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "temperature", 55.0)
	n := tr.pubCount()
	hvacMustCall(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 65, "operation_mode": "electric"})
	got := hvacPubsSince(tr, n)
	if len(got) != 2 || got[0].topic != "boiler/mode/set" || got[0].payload != "electric" || got[1].topic != "boiler/temp/set" || got[1].payload != "65.0" {
		t.Fatalf("%+v", got)
	}
	hvacMustFail(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 80})
	hvacMustFail(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 29})
	hvacMustFail(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 50, "operation_mode": "gas"})
	hvacMustFail(t, c, "water_heater", "set_operation_mode", map[string]any{"entity_id": id, "operation_mode": "gas"})
	hvacMustCall(t, c, "water_heater", "set_operation_mode", map[string]any{"entity_id": id, "operation_mode": "off"})
	if p := tr.lastPub(t); p.topic != "boiler/mode/set" || p.payload != "off" {
		t.Fatalf("%+v", p)
	}
	hvacMustCall(t, c, "water_heater", "turn_on", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "boiler/power/set" || p.payload != "ON" {
		t.Fatalf("%+v", p)
	}
	hvacMustCall(t, c, "water_heater", "turn_off", map[string]any{"entity_id": id})
	if p := tr.lastPub(t); p.topic != "boiler/power/set" || p.payload != "OFF" {
		t.Fatalf("%+v", p)
	}
	hvacMustFail(t, c, "water_heater", "set_away_mode", map[string]any{"entity_id": id, "away_mode": true})
}

func TestWaterHeaterOptimisticDefaults(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/water_heater/tank/config", `{"name":"Tank","unique_id":"tank_water_heater","mode_command_topic":"tank/mode","temperature_command_topic":"tank/temp","temperature_command_template":"{{ value | int }}","initial":50}`)
	id := "water_heater.tank"
	s := state(t, c, id)
	if s.State != core.StateUnknown || s.Attributes["min_temp"] != 43.3 || s.Attributes["max_temp"] != 60.0 || s.Attributes["temperature"] != 50.0 || s.Attributes["supported_features"] != 3 {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(s.Attributes["operation_list"], []string{"off", "eco", "electric", "gas", "heat_pump", "high_demand", "performance"}) {
		t.Fatalf("%+v", s.Attributes["operation_list"])
	}
	hvacMustCall(t, c, "water_heater", "set_operation_mode", map[string]any{"entity_id": id, "operation_mode": "heat_pump"})
	if s := state(t, c, id); s.State != "heat_pump" {
		t.Fatalf("%+v", s)
	}
	hvacMustCall(t, c, "water_heater", "set_temperature", map[string]any{"entity_id": id, "temperature": 55.5})
	if p := tr.lastPub(t); p.topic != "tank/temp" || p.payload != "55" {
		t.Fatalf("%+v", p)
	}
	hvacAttr(t, c, id, "temperature", 55.5)
	hvacMustFail(t, c, "water_heater", "turn_on", map[string]any{"entity_id": id})
}

func TestHVACRejectsBadConfig(t *testing.T) {
	c, tr, _ := setup(t)
	for name, payload := range map[string]string{
		"homeassistant/climate/a/config":      `{"name":"A","unique_id":"a","modes":["off","turbo"],"mode_command_topic":"a/mode"}`,
		"homeassistant/climate/b/config":      `{"name":"B","unique_id":"b","min_temp":30,"max_temp":10}`,
		"homeassistant/climate/c/config":      `{"name":"C","unique_id":"c","preset_mode_command_topic":"c/preset"}`,
		"homeassistant/climate/d/config":      `{"name":"D","unique_id":"d","precision":0.3}`,
		"homeassistant/fan/e/config":          `{"name":"E","unique_id":"e"}`,
		"homeassistant/fan/f/config":          `{"name":"F","unique_id":"f","command_topic":"f/set","speed_range_min":5,"speed_range_max":3}`,
		"homeassistant/fan/g/config":          `{"name":"G","unique_id":"g","command_topic":"g/set","preset_mode_command_topic":"g/preset"}`,
		"homeassistant/humidifier/h/config":   `{"name":"H","unique_id":"h","command_topic":"h/set"}`,
		"homeassistant/humidifier/i/config":   `{"name":"I","unique_id":"i","command_topic":"i/set","target_humidity_command_topic":"i/target","mode_command_topic":"i/mode"}`,
		"homeassistant/humidifier/j/config":   `{"name":"J","unique_id":"j","command_topic":"j/set","target_humidity_command_topic":"j/target","device_class":"fan"}`,
		"homeassistant/water_heater/k/config": `{"name":"K","unique_id":"k","modes":["off","turbo"]}`,
		"homeassistant/water_heater/l/config": `{"name":"L","unique_id":"l","temperature_unit":"K"}`,
	} {
		tr.inject(name, payload)
	}
	if got := ids(c); len(got) != 0 {
		t.Fatalf("invalid configs created entities: %v", got)
	}
}
