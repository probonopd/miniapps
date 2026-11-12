/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// eventLog records the events of one type fired on the core.
type eventLog struct {
	mu     sync.Mutex
	events []core.Event
}

func collectEvents(c *core.Core, typ string) *eventLog {
	l := &eventLog{}
	c.Subscribe(typ, func(ev core.Event) {
		l.mu.Lock()
		l.events = append(l.events, ev)
		l.mu.Unlock()
	})
	return l
}

func (l *eventLog) all() []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.Event(nil), l.events...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var activationRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}\+00:00$`)

func call(t *testing.T, c *core.Core, domain, service string, data map[string]any) error {
	t.Helper()
	_, err := c.CallService(context.Background(), domain, service, data, core.Context{})
	return err
}

func TestVacuumValetudoLike(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/vacuum/rockrobo/config", `{"name":"Rockrobo","unique_id":"rockrobo_vacuum","schema":"state",
"supported_features":["start","stop","pause","return_home","battery","status","locate","clean_spot","fan_speed","send_command"],
"command_topic":"valetudo/rockrobo/command","payload_start":"START","payload_pause":"PAUSE","payload_stop":"STOP",
"payload_return_to_base":"HOME","payload_locate":"LOCATE","payload_clean_spot":"SPOT",
"set_fan_speed_topic":"valetudo/rockrobo/fan","fan_speed_list":["off","min","low","medium","high","max"],
"send_command_topic":"valetudo/rockrobo/custom","state_topic":"valetudo/rockrobo/state"}`)
	id := "vacuum.rockrobo"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["supported_features"] != 14204 {
		t.Fatalf("initial %+v", s)
	}
	tr.inject("valetudo/rockrobo/state", `{"state":"docked","battery_level":61,"fan_speed":"off"}`)
	s := state(t, c, id)
	if s.State != "docked" || s.Attributes["battery_level"] != 61 || s.Attributes["fan_speed"] != "off" ||
		s.Attributes["battery_icon"] != "mdi:battery-charging-60" ||
		!reflect.DeepEqual(s.Attributes["fan_speed_list"], []string{"off", "min", "low", "medium", "high", "max"}) {
		t.Fatalf("state %+v", s)
	}
	// A partial message keeps the other fields.
	tr.inject("valetudo/rockrobo/state", `{"state":"cleaning"}`)
	if s := state(t, c, id); s.State != "cleaning" || s.Attributes["battery_level"] != 61 || s.Attributes["battery_icon"] != "mdi:battery-60" {
		t.Fatalf("partial %+v", s)
	}
	tr.inject("valetudo/rockrobo/state", `{"state":"exploding"}`)
	if state(t, c, id).State != "cleaning" {
		t.Fatal("invalid state accepted")
	}
	for service, want := range map[string]string{"start": "START", "pause": "PAUSE", "stop": "STOP", "return_to_base": "HOME", "locate": "LOCATE", "clean_spot": "SPOT"} {
		if err := call(t, c, "vacuum", service, map[string]any{"entity_id": id}); err != nil {
			t.Fatal(service, err)
		}
		if p := tr.lastPub(t); p.topic != "valetudo/rockrobo/command" || p.payload != want {
			t.Fatalf("%s: %+v", service, p)
		}
	}
	if err := call(t, c, "vacuum", "set_fan_speed", map[string]any{"entity_id": id, "fan_speed": "max"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "valetudo/rockrobo/fan" || p.payload != "max" {
		t.Fatalf("%+v", p)
	}
	if err := call(t, c, "vacuum", "set_fan_speed", map[string]any{"entity_id": id, "fan_speed": "turbo"}); err == nil {
		t.Fatal("fan speed outside the list accepted")
	}
	if err := call(t, c, "vacuum", "send_command", map[string]any{"entity_id": id, "command": "go_to"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "valetudo/rockrobo/custom" || p.payload != "go_to" {
		t.Fatalf("%+v", p)
	}
	if err := call(t, c, "vacuum", "send_command", map[string]any{"entity_id": id, "command": "go_to", "params": map[string]any{"x": 25500, "y": 25500}}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, tr.lastPub(t).payload, `{"command":"go_to","x":25500,"y":25500}`)
}

func TestVacuumDefaultFeaturesAndGating(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/vacuum/basic/config", `{"name":"Basic","unique_id":"basic_vac","command_topic":"vac/set","state_topic":"vac/state"}`)
	id := "vacuum.basic"
	// start, stop, return_home, status, battery, clean_spot
	if s := state(t, c, id); s.Attributes["supported_features"] != 8192+8+16+4096+64+1024 {
		t.Fatalf("%+v", s.Attributes)
	}
	if err := call(t, c, "vacuum", "start", map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.payload != "start" {
		t.Fatalf("%+v", p)
	}
	if err := call(t, c, "vacuum", "pause", map[string]any{"entity_id": id}); err == nil {
		t.Fatal("pause is not a default feature")
	}
	tr.inject("homeassistant/vacuum/bad/config", `{"name":"Bad","unique_id":"bad_vac","supported_features":["fly"]}`)
	if c.GetState("vacuum.bad") != nil {
		t.Fatal("unknown feature accepted")
	}
}

func TestBatteryIcon(t *testing.T) {
	for _, tc := range []struct {
		level    float64
		charging bool
		want     string
	}{
		{100, false, "mdi:battery"}, {95, false, "mdi:battery"}, {94, false, "mdi:battery-90"},
		{61, false, "mdi:battery-60"}, {5, false, "mdi:battery-alert"}, {8, true, "mdi:battery-outline"},
		{100, true, "mdi:battery-charging-100"}, {61, true, "mdi:battery-charging-60"},
	} {
		if got := batteryIcon(tc.level, tc.charging); got != tc.want {
			t.Errorf("batteryIcon(%v, %v) = %s, want %s", tc.level, tc.charging, got, tc.want)
		}
	}
}

func TestTagScanned(t *testing.T) {
	c, tr, d := setup(t)
	log := collectEvents(c, "tag_scanned")
	tr.inject("homeassistant/tag/0AFFD2/config", `{"topic":"tags/reader1/scan","value_template":"{{ value_json.uid }}","device":{"identifiers":["reader1"],"name":"Door reader"}}`)
	tr.inject("homeassistant/tag/bare/config", `{"topic":"tags/bare"}`)
	tr.inject("tags/reader1/scan", `{"uid":"04:A2:2B:5A"}`)
	tr.inject("tags/bare", `1234-abcd`)
	tr.inject("tags/bare", ``)
	evs := log.all()
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	devices := c.Devices()
	if len(devices) != 1 {
		t.Fatalf("devices %v", devices)
	}
	if evs[0].Data["tag_id"] != "04:A2:2B:5A" || evs[0].Data["device_id"] != devices[0].ID {
		t.Fatalf("first %+v", evs[0].Data)
	}
	if evs[1].Data["tag_id"] != "1234-abcd" || evs[1].Data["device_id"] != nil {
		t.Fatalf("second %+v", evs[1].Data)
	}
	if n := len(c.States()); n != 0 {
		t.Fatalf("tags must not create states: %v", ids(c))
	}
	tr.inject("homeassistant/tag/nothing/config", `{"value_template":"{{ value }}"}`)
	if d.Count() != 2 {
		t.Fatal("tag without topic accepted")
	}
}

func TestDeviceAutomationZigbee2MQTT(t *testing.T) {
	c, tr, _ := setup(t)
	log := collectEvents(c, "device_trigger")
	dev := `"device":{"identifiers":["zigbee2mqtt_0x00158d00"],"name":"Hallway remote","manufacturer":"Aqara","model":"WXKG11LM"}`
	tr.inject("homeassistant/device_automation/0x00158d00/action_single/config", `{"automation_type":"trigger","type":"button_short_press","subtype":"button_1","payload":"single","topic":"zigbee2mqtt/Hallway remote/action",`+dev+`}`)
	tr.inject("homeassistant/device_automation/0x00158d00/action_double/config", `{"automation_type":"trigger","type":"button_double_press","subtype":"button_1","payload":"double","topic":"zigbee2mqtt/Hallway remote/action",`+dev+`}`)
	tr.inject("zigbee2mqtt/Hallway remote/action", `single`)
	tr.inject("zigbee2mqtt/Hallway remote/action", `hold`)
	tr.inject("zigbee2mqtt/Hallway remote/action", `double`)
	evs := log.all()
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	devices := c.Devices()
	if len(devices) != 1 {
		t.Fatalf("one device expected: %v", devices)
	}
	want := []map[string]any{
		{"device_id": devices[0].ID, "type": "button_short_press", "subtype": "button_1", "payload": "single", "topic": "zigbee2mqtt/Hallway remote/action"},
		{"device_id": devices[0].ID, "type": "button_double_press", "subtype": "button_1", "payload": "double", "topic": "zigbee2mqtt/Hallway remote/action"},
	}
	for i := range want {
		if !reflect.DeepEqual(evs[i].Data, want[i]) {
			t.Errorf("event %d: got %v want %v", i, evs[i].Data, want[i])
		}
	}
	if n := len(c.States()); n != 0 {
		t.Fatalf("triggers must not create states: %v", ids(c))
	}
	// Triggers without a payload filter fire on every message, with a template applied.
	log2 := collectEvents(c, "device_trigger")
	tr.inject("homeassistant/device_automation/x/any/config", `{"automation_type":"trigger","type":"action","subtype":"any","topic":"remote/any","value_template":"{{ value_json.action }}"}`)
	tr.inject("remote/any", `{"action":"rotate_left"}`)
	if evs := log2.all(); len(evs) != 1 || evs[0].Data["payload"] != "rotate_left" || evs[0].Data["device_id"] != nil {
		t.Fatalf("%+v", evs)
	}
	tr.inject("homeassistant/device_automation/x/bad/config", `{"automation_type":"condition","type":"a","subtype":"b","topic":"t"}`)
	tr.inject("remote/bad", `x`)
	if len(log2.all()) != 1 {
		t.Fatal("wrong automation_type accepted")
	}
}

func TestScene(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/scene/movie/config", `{"name":"Movie night","unique_id":"scene_movie","command_topic":"scenes/movie/set","payload_on":"ACTIVATE","retain":false}`)
	if s := state(t, c, "scene.movie_night"); s.State != core.StateUnknown {
		t.Fatalf("%+v", s)
	}
	ctl, ok := c.Controller("scene.movie_night").(core.OnOff)
	if !ok {
		t.Fatal("scene is not an OnOff controller")
	}
	if err := ctl.TurnOn(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "scenes/movie/set" || p.payload != "ACTIVATE" {
		t.Fatalf("%+v", p)
	}
	if s := state(t, c, "scene.movie_night"); !activationRe.MatchString(s.State) {
		t.Fatalf("state %q", s.State)
	}
	before := tr.pubCount()
	if err := ctl.TurnOff(context.Background(), nil); err != nil || tr.pubCount() != before {
		t.Fatal("turn_off must do nothing")
	}
	tr.inject("homeassistant/scene/plain/config", `{"name":"Plain","unique_id":"scene_plain","command_topic":"scenes/plain/set"}`)
	if err := c.Controller("scene.plain").(core.OnOff).TurnOn(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if tr.lastPub(t).payload != "ON" {
		t.Fatal("default payload_on")
	}
	tr.inject("homeassistant/scene/bad/config", `{"name":"Bad","unique_id":"scene_bad"}`)
	if c.GetState("scene.bad") != nil {
		t.Fatal("scene without command_topic accepted")
	}
}

func TestNotifyCommandTemplate(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/notify/pager/config", `{"name":"Pager","unique_id":"notify_pager","command_topic":"pager/send","command_template":"{\"title\": \"{{ title }}\", \"text\": \"{{ message }}\", \"v\": \"{{ value }}\", \"for\": \"{{ entity_id }}\"}"}`)
	tr.inject("homeassistant/notify/plain/config", `{"name":"Plain","unique_id":"notify_plain","command_topic":"plain/send"}`)
	if s := state(t, c, "notify.pager"); s.State != core.StateUnknown {
		t.Fatalf("%+v", s)
	}
	if err := call(t, c, "notify", "send_message", map[string]any{"entity_id": "notify.pager", "message": "Door open", "title": "Alert"}); err != nil {
		t.Fatal(err)
	}
	p := tr.lastPub(t)
	if p.topic != "pager/send" {
		t.Fatalf("%+v", p)
	}
	jsonEq(t, p.payload, `{"title":"Alert","text":"Door open","v":"Door open","for":"notify.pager"}`)
	if s := state(t, c, "notify.pager"); !activationRe.MatchString(s.State) {
		t.Fatalf("state %q", s.State)
	}
	if err := call(t, c, "notify", "send_message", map[string]any{"entity_id": "notify.plain", "message": "hello"}); err != nil {
		t.Fatal(err)
	}
	if p := tr.lastPub(t); p.topic != "plain/send" || p.payload != "hello" {
		t.Fatalf("%+v", p)
	}
}

func TestCameraBase64(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/camera/doorbell/config", `{"name":"Doorbell","unique_id":"cam_door","topic":"doorbell/image","image_encoding":"b64"}`)
	id := "camera.doorbell"
	s := state(t, c, id)
	if s.State != "idle" || s.Attributes["entity_picture"] != "/api/camera_proxy/camera.doorbell" {
		t.Fatalf("%+v", s)
	}
	if v, ok := s.Attributes["access_token"]; !ok || v != nil {
		t.Fatalf("access_token %v %v", v, ok)
	}
	src, ok := c.Controller(id).(core.ImageSource)
	if !ok {
		t.Fatal("camera is not an ImageSource")
	}
	if data, _ := src.Image(); len(data) != 0 {
		t.Fatal("image before first message")
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xff, 0xd9}
	tr.inject("doorbell/image", base64.StdEncoding.EncodeToString(jpeg))
	data, ct := src.Image()
	if !reflect.DeepEqual(data, jpeg) || ct != "image/jpeg" {
		t.Fatalf("%x %s", data, ct)
	}
	// The returned slice is a copy.
	data[0] = 0
	if again, _ := src.Image(); again[0] != 0xff {
		t.Fatal("Image() exposes internal buffer")
	}
	tr.inject("doorbell/image", "!!! not base64")
	if again, _ := src.Image(); !reflect.DeepEqual(again, jpeg) {
		t.Fatal("bad payload replaced the image")
	}
	tr.inject("homeassistant/camera/raw/config", `{"name":"Raw","unique_id":"cam_raw","topic":"raw/image"}`)
	tr.inject("raw/image", "\x89PNG-bytes")
	if data, _ := c.Controller("camera.raw").(core.ImageSource).Image(); string(data) != "\x89PNG-bytes" {
		t.Fatalf("%q", data)
	}
	tr.inject("homeassistant/camera/enc/config", `{"name":"Enc","unique_id":"cam_enc","topic":"enc","image_encoding":"hex"}`)
	if c.GetState("camera.enc") != nil {
		t.Fatal("unsupported image_encoding accepted")
	}
}

func TestImageFromURL(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cover.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, tr, _ := setup(t)
	tr.inject("homeassistant/image/cover/config", `{"name":"Cover","unique_id":"img_cover","url_topic":"media/cover","url_template":"{{ value_json.url }}"}`)
	id := "image.cover"
	if s := state(t, c, id); s.State != core.StateUnknown || s.Attributes["entity_picture"] != "/api/camera_proxy/image.cover" {
		t.Fatalf("%+v", s)
	}
	src := c.Controller(id).(core.ImageSource)
	tr.inject("media/cover", `{"url":"`+srv.URL+`/cover.png"}`)
	waitFor(t, "image download", func() bool { d, _ := src.Image(); return len(d) > 0 })
	data, ct := src.Image()
	if !reflect.DeepEqual(data, png) || ct != "image/png" {
		t.Fatalf("%q %s", data, ct)
	}
	waitFor(t, "state update", func() bool { return activationRe.MatchString(state(t, c, id).State) })
	// A failing download keeps the last image.
	tr.inject("media/cover", `{"url":"`+srv.URL+`/missing.png"}`)
	tr.inject("media/cover", `{"url":"file:///etc/passwd"}`)
	time.Sleep(100 * time.Millisecond)
	if data, _ := src.Image(); !reflect.DeepEqual(data, png) {
		t.Fatal("failed download replaced the image")
	}
}

func TestImageFromTopic(t *testing.T) {
	c, tr, _ := setup(t)
	tr.inject("homeassistant/image/qr/config", `{"name":"QR","unique_id":"img_qr","image_topic":"qr/png","image_encoding":"b64","content_type":"image/png"}`)
	tr.inject("qr/png", base64.StdEncoding.EncodeToString([]byte("PNGDATA")))
	data, ct := c.Controller("image.qr").(core.ImageSource).Image()
	if string(data) != "PNGDATA" || ct != "image/png" {
		t.Fatalf("%q %s", data, ct)
	}
	if !activationRe.MatchString(state(t, c, "image.qr").State) {
		t.Fatal("no timestamp state")
	}
	tr.inject("homeassistant/image/both/config", `{"name":"Both","unique_id":"img_both","image_topic":"a","url_topic":"b"}`)
	tr.inject("homeassistant/image/none/config", `{"name":"None","unique_id":"img_none"}`)
	if c.GetState("image.both") != nil || c.GetState("image.none") != nil {
		t.Fatal("invalid image config accepted")
	}
}
