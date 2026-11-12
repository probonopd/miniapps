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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// fakeTransport stands in for the broker: it routes injected messages to
// matching subscriptions and records what the entities publish.
type fakeTransport struct {
	mu   sync.Mutex
	subs []fakeSub
	pubs []pub
}

type fakeSub struct {
	filter string
	cb     func(Message)
}

type pub struct {
	topic   string
	payload string
	retain  bool
}

func (f *fakeTransport) Subscribe(filter string, cb func(Message)) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := fakeSub{filter, cb}
	f.subs = append(f.subs, s)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.subs {
			if f.subs[i].filter == filter && reflect.ValueOf(f.subs[i].cb).Pointer() == reflect.ValueOf(cb).Pointer() {
				f.subs = append(f.subs[:i], f.subs[i+1:]...)
				return
			}
		}
	}, nil
}

func (f *fakeTransport) Publish(topic string, payload []byte, qos byte, retain bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pubs = append(f.pubs, pub{topic, string(payload), retain})
	return nil
}

func (f *fakeTransport) inject(topic, payload string) {
	f.mu.Lock()
	subs := append([]fakeSub(nil), f.subs...)
	f.mu.Unlock()
	for _, s := range subs {
		if MatchTopic(s.filter, topic) {
			s.cb(Message{Topic: topic, Payload: []byte(payload)})
		}
	}
}

func (f *fakeTransport) lastPub(t *testing.T) pub {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) == 0 {
		t.Fatal("nothing published")
	}
	return f.pubs[len(f.pubs)-1]
}

func (f *fakeTransport) pubCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pubs)
}

func setup(t *testing.T) (*core.Core, *fakeTransport, *Discovery) {
	t.Helper()
	c := core.New()
	tr := &fakeTransport{}
	d := NewDiscovery(c, tr, Options{Prefix: "homeassistant", Birth: &Will{Topic: "homeassistant/status", Payload: "online"}}, slog.Default())
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	return c, tr, d
}

func state(t *testing.T, c *core.Core, id string) *core.State {
	t.Helper()
	s := c.GetState(id)
	if s == nil {
		t.Fatalf("no state for %s; have %v", id, ids(c))
	}
	return s
}

func ids(c *core.Core) []string {
	var out []string
	for _, s := range c.States() {
		out = append(out, s.EntityID)
	}
	return out
}

func jsonEq(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("bad json %q: %v", got, err)
	}
	json.Unmarshal([]byte(want), &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("json\n got %s\nwant %s", got, want)
	}
}

const z2mAvail = `"availability":[{"topic":"zigbee2mqtt/bridge/state","value_template":"{{ value_json.state }}"}]`

func TestBirthPublished(t *testing.T) {
	_, tr, _ := setup(t)
	p := tr.lastPub(t)
	if p.topic != "homeassistant/status" || p.payload != "online" {
		t.Fatalf("birth not published: %+v", p)
	}
}

