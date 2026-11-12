/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// Tasmota native discovery: every Tasmota device publishes a retained
// description of itself under tasmota/discovery/<mac>/config, whatever
// SetOption19 says. This mirrors what HA's tasmota integration
// (hatasmota) derives from it: relays become switches, light relays
// become lights with the device's light subtype.

const tasmotaDiscoveryTopic = "tasmota/discovery/+/config"

type tasmotaConfig struct {
	IP   string         `json:"ip"`
	DN   string         `json:"dn"`
	FN   []*string      `json:"fn"`
	HN   string         `json:"hn"`
	MAC  string         `json:"mac"`
	MD   string         `json:"md"`
	SW   string         `json:"sw"`
	T    string         `json:"t"`
	FT   string         `json:"ft"`
	TP   []string       `json:"tp"`
	RL   []int          `json:"rl"`
	LtSt int            `json:"lt_st"`
	LK   *int           `json:"lk"`
	SO   map[string]int `json:"so"`
	Ofln string         `json:"ofln"`
	Onln string         `json:"onln"`
	Ver  int            `json:"ver"`
}

const (
	tasmotaRelay = 1
	tasmotaLight = 2
)

// topic expands Tasmota's full-topic template for one prefix.
func (t *tasmotaConfig) topic(prefixIdx int, suffix string) string {
	ft := t.FT
	if ft == "" {
		ft = "%prefix%/%topic%/"
	}
	tp := t.TP
	if len(tp) != 3 {
		tp = []string{"cmnd", "stat", "tele"}
	}
	s := strings.ReplaceAll(ft, "%prefix%", tp[prefixIdx])
	s = strings.ReplaceAll(s, "%topic%", t.T)
	if !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s + suffix
}

func (t *tasmotaConfig) friendlyName(idx int) string {
	if idx < len(t.FN) && t.FN[idx] != nil && *t.FN[idx] != "" {
		return *t.FN[idx]
	}
	return fmt.Sprintf("%s %d", t.DN, idx+1)
}

