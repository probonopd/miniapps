/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"log/slog"
	"testing"

	"github.com/probonopd/miniha/internal/core"
)

func setupTasmota(t *testing.T) (*core.Core, *fakeTransport) {
	t.Helper()
	c := core.New()
	tr := &fakeTransport{}
	d := NewDiscovery(c, tr, Options{Prefix: "homeassistant", Tasmota: true}, slog.Default())
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	return c, tr
}

// Payload captured from a real LB01-15W-E27 bulb on Tasmota 9.1.0.
const bulbConfig = `{"ip":"192.0.2.59","dn":"Bulb","fn":["Bulb","Tasmota2",null,null,null,null,null,null],"hn":"bulb","mac":"112233445566","md":"LB01-15W-E27-TAS","sw":"9.1.0","t":"tasmota_445566","ft":"%prefix%/%topic%/","tp":["cmnd","stat","tele"],"rl":[2,2,0,0,0,0,0,0],"swc":[-1,-1,-1,-1,-1,-1,-1,-1],"btn":[0,0,0,0],"so":{"4":0,"11":0,"13":0,"17":0,"20":0,"30":0,"68":0,"73":0,"82":0,"114":0},"lk":0,"lt_st":5,"sho":[0,0,0,0],"ver":1}`

func TestTasmotaSplitRGBCWBulb(t *testing.T) {
	c, tr := setupTasmota(t)
	tr.inject("tasmota/discovery/112233445566/config", bulbConfig)
	// lk 0: RGB on POWER1 and white on POWER2 are separate lights.
	rgb, white := "light.bulb", "light.bulb_tasmota2"
	if len(c.States()) != 2 {
		t.Fatalf("split RGBCW must give two lights: %v", ids(c))
	}
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_445566/STATE" {
		t.Fatalf("state must be requested on discovery: %+v", p)
	}
	if s := state(t, c, rgb); s.State != core.StateUnavailable {
		t.Fatalf("before LWT: %q", s.State)
	}
	n := tr.pubCount()
	tr.inject("tele/tasmota_445566/LWT", "Online")
	if tr.pubCount() <= n || tr.lastPub(t).topic != "cmnd/tasmota_445566/STATE" {
		t.Fatal("state must be requested when the device comes online")
	}
	tr.inject("tele/tasmota_445566/STATE", `{"Time":"2026-11-05T12:00:00","POWER1":"ON","POWER2":"OFF","Dimmer1":50,"Dimmer2":80,"Color":"FF000000","HSBColor":"0,100,50","White":0,"CT":153,"Channel":[100,0,0,0,0]}`)
	s := state(t, c, rgb)
	if s.State != "on" || s.Attributes["brightness"] != 128 || s.Attributes["color_mode"] != "hs" || s.Attributes["friendly_name"] != "Bulb" {
		t.Fatalf("rgb %+v", s.Attributes)
	}
	if modes, _ := s.Attributes["supported_color_modes"].([]string); len(modes) != 1 || modes[0] != "hs" {
		t.Fatalf("rgb modes %v", s.Attributes["supported_color_modes"])
	}
	w := state(t, c, white)
	if w.State != "off" || w.Attributes["friendly_name"] != "Bulb Tasmota2" {
		t.Fatalf("white %+v", w)
	}
	if modes, _ := w.Attributes["supported_color_modes"].([]string); len(modes) != 1 || modes[0] != "color_temp" {
		t.Fatalf("white modes %v", w.Attributes["supported_color_modes"])
	}
	dev := c.Device(c.Entity(rgb).DeviceID)
	if dev.Manufacturer != "Tasmota" || dev.Model != "LB01-15W-E27-TAS" || dev.SWVersion != "9.1.0" || dev.ConfigurationURL != "http://192.0.2.59/" || len(c.Devices()) != 1 {
		t.Fatalf("device %+v", dev)
	}
	tr.inject("stat/tasmota_445566/RESULT", `{"POWER2":"ON","Dimmer2":80,"White":80,"CT":300}`)
	w = state(t, c, white)
	if w.State != "on" || w.Attributes["color_mode"] != "color_temp" || w.Attributes["color_temp"] != 300 || w.Attributes["brightness"] != 204 {
		t.Fatalf("white on %+v", w.Attributes)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": rgb, "brightness_pct": 40}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_445566/Backlog" || p.payload != "POWER1 ON; Dimmer1 40" {
		t.Fatalf("brightness: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": white, "color_temp_kelvin": 4000, "brightness": 255}, core.Context{})
	if p := tr.lastPub(t); p.payload != "POWER2 ON; Dimmer2 100; CT 250" {
		t.Fatalf("ct: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": rgb, "rgb_color": []any{0, 255, 0}}, core.Context{})
	if p := tr.lastPub(t); p.payload != "POWER1 ON; HSBColor1 120; HSBColor2 100" {
		t.Fatalf("color: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": rgb}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_445566/POWER1" || p.payload != "ON" {
		t.Fatalf("plain on: %+v", p)
	}
	c.CallService(context.Background(), "light", "turn_off", map[string]any{"entity_id": white}, core.Context{})
	if p := tr.lastPub(t); p.topic != "cmnd/tasmota_445566/POWER2" || p.payload != "OFF" {
		t.Fatalf("off: %+v", p)
	}
	tr.inject("stat/tasmota_445566/RESULT", `{"POWER1":"OFF"}`)
	if state(t, c, rgb).State != "off" {
		t.Fatal("RESULT not applied")
	}
	tr.inject("tele/tasmota_445566/LWT", "Offline")
	if state(t, c, rgb).State != core.StateUnavailable || state(t, c, white).State != core.StateUnavailable {
		t.Fatal("LWT offline not applied")
	}
	tr.inject("tasmota/discovery/112233445566/config", "")
	if len(c.States()) != 0 || len(c.Devices()) != 0 {
		t.Fatalf("removal: %v", ids(c))
	}
}

