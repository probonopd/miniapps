/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package netdiscovery

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

func TestHint(t *testing.T) {
	tests := []struct {
		name     string
		service  string
		txt      map[string]string
		kind     string
		model    string
		contains string
	}{
		{"esphome", "_esphomelib._tcp", map[string]string{"platform": "ESP32", "board": "nodemcu-32s"}, KindESPHome, "nodemcu-32s", "broker: 192.0.2.2 and discovery: true"},
		{"esphome project", "_esphomelib._tcp", map[string]string{"project_name": "acme.sensor", "board": "x"}, KindESPHome, "acme.sensor", "mqtt: component"},
		{"esphome platform only", "_esphomelib._tcp", map[string]string{"platform": "ESP8266"}, KindESPHome, "ESP8266", "mqtt: component"},
		{"shelly gen1", "_shelly._tcp", map[string]string{"app": "SHSW-1"}, KindShelly, "SHSW-1", "shellies/<id>/relay/0"},
		{"shelly gen2", "_shelly._tcp", map[string]string{"app": "Plus1", "gen": "2"}, KindShelly, "Plus1", "server 192.0.2.2:1883"},
		{"shelly gen2 without app", "_shelly._tcp", map[string]string{"gen": "3"}, KindShelly, "Gen3", "Gen2+"},
		{"tasmota", "_http._tcp", map[string]string{"devicetype": "Tasmota"}, KindTasmota, "", "MqttHost 192.0.2.2"},
		{"plain http", "_http._tcp", map[string]string{}, KindOther, "", "No MQTT integration known"},
		{"homekit", "_hap._tcp", map[string]string{"md": "Hue Bridge"}, KindHomeKit, "Hue Bridge", "Not supported"},
		{"cast", "_googlecast._tcp", map[string]string{"md": "Chromecast", "fn": "TV"}, KindCast, "Chromecast", "Google Cast"},
		{"home assistant", "_home-assistant._tcp", map[string]string{"version": "2025.1"}, KindHomeAssistant, "", "shared MQTT broker"},
		{"mqtt", "_mqtt._tcp", nil, KindMQTTBroker, "", "mqtt.brokers"},
		{"secure mqtt", "_secure-mqtt._tcp", nil, KindMQTTBroker, "", "mqtt.brokers"},
		{"printer", "_printer._tcp", nil, KindPrinter, "", "Printer; not supported"},
		{"ipp", "_ipp._tcp", map[string]string{"ty": "LaserJet"}, KindPrinter, "LaserJet", "Printer; not supported"},
		{"domain suffix", "_hap._tcp.local.", nil, KindHomeKit, "", "HomeKit"},
		{"unknown", "_foo._tcp", nil, KindOther, "", "No MQTT integration known"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, model, hint := Hint(tc.service, tc.txt, "192.0.2.2")
			if kind != tc.kind || model != tc.model {
				t.Fatalf("got kind %q model %q, want %q %q", kind, model, tc.kind, tc.model)
			}
			if !strings.Contains(hint, tc.contains) {
				t.Fatalf("hint %q lacks %q", hint, tc.contains)
			}
		})
	}
}

func TestHintNoEmDash(t *testing.T) {
	for _, s := range append(DefaultServices, "_other._tcp") {
		_, _, hint := Hint(s, map[string]string{"devicetype": "tasmota"}, "")
		if strings.ContainsRune(hint, rune(0x2014)) {
			t.Fatalf("%s: em dash in hint", s)
		}
	}
}

func TestFromZeroconf(t *testing.T) {
	ze := zeroconf.NewServiceEntry("node1", "_esphomelib._tcp.local.", "local.")
	ze.HostName = "node1.local."
	ze.Port = 6053
	ze.Text = []string{"platform=ESP32", "flag"}
	ze.AddrIPv4 = []net.IP{net.ParseIP("192.0.2.50")}
	ze.AddrIPv6 = []net.IP{net.ParseIP("fe80::1")}
	e := fromZeroconf("_esphomelib._tcp", ze)
	if e.Instance != "node1" || e.Service != "_esphomelib._tcp" || e.Port != 6053 || e.Host != "node1.local." {
		t.Fatalf("unexpected entry %+v", e)
	}
	if len(e.Addrs) != 2 || e.Addrs[0] != "192.0.2.50" || e.Addrs[1] != "fe80::1" {
		t.Fatalf("addrs %v", e.Addrs)
	}
	txt := parseTXT(e.Text)
	if txt["platform"] != "ESP32" {
		t.Fatalf("txt %v", txt)
	}
	if v, ok := txt["flag"]; !ok || v != "" {
		t.Fatalf("flag-only attribute lost: %v", txt)
	}
}