func (d *Discovery) onTasmotaConfig(m Message) {
	parts := strings.Split(m.Topic, "/")
	if len(parts) != 4 {
		return
	}
	mac := parts[2]
	prefix := "tasmota/" + mac + "/"
	if len(strings.TrimSpace(string(m.Payload))) == 0 {
		d.removePrefix(prefix)
		return
	}
	var cfg tasmotaConfig
	if err := json.Unmarshal(m.Payload, &cfg); err != nil {
		d.log.Error("tasmota discovery: bad payload", "mac", mac, "err", err)
		return
	}
	if cfg.T == "" || cfg.MAC == "" {
		d.log.Error("tasmota discovery: payload without topic or mac", "mac", mac)
		return
	}
	if cfg.Ofln == "" {
		cfg.Ofln = "Offline"
	}
	if cfg.Onln == "" {
		cfg.Onln = "Online"
	}
	device := map[string]any{
		"identifiers": []any{cfg.MAC}, "name": cfg.DN, "model": cfg.MD, "sw_version": cfg.SW,
		"manufacturer": "Tasmota", "configuration_url": "http://" + cfg.IP + "/",
	}
	relayCount := 0
	for _, r := range cfg.RL {
		if r != 0 {
			relayCount++
		}
	}
	base := func(idx int) Config {
		powerKey := "POWER"
		if relayCount > 1 {
			powerKey = "POWER" + strconv.Itoa(idx+1)
		}
		return Config{
			"name": cfg.friendlyName(idx), "device": device,
			"availability_topic": cfg.topic(2, "LWT"), "payload_available": cfg.Onln, "payload_not_available": cfg.Ofln,
			"tasmota": map[string]any{
				"cmnd": cfg.topic(0, ""), "stat": cfg.topic(1, "RESULT"), "tele": cfg.topic(2, "STATE"),
				"lwt": cfg.topic(2, "LWT"), "online": cfg.Onln,
				"power_key": powerKey, "idx": idx + 1, "subtype": cfg.LtSt,
			},
		}
	}
	wanted := map[string]bool{}
	// An RGBCW device with RGB and white unlinked (lk 0, SetOption37
	// split) reports two light relays: POWER1 drives the RGB part and
	// POWER2 the white part, each with its own dimmer (Dimmer1/Dimmer2).
	split := cfg.LtSt == 5 && cfg.LK != nil && *cfg.LK == 0
	lightNo := 0
	for idx, r := range cfg.RL {
		switch r {
		case tasmotaRelay:
			id := prefix + "switch" + strconv.Itoa(idx+1)
			c := base(idx)
			c["unique_id"] = cfg.MAC + "_switch_relay_" + strconv.Itoa(idx)
			c["command_topic"] = cfg.topic(0, "POWER"+strconv.Itoa(idx+1))
			wanted[id] = true
			d.install(id, "switch", c, newTasmotaSwitch)
		case tasmotaLight:
			if cfg.LtSt == 0 {
				continue
			}
			lightNo++
			id := prefix + "light" + strconv.Itoa(idx+1)
			c := base(idx)
			c["unique_id"] = cfg.MAC + "_light_light_" + strconv.Itoa(idx)
			c["command_topic"] = cfg.topic(0, "POWER"+strconv.Itoa(idx+1))
			c["schema"] = "tasmota"
			t := c["tasmota"].(map[string]any)
			t["dimmer_key"] = "Dimmer"
			switch {
			case split && lightNo == 1:
				t["subtype"] = 3
				t["dimmer_key"] = "Dimmer1"
			case split && lightNo == 2:
				t["subtype"] = 2
				t["dimmer_key"] = "Dimmer2"
			case lightNo > 1:
				// Further light relays (multi-channel, SetOption68) are
				// exposed as on/off lights only.
				t["subtype"] = 0
			}
			wanted[id] = true
			d.install(id, "light", c, newLight)
		}
	}
	// Ask the device for its current state; otherwise entities stay
	// unknown until the next telemetry period.
	if err := d.tr.Publish(cfg.topic(0, "STATE"), nil, 0, false); err != nil {
		d.log.Warn("tasmota: state request failed", "mac", mac, "err", err)
	}
	// Relays that disappeared from the config are removed.
	d.mu.Lock()
	var stale []string
	for id := range d.items {
		if strings.HasPrefix(id, prefix) && !wanted[id] {
			stale = append(stale, id)
		}
	}
	d.mu.Unlock()
	for _, id := range stale {
		d.remove(id)
	}
}

// tasmotaPowerTopic matches Tasmota's relay command topics in either
// full-topic order (cmnd/<topic>/POWERn or <topic>/cmnd/POWERn).
var tasmotaPowerTopic = regexp.MustCompile(`^(.*/)?cmnd/(.*/)?POWER\d*$`)

