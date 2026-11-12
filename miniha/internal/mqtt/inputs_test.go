/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"strings"
	"testing"

	"github.com/probonopd/miniha/internal/core"
)

// Payloads captured from a "minitv" Android TV app on EMQX Cloud.
const tvAvail = `"availability":[{"topic":"tv/echoshow/status","value_template":"{{ value_json.state }}","payload_available":"online","payload_not_available":"offline"}]`
const tvDev = `"device":{"identifiers":["tv_echoshow"],"name":"Echo Show","manufacturer":"minitv","model":"TV app"}`

func TestNumberSelectButtonText(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/number/tv_echoshow/footer_seconds/config", `{"name":"Footer seconds","entity_category":"config","state_topic":"tv/echoshow/config","value_template":"{{ value_json.footer_seconds }}","command_topic":"tv/echoshow/set/footer_seconds","min":1,"max":60,"step":1,"unit_of_measurement":"s","unique_id":"tv_echoshow_footer_seconds","has_entity_name":true,`+tvDev+`,`+tvAvail+`}`)
	tr.inject("homeassistant/select/tv_echoshow/channel/config", `{"name":"Channel","icon":"mdi:television","state_topic":"tv/echoshow/state","value_template":"{{ value_json.channel }}","command_topic":"tv/echoshow/set/channel","options":["ARD","ZDF","SWR BW"],"unique_id":"tv_echoshow_channel","has_entity_name":true,`+tvDev+`,`+tvAvail+`}`)
	tr.inject("homeassistant/button/tv_echoshow/reload_playlist/config", `{"name":"Reload playlist","entity_category":"config","command_topic":"tv/echoshow/cmd/reload-playlist","payload_press":"1","unique_id":"tv_echoshow_reload_playlist","has_entity_name":true,`+tvDev+`,`+tvAvail+`}`)
	tr.inject("homeassistant/text/tv_echoshow/jitsi_room/config", `{"name":"Video call room (green key)","entity_category":"config","state_topic":"tv/echoshow/config","value_template":"{{ value_json.jitsi_room }}","command_topic":"tv/echoshow/set/jitsi_room","max":64,"unique_id":"tv_echoshow_jitsi_room","has_entity_name":true,`+tvDev+`,`+tvAvail+`}`)
	tr.inject("tv/echoshow/status", `{"state":"online"}`)
	tr.inject("tv/echoshow/config", `{"footer_seconds":5,"jitsi_room":"family"}`)
	tr.inject("tv/echoshow/state", `{"channel":"ARD"}`)
	num, sel, btn, txt := "number.echo_show_footer_seconds", "select.echo_show_channel", "button.echo_show_reload_playlist", "text.echo_show_video_call_room_green_key"
	if s := state(t, c, num); s.State != "5" || s.Attributes["min"] != 1.0 || s.Attributes["max"] != 60.0 || s.Attributes["unit_of_measurement"] != "s" {
		t.Fatalf("number %+v", s)
	}
	if s := state(t, c, sel); s.State != "ARD" || len(s.Attributes["options"].([]string)) != 3 {
		t.Fatalf("select %+v", s)
	}
	if s := state(t, c, btn); s.State != core.StateUnknown {
		t.Fatalf("button %+v", s)
	}
	if s := state(t, c, txt); s.State != "family" || s.Attributes["max"] != 64 {
		t.Fatalf("text %+v", s)
	}
	if _, err := c.CallService(context.Background(), "number", "set_value", map[string]any{"entity_id": num, "value": 10.0}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "tv/echoshow/set/footer_seconds" || p.payload != "10" {
		t.Fatalf("%+v", p)
	}
	if _, err := c.CallService(context.Background(), "number", "set_value", map[string]any{"entity_id": num, "value": 61}, core.Context{}); err == nil {
		t.Fatal("out of range accepted")
	}
	// Not optimistic: state follows the device.
	if state(t, c, num).State != "5" {
		t.Fatal("state changed before the device reported")
	}
	tr.inject("tv/echoshow/config", `{"footer_seconds":10.5}`)
	if state(t, c, num).State != "10.5" {
		t.Fatal("number not updated")
	}
	tr.inject("tv/echoshow/config", `{"footer_seconds":999}`)
	if state(t, c, num).State != "10.5" {
		t.Fatal("out-of-range report applied")
	}
	if _, err := c.CallService(context.Background(), "select", "select_option", map[string]any{"entity_id": sel, "option": "ZDF"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "tv/echoshow/set/channel" || p.payload != "ZDF" {
		t.Fatalf("%+v", p)
	}
	if _, err := c.CallService(context.Background(), "select", "select_option", map[string]any{"entity_id": sel, "option": "RTL"}, core.Context{}); err == nil {
		t.Fatal("invalid option accepted")
	}
	tr.inject("tv/echoshow/state", `{"channel":"RTL"}`)
	if state(t, c, sel).State != "ARD" {
		t.Fatal("invalid reported option applied")
	}
	if _, err := c.CallService(context.Background(), "button", "press", map[string]any{"entity_id": btn}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "tv/echoshow/cmd/reload-playlist" || p.payload != "1" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, btn); !strings.HasSuffix(s.State, "+00:00") {
		t.Fatalf("button state after press: %q", s.State)
	}
	if _, err := c.CallService(context.Background(), "text", "set_value", map[string]any{"entity_id": txt, "value": "kitchen"}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "tv/echoshow/set/jitsi_room" || p.payload != "kitchen" {
		t.Fatalf("%+v", p)
	}
	if _, err := c.CallService(context.Background(), "text", "set_value", map[string]any{"entity_id": txt, "value": strings.Repeat("x", 65)}, core.Context{}); err == nil {
		t.Fatal("too long text accepted")
	}
	// Optimistic without a state topic.
	tr.inject("homeassistant/number/n2/config", `{"name":"Vol","cmd_t":"n2/set","uniq_id":"n2","min":0,"max":10}`)
	c.CallService(context.Background(), "number", "set_value", map[string]any{"entity_id": "number.vol", "value": 3}, core.Context{})
	if state(t, c, "number.vol").State != "3" {
		t.Fatal("optimistic number not updated")
	}
}