func TestMergeAndRefresh(t *testing.T) {
	b := New(nil, Options{MQTTHost: "10.0.0.1"})
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e := entry{Instance: "node1", Service: "_esphomelib._tcp", Host: "node1.local.", Port: 6053, Text: []string{"board=esp32dev"}, Addrs: []string{"192.0.2.5"}}
	b.merge([]entry{e}, t0)
	e.Port = 6054
	b.merge([]entry{e}, t0.Add(time.Minute))

	devs := b.Devices()
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	d := devs[0]
	if d.Host != "node1.local" || d.Port != 6054 || d.Model != "esp32dev" || d.Kind != KindESPHome {
		t.Fatalf("unexpected device %+v", d)
	}
	if !d.FirstSeen.Equal(t0) || !d.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("times: first %v last %v", d.FirstSeen, d.LastSeen)
	}
	if !strings.Contains(d.Hint, "10.0.0.1") {
		t.Fatalf("hint lacks MQTT host: %q", d.Hint)
	}
}

func TestMergeKeysByServiceAndInstance(t *testing.T) {
	b := New(nil, Options{})
	now := time.Now()
	b.merge([]entry{
		{Instance: "dev", Service: "_hap._tcp"},
		{Instance: "dev", Service: "_googlecast._tcp"},
	}, now)
	if n := len(b.Devices()); n != 2 {
		t.Fatalf("want 2 devices, got %d", n)
	}
}

func TestMergeSkipsPlainHTTP(t *testing.T) {
	b := New(nil, Options{})
	now := time.Now()
	b.merge([]entry{
		{Instance: "router", Service: "_http._tcp"},
		{Instance: "plug", Service: "_http._tcp", Text: []string{"devicetype=tasmota"}},
	}, now)
	devs := b.Devices()
	if len(devs) != 1 || devs[0].Name != "plug" || devs[0].Kind != KindTasmota {
		t.Fatalf("unexpected devices %+v", devs)
	}
}

func TestCastNameFromTXT(t *testing.T) {
	b := New(nil, Options{})
	b.merge([]entry{{Instance: "abc123", Service: "_googlecast._tcp", Text: []string{"fn=Living Room TV", "md=Chromecast"}}}, time.Now())
	devs := b.Devices()
	if len(devs) != 1 || devs[0].Name != "Living Room TV" || devs[0].Model != "Chromecast" {
		t.Fatalf("unexpected devices %+v", devs)
	}
}

func TestExpire(t *testing.T) {
	b := New(nil, Options{Interval: time.Minute})
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b.merge([]entry{{Instance: "old", Service: "_hap._tcp"}}, t0)
	b.merge([]entry{{Instance: "new", Service: "_hap._tcp"}}, t0.Add(2*time.Minute))

	b.expire(t0.Add(3 * time.Minute))
	if n := len(b.Devices()); n != 2 {
		t.Fatalf("exactly 3 intervals must not expire yet, got %d devices", n)
	}
	b.expire(t0.Add(3*time.Minute + time.Second))
	devs := b.Devices()
	if len(devs) != 1 || devs[0].Name != "new" {
		t.Fatalf("want only 'new', got %+v", devs)
	}
}

func TestListShapeAndOrder(t *testing.T) {
	b := New(nil, Options{IsIntegrated: func(d Device) bool { return d.Name == "alpha" }})
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b.merge([]entry{
		{Instance: "zulu", Service: "_hap._tcp", Host: "zulu.local.", Port: 1, Addrs: []string{"10.0.0.2"}},
		{Instance: "alpha", Service: "_esphomelib._tcp", Host: "alpha.local.", Port: 6053, Text: []string{"board=b"}, Addrs: []string{"10.0.0.1"}},
	}, t0)

	list := b.List()
	if len(list) != 2 || list[0]["name"] != "alpha" || list[1]["name"] != "zulu" {
		t.Fatalf("order: %+v", list)
	}
	want := []string{"name", "host", "addrs", "port", "service", "kind", "model", "txt", "hint", "integrated", "first_seen", "last_seen"}
	for _, k := range want {
		if _, ok := list[0][k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if len(list[0]) != len(want) {
		t.Errorf("unexpected keys: %v", list[0])
	}
	if list[0]["integrated"] != true || list[1]["integrated"] != false {
		t.Errorf("integrated flags: %v %v", list[0]["integrated"], list[1]["integrated"])
	}
	if list[0]["first_seen"] != "2026-01-01T12:00:00Z" {
		t.Errorf("first_seen %v", list[0]["first_seen"])
	}
	if _, err := json.Marshal(list); err != nil {
		t.Fatalf("not JSON-encodable: %v", err)
	}
}

func TestListEmptyIsNotNil(t *testing.T) {
	list := New(nil, Options{}).List()
	if list == nil {
		t.Fatal("empty list must encode as [] not null")
	}
	data, _ := json.Marshal(list)
	if string(data) != "[]" {
		t.Fatalf("got %s", data)
	}
}

func TestNewDefaults(t *testing.T) {
	b := New(nil, Options{})
	if b.opts.Interval != DefaultInterval || len(b.opts.Services) != len(DefaultServices) {
		t.Fatalf("defaults not applied: %+v", b.opts)
	}
}

func TestStopWithoutStart(t *testing.T) {
	New(nil, Options{}).Stop()
}