// tasmotaKick asks a Tasmota device discovered through HA discovery
// (SetOption19) for its state right away instead of at the next
// telemetry period, which can be minutes away. An empty STATE command
// yields tele/STATE; sensors only come with the telemetry, which
// "TelePeriod <current value>" forces without changing the setting, so
// the value is queried first. Repeated when the device comes back online.
func (d *Discovery) tasmotaKick(e *entity) {
	cmd := e.cfg.Str("command_topic")
	if !tasmotaPowerTopic.MatchString(cmd) || !strings.HasSuffix(e.cfg.Str("availability_topic"), "/LWT") {
		return
	}
	base := cmd[:strings.LastIndex(cmd, "/")+1]
	result := strings.Replace(base, "cmnd/", "stat/", 1) + "RESULT"
	d.kickMu.Lock()
	if _, ok := d.kickSubs[result]; !ok {
		unsub, err := d.tr.Subscribe(result, func(m Message) {
			var v struct {
				TelePeriod *int `json:"TelePeriod"`
			}
			if json.Unmarshal(m.Payload, &v) != nil || v.TelePeriod == nil || *v.TelePeriod < 10 {
				return
			}
			d.tr.Publish(base+"TelePeriod", []byte(strconv.Itoa(*v.TelePeriod)), 0, false)
		})
		if err != nil {
			d.log.Warn("tasmota: subscribe failed", "topic", result, "err", err)
		} else {
			d.kickSubs[result] = unsub
		}
	}
	d.kickMu.Unlock()
	kick := func() {
		// Own mutex: install() calls this while holding d.mu.
		d.kickMu.Lock()
		last, seen := d.kicked[base]
		if seen && d.now().Sub(last) < 5*time.Second {
			d.kickMu.Unlock()
			return
		}
		d.kicked[base] = d.now()
		d.kickMu.Unlock()
		for _, c := range []string{"STATE", "TelePeriod"} {
			if err := d.tr.Publish(base+c, nil, 0, false); err != nil {
				d.log.Warn("tasmota: state request failed", "topic", base+c, "err", err)
			}
		}
	}
	e.mu.Lock()
	e.onAvailable = append(e.onAvailable, kick)
	e.mu.Unlock()
	kick()
}

func (d *Discovery) removePrefix(prefix string) {
	d.mu.Lock()
	var ids []string
	for id := range d.items {
		if strings.HasPrefix(id, prefix) {
			ids = append(ids, id)
		}
	}
	d.mu.Unlock()
	for _, id := range ids {
		d.remove(id)
	}
}

type tasmotaInfo struct {
	cmnd, stat, tele string
	lwt, online      string
	powerKey         string
	dimmerKey        string
	idx              int
	subtype          int
}

func tasmotaFrom(cfg Config) tasmotaInfo {
	t := cfg.Map("tasmota")
	return tasmotaInfo{cmnd: t.Str("cmnd"), stat: t.Str("stat"), tele: t.Str("tele"), lwt: t.Str("lwt"), online: t.Str("online"),
		powerKey: t.Str("power_key"), dimmerKey: t.StrDef("dimmer_key", "Dimmer"), idx: t.Int("idx", 1), subtype: t.Int("subtype", 0)}
}

// requestStateWhenOnline asks for a fresh STATE whenever the device
// (re)appears, since RESULT only reports changes.
func (t tasmotaInfo) requestStateWhenOnline(e *entity) {
	e.subscribe(t.lwt, func(m Message) {
		if string(m.Payload) == t.online {
			e.publish(t.cmnd+"STATE", "")
		}
	})
}

// dimmer reads the light's own dimmer, falling back to the shared one.
func (t tasmotaInfo) dimmer(v map[string]any) (float64, bool) {
	if d, ok := num(v[t.dimmerKey]); ok {
		return d, true
	}
	if t.dimmerKey != "Dimmer" {
		return 0, false
	}
	return num(v["Dimmer"])
}

// powerState reads POWER / POWERn from a STATE or RESULT payload.
func (t tasmotaInfo) powerState(v map[string]any) (string, bool) {
	for _, k := range []string{t.powerKey, "POWER" + strconv.Itoa(t.idx), "POWER"} {
		if s, ok := v[k].(string); ok {
			switch s {
			case "ON":
				return core.StateOn, true
			case "OFF":
				return core.StateOff, true
			}
		}
	}
	return "", false
}

// --- switch ---

type tasmotaSwitch struct {
	e *entity
	t tasmotaInfo
}

func newTasmotaSwitch(e *entity) (component, error) {
	s := &tasmotaSwitch{e: e, t: tasmotaFrom(e.cfg)}
	handle := func(m Message) {
		var v map[string]any
		if err := json.Unmarshal(m.Payload, &v); err != nil {
			return
		}
		if st, ok := s.t.powerState(v); ok {
			e.setState(st, context.Background())
		}
	}
	e.subscribe(s.t.tele, handle)
	e.subscribe(s.t.stat, handle)
	s.t.requestStateWhenOnline(e)
	e.d.core.RegisterController(e.entityID, s)
	e.write(false)
	return s, nil
}