func TestTasmotaLinkedRGBCWBulb(t *testing.T) {
	c, tr := setupTasmota(t)
	tr.inject("tasmota/discovery/0011223344/config", `{"ip":"192.0.2.61","dn":"Lamp","fn":["Lamp",null,null,null,null,null,null,null],"mac":"0011223344","md":"X","sw":"13.0.0","t":"lamp","ft":"%prefix%/%topic%/","tp":["cmnd","stat","tele"],"rl":[2,0,0,0,0,0,0,0],"lk":1,"lt_st":5,"so":{},"ver":1}`)
	if len(c.States()) != 1 {
		t.Fatalf("linked RGBCW must give one light: %v", ids(c))
	}
	tr.inject("tele/lamp/LWT", "Online")
	tr.inject("tele/lamp/STATE", `{"POWER":"ON","Dimmer":100,"HSBColor":"30,50,100","White":0,"CT":153}`)
	s := state(t, c, "light.lamp")
	if s.State != "on" || s.Attributes["brightness"] != 255 || s.Attributes["color_mode"] != "hs" {
		t.Fatalf("%+v", s.Attributes)
	}
	if modes, _ := s.Attributes["supported_color_modes"].([]string); len(modes) != 2 {
		t.Fatalf("modes %v", s.Attributes["supported_color_modes"])
	}
	c.CallService(context.Background(), "light", "turn_on", map[string]any{"entity_id": "light.lamp", "brightness": 255}, core.Context{})
	if p := tr.lastPub(t); p.payload != "POWER1 ON; Dimmer 100" {
		t.Fatalf("%+v", p)
	}
}

func TestTasmotaTwoRelays(t *testing.T) {
	c, tr := setupTasmota(t)
	tr.inject("tasmota/discovery/AABBCCDDEEFF/config", `{"ip":"192.0.2.60","dn":"Dual","fn":["Left","Right",null,null,null,null,null,null],"hn":"dual","mac":"AABBCCDDEEFF","md":"Sonoff Dual R2","sw":"12.0.0","t":"tasmota_DDEEFF","ft":"%topic%/%prefix%/","tp":["cmnd","stat","tele"],"rl":[1,1,0,0,0,0,0,0],"lt_st":0,"so":{},"ver":1}`)
	tr.inject("tasmota_DDEEFF/tele/LWT", "Online")
	tr.inject("tasmota_DDEEFF/tele/STATE", `{"POWER1":"ON","POWER2":"OFF"}`)
	if state(t, c, "switch.dual_left").State != "on" || state(t, c, "switch.dual_right").State != "off" {
		t.Fatalf("%v", ids(c))
	}
	c.CallService(context.Background(), "switch", "turn_on", map[string]any{"entity_id": "switch.dual_right"}, core.Context{})
	if p := tr.lastPub(t); p.topic != "tasmota_DDEEFF/cmnd/POWER2" || p.payload != "ON" {
		t.Fatalf("%+v", p)
	}
	tr.inject("tasmota_DDEEFF/stat/RESULT", `{"POWER2":"ON"}`)
	if state(t, c, "switch.dual_right").State != "on" {
		t.Fatal("RESULT not applied")
	}
	// A relay vanishing from the config removes its switch.
	tr.inject("tasmota/discovery/AABBCCDDEEFF/config", `{"dn":"Dual","fn":["Left"],"mac":"AABBCCDDEEFF","t":"tasmota_DDEEFF","ft":"%topic%/%prefix%/","rl":[1,0],"lt_st":0,"ver":1}`)
	if c.GetState("switch.dual_right") != nil || c.GetState("switch.dual_left") == nil {
		t.Fatalf("%v", ids(c))
	}
}