func TestZigbee2MQTTSensor(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/sensor/0x00124b001234abcd/temperature/config", `{`+z2mAvail+`,"device":{"identifiers":["zigbee2mqtt_0x00124b001234abcd"],"manufacturer":"Xiaomi","model":"Temperature and humidity sensor (WSDCGQ11LM)","name":"Living room sensor","sw_version":"3000-0001"},"device_class":"temperature","enabled_by_default":true,"name":"Temperature","object_id":"living_room_sensor_temperature","origin":{"name":"Zigbee2MQTT","sw":"1.35.1","url":"https://www.zigbee2mqtt.io"},"state_class":"measurement","state_topic":"zigbee2mqtt/Living room sensor","unique_id":"0x00124b001234abcd_temperature_zigbee2mqtt","unit_of_measurement":"°C","value_template":"{{ value_json.temperature }}"}`)
	id := "sensor.living_room_sensor_temperature"
	if s := state(t, c, id); s.State != core.StateUnavailable {
		t.Fatalf("before availability: %q", s.State)
	}
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	if s := state(t, c, id); s.State != core.StateUnknown {
		t.Fatalf("after online: %q", s.State)
	}
	tr.inject("zigbee2mqtt/Living room sensor", `{"temperature":21.5,"battery":87,"linkquality":120}`)
	s := state(t, c, id)
	if s.State != "21.5" {
		t.Fatalf("state %q", s.State)
	}
	want := map[string]any{"unit_of_measurement": "°C", "device_class": "temperature", "state_class": "measurement", "friendly_name": "Living room sensor Temperature"}
	for k, v := range want {
		if s.Attributes[k] != v {
			t.Errorf("attr %s = %v, want %v", k, s.Attributes[k], v)
		}
	}
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"offline"}`)
	if s := state(t, c, id); s.State != core.StateUnavailable {
		t.Fatalf("after offline: %q", s.State)
	}
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	if s := state(t, c, id); s.State != "21.5" {
		t.Fatalf("state must survive an availability gap: %q", s.State)
	}
	e := c.Entity(id)
	if e == nil || e.UniqueID != "0x00124b001234abcd_temperature_zigbee2mqtt" || e.Platform != "mqtt" {
		t.Fatalf("registry entry: %+v", e)
	}
	dev := c.Device(e.DeviceID)
	if dev == nil || dev.Manufacturer != "Xiaomi" || dev.Name != "Living room sensor" || dev.Identifiers[0] != [2]string{"mqtt", "zigbee2mqtt_0x00124b001234abcd"} {
		t.Fatalf("device: %+v", dev)
	}
	// Removal by empty retained config drops entity and orphaned device.
	tr.inject("homeassistant/sensor/0x00124b001234abcd/temperature/config", "")
	if c.GetState(id) != nil || c.Entity(id) != nil || c.Device(dev.ID) != nil {
		t.Fatal("entity or device not removed")
	}
}

func TestZigbee2MQTTSwitch(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/switch/0xdef/switch/config", `{`+z2mAvail+`,"command_topic":"zigbee2mqtt/Plug/set","device":{"identifiers":["zigbee2mqtt_0xdef"],"name":"Plug","manufacturer":"IKEA","model":"TRADFRI control outlet"},"name":null,"object_id":"plug","payload_off":"OFF","payload_on":"ON","state_topic":"zigbee2mqtt/Plug","unique_id":"0xdef_switch_zigbee2mqtt","value_template":"{{ value_json.state }}"}`)
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	id := "switch.plug"
	tr.inject("zigbee2mqtt/Plug", `{"state":"ON","linkquality":50}`)
	s := state(t, c, id)
	if s.State != "on" || s.Attributes["friendly_name"] != "Plug" {
		t.Fatalf("%+v", s)
	}
	n := tr.pubCount()
	if _, err := c.CallService(context.Background(), "switch", "turn_off", map[string]any{"entity_id": id}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	p := tr.lastPub(t)
	if tr.pubCount() != n+1 || p.topic != "zigbee2mqtt/Plug/set" || p.payload != "OFF" {
		t.Fatalf("published %+v", p)
	}
	// Not optimistic: state waits for the device.
	if state(t, c, id).State != "on" {
		t.Fatal("state changed before the device reported")
	}
	tr.inject("zigbee2mqtt/Plug", `{"state":"OFF"}`)
	if state(t, c, id).State != "off" {
		t.Fatal("state not updated")
	}
}

func TestTasmotaAbbreviations(t *testing.T) {
	c, tr, d := setup(t)
	tr.inject("homeassistant/switch/ABC123_RL_1/config", `{"name":"Tasmota","stat_t":"tele/tasmota_ABC123/STATE","avty_t":"tele/tasmota_ABC123/LWT","pl_avail":"Online","pl_not_avail":"Offline","cmd_t":"cmnd/tasmota_ABC123/POWER","val_tpl":"{{value_json.POWER}}","pl_off":"OFF","pl_on":"ON","uniq_id":"ABC123_RL_1","dev":{"ids":["ABC123"]}}`)
	tr.inject("homeassistant/sensor/ABC123_status/config", `{"name":"Tasmota status","stat_t":"tele/tasmota_ABC123/HASS_STATE","avty_t":"tele/tasmota_ABC123/LWT","pl_avail":"Online","pl_not_avail":"Offline","json_attr_t":"tele/tasmota_ABC123/HASS_STATE","unit_of_meas":"%","val_tpl":"{{value_json['RSSI']}}","ic":"mdi:information-outline","uniq_id":"ABC123_status","dev":{"ids":["ABC123"],"name":"Tasmota","mdl":"Sonoff Basic","sw":"12.5.0","mf":"Tasmota"}}`)
	// Discovery asks for STATE and queries TelePeriod; the reply makes
	// us resend the same value, which forces STATE and SENSOR telemetry.
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_ABC123/TelePeriod" || p.payload != "" {
		t.Fatalf("TelePeriod query on discovery: %+v", p)
	}
	tr.inject("stat/tasmota_ABC123/RESULT", `{"TelePeriod":300}`)
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_ABC123/TelePeriod" || p.payload != "300" {
		t.Fatalf("TelePeriod resend: %+v", p)
	}
	// Within the rate limit (retained LWT right after discovery) no second
	// request goes out; later, coming online asks again.
	n := tr.pubCount()
	tr.inject("tele/tasmota_ABC123/LWT", "Online")
	if tr.pubCount() != n {
		t.Fatalf("state request repeated within the rate limit")
	}
	tr.inject("tele/tasmota_ABC123/LWT", "Offline")
	d.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	tr.inject("tele/tasmota_ABC123/LWT", "Online")
	if tr.pubCount() != n+2 || tr.lastPub(t).topic != "cmnd/tasmota_ABC123/TelePeriod" {
		t.Fatalf("one request pair per device when it comes online: %d", tr.pubCount()-n)
	}
	tr.inject("tele/tasmota_ABC123/STATE", `{"Time":"2026-01-01T00:00:00","POWER":"ON","Wifi":{"RSSI":80}}`)
	sw := state(t, c, "switch.tasmota")
	if sw.State != "on" {
		t.Fatalf("switch %+v", sw)
	}
	tr.inject("tele/tasmota_ABC123/HASS_STATE", `{"Version":"12.5.0","RSSI":80,"Module":"Sonoff Basic"}`)
	st := state(t, c, "sensor.tasmota_status")
	if st.State != "80" || st.Attributes["Module"] != "Sonoff Basic" || st.Attributes["icon"] != "mdi:information-outline" || st.Attributes["unit_of_measurement"] != "%" {
		t.Fatalf("status %+v", st)
	}
	// Both entities share one device, named by the second config.
	if len(c.Devices()) != 1 || c.Devices()[0].Name != "Tasmota" || c.Devices()[0].Model != "Sonoff Basic" {
		t.Fatalf("devices %+v", c.Devices()[0])
	}
	c.CallService(context.Background(), "switch", "turn_on", map[string]any{"entity_id": "switch.tasmota"}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_ABC123/POWER" || p.payload != "ON" {
		t.Fatalf("published %+v", p)
	}
	tr.inject("tele/tasmota_ABC123/LWT", "Offline")
	if state(t, c, "switch.tasmota").State != core.StateUnavailable || state(t, c, "sensor.tasmota_status").State != core.StateUnavailable {
		t.Fatal("LWT offline not applied")
	}
}

func TestBinarySensorBooleanPayloads(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/binary_sensor/0xaaa/contact/config", `{"device":{"identifiers":["zigbee2mqtt_0xaaa"],"name":"Door"},"device_class":"door","name":"Contact","object_id":"door_contact","payload_off":true,"payload_on":false,"state_topic":"zigbee2mqtt/Door","unique_id":"0xaaa_contact_zigbee2mqtt","value_template":"{{ value_json.contact }}"}`)
	id := "binary_sensor.door_contact"
	if state(t, c, id).State != core.StateUnknown {
		t.Fatal("no availability configured: must start unknown")
	}
	tr.inject("zigbee2mqtt/Door", `{"contact":true}`)
	if s := state(t, c, id); s.State != "off" || s.Attributes["device_class"] != "door" {
		t.Fatalf("%+v", s)
	}
	tr.inject("zigbee2mqtt/Door", `{"contact":false}`)
	if state(t, c, id).State != "on" {
		t.Fatal("not on")
	}
	// Payloads that match neither are ignored.
	tr.inject("zigbee2mqtt/Door", `{"contact":"garbage"}`)
	if state(t, c, id).State != "on" {
		t.Fatal("garbage changed state")
	}
}

func TestZigbee2MQTTJSONLight(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/light/0xabc/light/config", `{`+z2mAvail+`,"brightness":true,"brightness_scale":254,"command_topic":"zigbee2mqtt/Bulb/set","device":{"identifiers":["zigbee2mqtt_0xabc"],"manufacturer":"IKEA","model":"TRADFRI bulb E27 WS 806lm","name":"Bulb"},"effect":true,"effect_list":["blink","breathe","okay"],"max_mireds":454,"min_mireds":250,"name":null,"object_id":"bulb","schema":"json","state_topic":"zigbee2mqtt/Bulb","supported_color_modes":["color_temp","xy"],"unique_id":"0xabc_light_zigbee2mqtt"}`)
	tr.inject("zigbee2mqtt/bridge/state", `{"state":"online"}`)
	id := "light.bulb"
	tr.inject("zigbee2mqtt/Bulb", `{"brightness":254,"color_mode":"color_temp","color_temp":370,"linkquality":100,"state":"ON"}`)
	s := state(t, c, id)
	if s.State != "on" {
		t.Fatalf("%+v", s)
	}
	want := map[string]any{"brightness": 255, "color_mode": "color_temp", "color_temp": 370, "color_temp_kelvin": 2702, "min_mireds": 250, "max_mireds": 454, "min_color_temp_kelvin": 2202, "max_color_temp_kelvin": 4000, "friendly_name": "Bulb"}
	for k, v := range want {
		if !reflect.DeepEqual(s.Attributes[k], v) {
			t.Errorf("attr %s = %#v, want %#v", k, s.Attributes[k], v)
		}
	}
	if !reflect.DeepEqual(s.Attributes["supported_color_modes"], []string{"color_temp", "xy"}) || !reflect.DeepEqual(s.Attributes["effect_list"], []string{"blink", "breathe", "okay"}) {
		t.Errorf("lists: %#v %#v", s.Attributes["supported_color_modes"], s.Attributes["effect_list"])
	}
	tr.inject("zigbee2mqtt/Bulb", `{"state":"ON","color_mode":"xy","color":{"x":0.7,"y":0.3}}`)
	s = state(t, c, id)
	if !reflect.DeepEqual(s.Attributes["xy_color"], []float64{0.7, 0.3}) || s.Attributes["color_mode"] != "xy" || s.Attributes["brightness"] != 255 {
		t.Errorf("xy state: %#v", s.Attributes)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "brightness": 100, "transition": 2}, core.Context{})
	jsonEq(t, tr.lastPub(t).payload, `{"state":"ON","brightness":100,"transition":2}`)
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "color_temp_kelvin": 4000}, core.Context{})
	jsonEq(t, tr.lastPub(t).payload, `{"state":"ON","color_temp":250}`)
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "rgb_color": []any{255, 0, 0}}, core.Context{})
	jsonEq(t, tr.lastPub(t).payload, `{"state":"ON","color":{"x":0.701,"y":0.299}}`)
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "effect": "blink"}, core.Context{})
	jsonEq(t, tr.lastPub(t).payload, `{"state":"ON","effect":"blink"}`)
	c.CallService(context.Background(), "light", "turn_off", map[string]any{"entity_id": id}, core.Context{})
	if p := tr.lastPub(t); p.topic != "zigbee2mqtt/Bulb/set" {
		t.Fatalf("%+v", p)
	}
	jsonEq(t, tr.lastPub(t).payload, `{"state":"OFF"}`)
	tr.inject("zigbee2mqtt/Bulb", `{"state":"OFF"}`)
	s = state(t, c, id)
	if s.State != "off" || s.Attributes["brightness"] != nil || s.Attributes["color_mode"] != nil {
		t.Errorf("off state must null the color attributes: %#v", s.Attributes)
	}
}

func TestTasmotaDefaultSchemaLight(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/light/X_LI_1/config", `{"name":"Tasmota","cmd_t":"cmnd/tasmota_X/POWER","stat_t":"tele/tasmota_X/STATE","stat_val_tpl":"{{value_json.POWER}}","pl_off":"OFF","pl_on":"ON","bri_cmd_t":"cmnd/tasmota_X/Dimmer","bri_stat_t":"tele/tasmota_X/STATE","bri_scl":100,"on_cmd_type":"brightness","bri_val_tpl":"{{value_json.Dimmer}}","clr_temp_cmd_t":"cmnd/tasmota_X/CT","clr_temp_stat_t":"tele/tasmota_X/STATE","clr_temp_val_tpl":"{{value_json.CT}}","uniq_id":"X_LI_1","dev":{"ids":["X"],"name":"Tasmota"}}`)
	id := "light.tasmota"
	tr.inject("tele/tasmota_X/STATE", `{"POWER":"ON","Dimmer":50,"CT":300}`)
	s := state(t, c, id)
	if s.State != "on" || s.Attributes["brightness"] != 128 || s.Attributes["color_temp"] != 300 || s.Attributes["color_mode"] != "color_temp" {
		t.Fatalf("%+v", s.Attributes)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "brightness": 255}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_X/Dimmer" || p.payload != "100" {
		t.Fatalf("on_command_type brightness: %+v", p)
	}
	// HA resends the saved brightness instead of a power command.
	n := tr.pubCount()
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id}, core.Context{})
	if p := tr.lastPub(t); tr.pubCount() != n+1 || p.topic != "cmnd/tasmota_X/Dimmer" || p.payload != "50" {
		t.Fatalf("plain turn_on: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": id, "color_temp": 400}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_X/CT" || p.payload != "400" {
		t.Fatalf("color temp: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_off", map[string]any{"entity_id": id}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_X/POWER" || p.payload != "OFF" {
		t.Fatalf("turn_off: %+v", p)
	}
}

func TestTopicPrefixAndOptimisticSwitch(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/switch/relay/config", `{"~":"home/relay","cmd_t":"~/set","name":"Relay","uniq_id":"relay1"}`)
	id := "switch.relay"
	if state(t, c, id).State != core.StateUnknown {
		t.Fatal("initial")
	}
	c.CallService(context.Background(), "switch", "turn_on", map[string]any{"entity_id": id}, core.Context{})
	if p := tr.lastPub(t); p.topic != "home/relay/set" || p.payload != "ON" {
		t.Fatalf("%+v", p)
	}
	if state(t, c, id).State != "on" {
		t.Fatal("no state_topic means optimistic")
	}
}

func TestDeviceBasedDiscovery(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/device/sensorhub/config", `{"dev":{"ids":"hub1","name":"Hub"},"o":{"name":"test"},"stat_t":"hub/state","cmps":{"temp":{"p":"sensor","name":"Temperature","val_tpl":"{{ value_json.t }}","unit_of_meas":"°C","uniq_id":"hub1_t"},"relay":{"p":"switch","name":"Relay","cmd_t":"hub/relay/set","val_tpl":"{{ value_json.r }}","uniq_id":"hub1_r"}}}`)
	tr.inject("hub/state", `{"t":19,"r":"ON"}`)
	if state(t, c, "sensor.hub_temperature").State != "19" || state(t, c, "switch.hub_relay").State != "on" {
		t.Fatalf("%v", ids(c))
	}
	// An empty component removes just that component.
	tr.inject("homeassistant/device/sensorhub/config", `{"dev":{"ids":"hub1","name":"Hub"},"o":{"name":"test"},"stat_t":"hub/state","cmps":{"temp":{"p":"sensor","name":"Temperature","val_tpl":"{{ value_json.t }}","unit_of_meas":"°C","uniq_id":"hub1_t"},"relay":{}}}`)
	if c.GetState("switch.hub_relay") != nil || c.GetState("sensor.hub_temperature") == nil {
		t.Fatalf("component removal: %v", ids(c))
	}
	tr.inject("homeassistant/device/sensorhub/config", ``)
	if len(c.States()) != 0 || len(c.Devices()) != 0 {
		t.Fatalf("device removal: %v", ids(c))
	}
}

func TestBadConfigsAreIgnored(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/nosuch/x/config", `{"name":"x","uniq_id":"1"}`)
	tr.inject("homeassistant/sensor/x/config", `not json`)
	tr.inject("homeassistant/sensor/x/config", `{"name":"no state topic","uniq_id":"2"}`)
	tr.inject("homeassistant/switch/x/config", `{"name":"no command topic","uniq_id":"3"}`)
	tr.inject("homeassistant/sensor/x/config", `{"name":"bad template","stat_t":"a","val_tpl":"{{ ","uniq_id":"4"}`)
	if len(c.States()) != 0 {
		t.Fatalf("created %v", ids(c))
	}
}

func TestReconfigureKeepsEntityAndState(t *testing.T) {
	c, tr, _ := setup(t)
	cfg := `{"name":"T","stat_t":"a/state","uniq_id":"u1","unit_of_meas":"°C"}`
	tr.inject("homeassistant/sensor/a/config", cfg)
	tr.inject("a/state", "5")
	tr.inject("homeassistant/sensor/a/config", strings.Replace(cfg, "°C", "K", 1))
	s := state(t, c, "sensor.t")
	if s.State != "5" || s.Attributes["unit_of_measurement"] != "K" {
		t.Fatalf("%+v", s)
	}
	if len(c.States()) != 1 {
		t.Fatalf("duplicate entity: %v", ids(c))
	}
}

func TestMatchTopic(t *testing.T) {
	cases := []struct {
		f, t string
		ok   bool
	}{
		{"a/+/c", "a/b/c", true}, {"a/+/c", "a/b/d", false}, {"a/#", "a/b/c", true}, {"a/#", "a", true},
		{"+/+/config", "homeassistant/sensor/config", true}, {"a/b", "a/b/c", false}, {"#", "x/y", true},
		{"a/+", "a/b/c", false},
	}
	for _, c := range cases {
		if MatchTopic(c.f, c.t) != c.ok {
			t.Errorf("MatchTopic(%q, %q) != %v", c.f, c.t, c.ok)
		}
	}
}

// Captured from a Sonoff TH on Tasmota 8.1.0 with SetOption19.
func TestTasmota81SensorDiscovery(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/sensor/112233_AM2301_Temperature/config", `{"name":"Sonoff AM2301 Temperature","stat_t":"~SENSOR","avty_t":"~LWT","frc_upd":true,"pl_avail":"Online","pl_not_avail":"Offline","uniq_id":"112233_AM2301_Temperature","device":{"identifiers":["112233"],"connections":[["mac","5C:CF:7F:11:22:33"]]},"~":"tasmota/tele/","unit_of_meas":"°C","val_tpl":"{{value_json['AM2301'].Temperature}}","dev_cla":"temperature"}`)
	tr.inject("tasmota/tele/LWT", "Online")
	tr.inject("tasmota/tele/SENSOR", `{"Time":"2026-11-05T13:36:32","AM2301":{"Temperature":24.0,"Humidity":31.6},"TempUnit":"C"}`)
	s := state(t, c, "sensor.sonoff_am2301_temperature")
	if s.State != "24.0" {
		t.Fatalf("%+v", s)
	}
}

// Two brokers announcing the same unique_id must stay two entities.
func TestBrokerNamespaces(t *testing.T) {
	c := core.New()
	trA, trB := &fakeTransport{}, &fakeTransport{}
	dA := NewDiscovery(c, trA, Options{}, slog.Default())
	dB := NewDiscovery(c, trB, Options{Platform: "mqtt/public"}, slog.Default())
	for _, d := range []*Discovery{dA, dB} {
		if err := d.Start(); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"name":"Temp","stat_t":"t/state","uniq_id":"same","unit_of_meas":"C","dev":{"ids":["dev1"],"name":"Dev"}}`
	trA.inject("homeassistant/sensor/t/config", cfg)
	trB.inject("homeassistant/sensor/t/config", cfg)
	if len(c.States()) != 2 || len(c.Devices()) != 2 {
		t.Fatalf("entities %v devices %d", ids(c), len(c.Devices()))
	}
	trA.inject("t/state", "1")
	trB.inject("t/state", "2")
	if state(t, c, "sensor.dev_temp").State != "1" || state(t, c, "sensor.dev_temp_2").State != "2" {
		t.Fatalf("%v", ids(c))
	}
	if e := c.Entity("sensor.dev_temp_2"); e.Platform != "mqtt/public" {
		t.Fatalf("platform %q", e.Platform)
	}
	// The same payload on one broker again is the same entity.
	trB.inject("homeassistant/sensor/t/config", cfg)
	if len(c.States()) != 2 {
		t.Fatalf("%v", ids(c))
	}
}