func (s *tasmotaSwitch) stop() {}

func (s *tasmotaSwitch) TurnOn(_ context.Context, _ map[string]any) error {
	return s.e.publish(s.t.cmnd+"POWER"+strconv.Itoa(s.t.idx), "ON")
}

func (s *tasmotaSwitch) TurnOff(_ context.Context, _ map[string]any) error {
	return s.e.publish(s.t.cmnd+"POWER"+strconv.Itoa(s.t.idx), "OFF")
}

// --- light schema ---

type tasmotaLightSchema struct {
	t tasmotaInfo
}

func newTasmotaLight(l *light) (lightSchema, error) {
	s := &tasmotaLightSchema{t: tasmotaFrom(l.e.cfg)}
	switch s.t.subtype {
	case 0:
		l.modes = []string{"onoff"}
	case 1:
		l.modes = []string{"brightness"}
	case 2:
		l.modes = []string{"color_temp"}
	case 3, 4:
		l.modes = []string{"hs"}
	case 5:
		l.modes = []string{"color_temp", "hs"}
	default:
		return nil, fmt.Errorf("unknown tasmota light subtype %d", s.t.subtype)
	}
	l.optimistic = false
	handle := func(m Message) { s.onState(l, m) }
	l.e.subscribe(s.t.tele, handle)
	l.e.subscribe(s.t.stat, handle)
	s.t.requestStateWhenOnline(l.e)
	return s, nil
}

func (s *tasmotaLightSchema) onState(l *light, m Message) {
	var v map[string]any
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return
	}
	e := l.e
	e.mu.Lock()
	if st, ok := s.t.powerState(v); ok {
		e.state = st
	}
	if dim, ok := s.t.dimmer(v); ok {
		l.st.brightness = min(255, int(math.RoundToEven(dim*255/100)))
		l.st.hasBrightness = true
	}
	if ct, ok := num(v["CT"]); ok && ct > 0 {
		l.st.colorTemp = int(ct)
	}
	if hsb, ok := v["HSBColor"].(string); ok {
		if f, ok := splitFloats(hsb, 3); ok {
			l.st.hs = [2]float64{f[0], f[1]}
		}
	}
	white, hasWhite := num(v["White"])
	switch {
	case len(l.modes) == 1:
		l.st.colorMode = l.modes[0]
	case hasWhite && white > 0, !hasWhite && l.st.hs[1] == 0:
		l.st.colorMode = "color_temp"
	default:
		l.st.colorMode = "hs"
	}
	e.mu.Unlock()
	e.write(false)
}

func (s *tasmotaLightSchema) turnOn(_ context.Context, l *light, p turnOnParams) error {
	power := "POWER" + strconv.Itoa(s.t.idx)
	var cmds []string
	if p.brightness != nil && l.supportsBrightness() {
		cmds = append(cmds, s.t.dimmerKey+" "+strconv.Itoa(max(1, int(math.RoundToEven(float64(*p.brightness)*100/255)))))
	}
	if p.colorTemp != nil {
		cmds = append(cmds, "CT "+strconv.Itoa(*p.colorTemp))
	}
	if p.hs != nil {
		cmds = append(cmds, "HSBColor1 "+strconv.Itoa(int(math.Round(p.hs[0]))), "HSBColor2 "+strconv.Itoa(int(math.Round(p.hs[1]))))
	}
	if len(cmds) == 0 {
		return l.e.publish(s.t.cmnd+power, "ON")
	}
	// Backlog runs the commands in order on the device; power first so
	// that a color change on a dark light also switches it on.
	return l.e.publish(s.t.cmnd+"Backlog", power+" ON; "+strings.Join(cmds, "; "))
}

func (s *tasmotaLightSchema) turnOff(_ context.Context, l *light) error {
	return l.e.publish(s.t.cmnd+"POWER"+strconv.Itoa(s.t.idx), "OFF")
}
