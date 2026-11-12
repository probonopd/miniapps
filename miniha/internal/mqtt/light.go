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
	"strconv"
	"strings"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["light"] = newLight
}

const (
	featEffect     = 4
	featFlash      = 8
	featTransition = 32
)

// lightState is what both schemas feed into the shared attribute logic.
type lightState struct {
	brightness    int
	hasBrightness bool
	colorMode     string
	colorTemp     int // mireds
	rgb           [3]int
	hs            [2]float64
	xy            [2]float64
	effect        string
}

type light struct {
	e          *entity
	modes      []string
	effectList []string
	minMireds  int
	maxMireds  int
	features   int
	optimistic bool
	st         lightState
	schema     lightSchema
}

type lightSchema interface {
	turnOn(ctx context.Context, l *light, p turnOnParams) error
	turnOff(ctx context.Context, l *light) error
}

func newLight(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	l := &light{e: e, minMireds: cfg.Int("min_mireds", 153), maxMireds: cfg.Int("max_mireds", 500)}
	if cfg.Has("max_kelvin") {
		l.minMireds = kelvinToMired(cfg.Float("max_kelvin", 0))
	}
	if cfg.Has("min_kelvin") {
		l.maxMireds = kelvinToMired(cfg.Float("min_kelvin", 0))
	}
	l.optimistic = cfg.Bool("optimistic", false) || !cfg.Has("state_topic")
	e.attrs = l.attributes
	var err error
	switch schema := cfg.StrDef("schema", "basic"); schema {
	case "json":
		l.schema, err = newJSONLight(l)
	case "basic", "default":
		l.schema, err = newBasicLight(l)
	case "tasmota":
		l.schema, err = newTasmotaLight(l)
	case "template":
		l.schema, err = newTemplateLight(l)
	default:
		return nil, fmt.Errorf("unsupported light schema %q", schema)
	}
	if err != nil {
		return nil, err
	}
	if len(l.effectList) > 0 {
		l.features |= featEffect
	}
	e.d.core.RegisterController(e.entityID, l)
	e.write(false)
	return l, nil
}

func (l *light) stop() {}

func (l *light) supports(mode string) bool {
	for _, m := range l.modes {
		if m == mode {
			return true
		}
	}
	return false
}

func (l *light) supportsColor() bool {
	return l.supports("hs") || l.supports("rgb") || l.supports("xy") || l.supports("rgbw") || l.supports("rgbww")
}

func (l *light) supportsBrightness() bool {
	for _, m := range l.modes {
		if m != "onoff" && m != "unknown" {
			return true
		}
	}
	return false
}

// attributes mirrors LightEntity.state_attributes: color values are
// derived from the active color mode and nulled when the light is off.
func (l *light) attributes() map[string]any {
	a := map[string]any{"supported_color_modes": l.modes, "supported_features": l.features}
	if l.features&featEffect != 0 {
		a["effect_list"] = l.effectList
		a["effect"] = nil
	}
	if l.supports("color_temp") {
		a["min_mireds"] = l.minMireds
		a["max_mireds"] = l.maxMireds
		a["min_color_temp_kelvin"] = miredToKelvin(float64(l.maxMireds))
		a["max_color_temp_kelvin"] = miredToKelvin(float64(l.minMireds))
	}
	for _, k := range []string{"color_mode", "brightness", "color_temp_kelvin", "color_temp", "hs_color", "rgb_color", "xy_color"} {
		a[k] = nil
	}
	if l.e.state != core.StateOn {
		return a
	}
	st := l.st
	if l.features&featEffect != 0 && st.effect != "" {
		a["effect"] = st.effect
	}
	if l.supportsBrightness() && st.hasBrightness {
		a["brightness"] = st.brightness
	}
	mode := st.colorMode
	if mode == "" {
		mode = "unknown"
	}
	a["color_mode"] = mode
	switch mode {
	case "color_temp":
		if st.colorTemp > 0 {
			k := miredToKelvin(float64(st.colorTemp))
			a["color_temp_kelvin"] = k
			a["color_temp"] = st.colorTemp
			rgb := colorTempToRGB(float64(k))
			h, s := rgbToHS(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
			x, y := rgbToXY(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
			a["rgb_color"] = []int{rgb[0], rgb[1], rgb[2]}
			a["hs_color"] = []float64{h, s}
			a["xy_color"] = []float64{x, y}
		}
	case "xy":
		rgb := xyToRGB(st.xy[0], st.xy[1])
		h, s := rgbToHS(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
		a["xy_color"] = []float64{pyround(st.xy[0], 6), pyround(st.xy[1], 6)}
		a["rgb_color"] = []int{rgb[0], rgb[1], rgb[2]}
		a["hs_color"] = []float64{h, s}
	case "hs":
		rgb := hsToRGB(st.hs[0], st.hs[1])
		x, y := rgbToXY(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
		a["hs_color"] = []float64{pyround(st.hs[0], 3), pyround(st.hs[1], 3)}
		a["rgb_color"] = []int{rgb[0], rgb[1], rgb[2]}
		a["xy_color"] = []float64{x, y}
	case "rgb":
		h, s := rgbToHS(float64(st.rgb[0]), float64(st.rgb[1]), float64(st.rgb[2]))
		x, y := rgbToXY(float64(st.rgb[0]), float64(st.rgb[1]), float64(st.rgb[2]))
		a["rgb_color"] = []int{st.rgb[0], st.rgb[1], st.rgb[2]}
		a["hs_color"] = []float64{h, s}
		a["xy_color"] = []float64{x, y}
	}
	return a
}

// turnOnParams is light.turn_on service data after HA's normalization:
// at most one color, expressed in a mode the light supports.
type turnOnParams struct {
	brightness *int
	colorTemp  *int // mireds
	rgb        *[3]int
	hs         *[2]float64
	xy         *[2]float64
	effect     string
	transition *float64
	flash      string
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func numList(v any, n int) ([]float64, bool) {
	list, ok := v.([]any)
	if !ok || len(list) != n {
		return nil, false
	}
	out := make([]float64, n)
	for i, e := range list {
		f, ok := num(e)
		if !ok {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

// parseTurnOn validates service data and converts colors the way HA's
// light service does before the entity sees them.
func (l *light) parseTurnOn(data map[string]any) (turnOnParams, error) {
	var p turnOnParams
	if v, ok := data["brightness_pct"]; ok {
		f, ok := num(v)
		if !ok || f < 0 || f > 100 {
			return p, fmt.Errorf("brightness_pct must be 0..100")
		}
		b := int(math.RoundToEven(255 * f / 100))
		p.brightness = &b
	}
	if v, ok := data["brightness"]; ok {
		f, ok := num(v)
		if !ok || f < 0 || f > 255 {
			return p, fmt.Errorf("brightness must be 0..255")
		}
		b := int(f)
		p.brightness = &b
	}
	if v, ok := data["transition"]; ok {
		f, ok := num(v)
		if !ok || f < 0 {
			return p, fmt.Errorf("transition must be a non-negative number")
		}
		p.transition = &f
	}
	if v, ok := data["effect"]; ok {
		s, _ := v.(string)
		if s == "" {
			return p, fmt.Errorf("effect must be a string")
		}
		p.effect = s
	}
	if v, ok := data["flash"]; ok {
		s, _ := v.(string)
		if s != "short" && s != "long" {
			return p, fmt.Errorf("flash must be short or long")
		}
		p.flash = s
	}
	var kelvin float64
	haveTemp := false
	if v, ok := data["color_temp_kelvin"]; ok {
		kelvin, ok = num(v)
		if !ok {
			return p, fmt.Errorf("color_temp_kelvin must be a number")
		}
		haveTemp = true
	} else if v, ok := data["kelvin"]; ok {
		kelvin, ok = num(v)
		if !ok {
			return p, fmt.Errorf("kelvin must be a number")
		}
		haveTemp = true
	} else if v, ok := data["color_temp"]; ok {
		m, ok := num(v)
		if !ok || m <= 0 {
			return p, fmt.Errorf("color_temp must be a positive number")
		}
		kelvin = float64(miredToKelvin(m))
		haveTemp = true
	}
	var rgb *[3]int
	var hs *[2]float64
	var xy *[2]float64
	if v, ok := data["rgb_color"]; ok {
		f, ok := numList(v, 3)
		if !ok {
			return p, fmt.Errorf("rgb_color must be [r, g, b]")
		}
		rgb = &[3]int{int(f[0]), int(f[1]), int(f[2])}
	}
	if v, ok := data["hs_color"]; ok {
		f, ok := numList(v, 2)
		if !ok {
			return p, fmt.Errorf("hs_color must be [hue, saturation]")
		}
		hs = &[2]float64{f[0], f[1]}
	}
	if v, ok := data["xy_color"]; ok {
		f, ok := numList(v, 2)
		if !ok {
			return p, fmt.Errorf("xy_color must be [x, y]")
		}
		xy = &[2]float64{f[0], f[1]}
	}
	if haveTemp {
		if l.supports("color_temp") {
			m := kelvinToMired(kelvin)
			m = max(l.minMireds, min(l.maxMireds, m))
			p.colorTemp = &m
		} else if l.supportsColor() {
			c := colorTempToRGB(kelvin)
			rgb = &c
		}
	}
	// Convert the requested color into one the light supports, in HA's
	// order of preference.
	switch {
	case rgb != nil:
		switch {
		case l.supports("rgb"):
			p.rgb = rgb
		case l.supports("hs"):
			h, s := rgbToHS(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
			p.hs = &[2]float64{h, s}
		case l.supports("xy"):
			x, y := rgbToXY(float64(rgb[0]), float64(rgb[1]), float64(rgb[2]))
			p.xy = &[2]float64{x, y}
		}
	case hs != nil:
		switch {
		case l.supports("hs"):
			p.hs = hs
		case l.supports("rgb"):
			c := hsToRGB(hs[0], hs[1])
			p.rgb = &c
		case l.supports("xy"):
			c := hsToRGB(hs[0], hs[1])
			x, y := rgbToXY(float64(c[0]), float64(c[1]), float64(c[2]))
			p.xy = &[2]float64{x, y}
		}
	case xy != nil:
		switch {
		case l.supports("xy"):
			p.xy = xy
		case l.supports("rgb"):
			c := xyToRGB(xy[0], xy[1])
			p.rgb = &c
		case l.supports("hs"):
			c := xyToRGB(xy[0], xy[1])
			h, s := rgbToHS(float64(c[0]), float64(c[1]), float64(c[2]))
			p.hs = &[2]float64{h, s}
		}
	}
	return p, nil
}

func (l *light) TurnOn(ctx context.Context, data map[string]any) error {
	p, err := l.parseTurnOn(data)
	if err != nil {
		return err
	}
	return l.schema.turnOn(ctx, l, p)
}

func (l *light) TurnOff(ctx context.Context, _ map[string]any) error {
	return l.schema.turnOff(ctx, l)
}

func (l *light) deviceBrightness(b int, scale int) int {
	return max(1, min(scale, int(math.RoundToEven(float64(b)/255*float64(scale)))))
}

func (l *light) fromDeviceBrightness(v float64, scale int) int {
	return min(255, int(math.RoundToEven(v/float64(scale)*255)))
}

// --- JSON schema ---

type jsonLight struct {
	cmdTopic string
	scale    int
}

func newJSONLight(l *light) (lightSchema, error) {
	cfg := l.e.cfg
	j := &jsonLight{cmdTopic: cfg.Str("command_topic"), scale: cfg.Int("brightness_scale", 255)}
	l.modes = cfg.Strings("supported_color_modes")
	if len(l.modes) == 0 {
		if cfg.Bool("brightness", false) {
			l.modes = []string{"brightness"}
		} else {
			l.modes = []string{"onoff"}
		}
	}
	for _, m := range l.modes {
		switch m {
		case "onoff", "brightness", "color_temp", "hs", "xy", "rgb", "rgbw", "rgbww", "white":
		default:
			return nil, fmt.Errorf("unsupported color mode %q", m)
		}
	}
	if cfg.Bool("effect", false) {
		l.effectList = cfg.Strings("effect_list")
	}
	l.features |= featFlash | featTransition
	if cfg.Has("state_topic") {
		l.e.subscribe(cfg.Str("state_topic"), func(m Message) { j.onState(l, m) })
	}
	return j, nil
}

func (j *jsonLight) onState(l *light, m Message) {
	var v map[string]any
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		l.e.d.log.Warn("light state is not JSON", "entity_id", l.e.entityID)
		return
	}
	e := l.e
	e.mu.Lock()
	switch s, _ := v["state"].(string); s {
	case "ON":
		e.state = core.StateOn
	case "OFF":
		e.state = core.StateOff
	default:
		if _, present := v["state"]; present && v["state"] == nil {
			e.state = ""
		}
	}
	if b, ok := num(v["brightness"]); ok && l.supportsBrightness() {
		l.st.brightness = l.fromDeviceBrightness(b, j.scale)
		l.st.hasBrightness = true
	}
	if mode, ok := v["color_mode"].(string); ok {
		l.st.colorMode = mode
	}
	if ct, ok := num(v["color_temp"]); ok && ct > 0 {
		l.st.colorTemp = int(ct)
		if _, hasMode := v["color_mode"]; !hasMode {
			l.st.colorMode = "color_temp"
		}
	}
	if c, ok := v["color"].(map[string]any); ok {
		if x, ok := num(c["x"]); ok {
			y, _ := num(c["y"])
			l.st.xy = [2]float64{x, y}
			if _, hasMode := v["color_mode"]; !hasMode && l.supports("xy") {
				l.st.colorMode = "xy"
			}
		}
		if r, ok := num(c["r"]); ok {
			g, _ := num(c["g"])
			b, _ := num(c["b"])
			l.st.rgb = [3]int{int(r), int(g), int(b)}
			if _, hasMode := v["color_mode"]; !hasMode && l.supports("rgb") {
				l.st.colorMode = "rgb"
			}
		}
		if h, ok := num(c["h"]); ok {
			s, _ := num(c["s"])
			l.st.hs = [2]float64{h, s}
			if _, hasMode := v["color_mode"]; !hasMode && l.supports("hs") {
				l.st.colorMode = "hs"
			}
		}
	}
	if fx, ok := v["effect"].(string); ok {
		l.st.effect = fx
	}
	if l.st.colorMode == "" && len(l.modes) == 1 {
		l.st.colorMode = l.modes[0]
	}
	e.mu.Unlock()
	e.write(false)
}

func (j *jsonLight) turnOn(ctx context.Context, l *light, p turnOnParams) error {
	msg := map[string]any{"state": "ON"}
	if p.brightness != nil && l.supportsBrightness() {
		msg["brightness"] = l.deviceBrightness(*p.brightness, j.scale)
	}
	if p.colorTemp != nil {
		msg["color_temp"] = *p.colorTemp
	}
	switch {
	case p.hs != nil:
		msg["color"] = map[string]any{"h": p.hs[0], "s": p.hs[1]}
	case p.rgb != nil:
		msg["color"] = map[string]any{"r": p.rgb[0], "g": p.rgb[1], "b": p.rgb[2]}
	case p.xy != nil:
		msg["color"] = map[string]any{"x": p.xy[0], "y": p.xy[1]}
	}
	if p.effect != "" {
		msg["effect"] = p.effect
	}
	if p.transition != nil {
		msg["transition"] = *p.transition
	}
	if p.flash != "" {
		msg["flash"] = l.e.cfg.Int("flash_time_"+p.flash, map[string]int{"short": 2, "long": 10}[p.flash])
	}
	b, _ := json.Marshal(msg)
	if err := l.e.publish(j.cmdTopic, string(b)); err != nil {
		return err
	}
	if l.optimistic {
		l.e.mu.Lock()
		l.applyOptimistic(p)
		l.e.state = core.StateOn
		l.e.mu.Unlock()
		l.e.write(false, ctx)
	}
	return nil
}

func (l *light) applyOptimistic(p turnOnParams) {
	if p.brightness != nil {
		l.st.brightness = *p.brightness
		l.st.hasBrightness = true
	}
	if p.colorTemp != nil {
		l.st.colorTemp = *p.colorTemp
		l.st.colorMode = "color_temp"
	}
	if p.rgb != nil {
		l.st.rgb = *p.rgb
		l.st.colorMode = "rgb"
	}
	if p.hs != nil {
		l.st.hs = *p.hs
		l.st.colorMode = "hs"
	}
	if p.xy != nil {
		l.st.xy = *p.xy
		l.st.colorMode = "xy"
	}
	if p.effect != "" {
		l.st.effect = p.effect
	}
	if l.st.colorMode == "" && len(l.modes) == 1 {
		l.st.colorMode = l.modes[0]
	}
}

func (j *jsonLight) turnOff(ctx context.Context, l *light) error {
	if err := l.e.publish(j.cmdTopic, `{"state":"OFF"}`); err != nil {
		return err
	}
	if l.optimistic {
		l.e.setState(core.StateOff, ctx)
	}
	return nil
}

// --- basic (default) schema: one topic per property ---

type basicLight struct {
	cmdTopic   string
	payloadOn  string
	payloadOff string
	onCmdType  string
	scale      int
	topics     map[string]string // property -> command topic
	cmdTpls    map[string]*template.Template
}

var basicProps = []string{"brightness", "color_temp", "rgb", "hs", "xy", "effect", "white"}

func newBasicLight(l *light) (lightSchema, error) {
	cfg := l.e.cfg
	b := &basicLight{cmdTopic: cfg.Str("command_topic"), payloadOn: cfg.StrDef("payload_on", "ON"), payloadOff: cfg.StrDef("payload_off", "OFF"),
		onCmdType: cfg.StrDef("on_command_type", "last"), scale: cfg.Int("brightness_scale", 255), topics: map[string]string{}, cmdTpls: map[string]*template.Template{}}
	switch b.onCmdType {
	case "last", "first", "brightness":
	default:
		return nil, fmt.Errorf("invalid on_command_type %q", b.onCmdType)
	}
	for _, p := range basicProps {
		if cfg.Has(p + "_command_topic") {
			b.topics[p] = cfg.Str(p + "_command_topic")
			tpl, err := cfg.Template(p + "_command_template")
			if err != nil {
				return nil, err
			}
			b.cmdTpls[p] = tpl
		}
	}
	for _, m := range []string{"color_temp", "rgb", "hs", "xy", "white"} {
		if _, ok := b.topics[m]; ok {
			l.modes = append(l.modes, m)
		}
	}
	if len(l.modes) == 0 {
		if _, ok := b.topics["brightness"]; ok {
			l.modes = []string{"brightness"}
		} else {
			l.modes = []string{"onoff"}
		}
	}
	if _, ok := b.topics["effect"]; ok {
		l.effectList = cfg.Strings("effect_list")
	}
	// State topics, each with its own value template.
	stateTpl, err := cfg.Template("state_value_template")
	if err != nil {
		return nil, err
	}
	stateOn := cfg.StrDef("state_on", b.payloadOn)
	stateOff := cfg.StrDef("state_off", b.payloadOff)
	if cfg.Has("state_topic") {
		l.e.subscribe(cfg.Str("state_topic"), func(m Message) {
			payload, ok := b.rendered(l, stateTpl, m)
			if !ok {
				return
			}
			switch payload {
			case stateOn:
				l.e.setState(core.StateOn, context.Background())
			case stateOff:
				l.e.setState(core.StateOff, context.Background())
			case "None":
				l.e.setState("", context.Background())
			default:
				l.e.d.log.Warn("light payload matches neither state_on nor state_off", "entity_id", l.e.entityID, "payload", payload)
			}
		})
	}
	sub := func(prop string, apply func(payload string) bool) error {
		if !cfg.Has(prop + "_state_topic") {
			return nil
		}
		tpl, err := cfg.Template(prop + "_value_template")
		if err != nil {
			return err
		}
		l.e.subscribe(cfg.Str(prop+"_state_topic"), func(m Message) {
			payload, ok := b.rendered(l, tpl, m)
			if !ok {
				return
			}
			l.e.mu.Lock()
			changed := apply(payload)
			l.e.mu.Unlock()
			if changed {
				l.e.write(false)
			} else {
				l.e.d.log.Warn("ignoring light payload", "entity_id", l.e.entityID, "property", prop, "payload", payload)
			}
		})
		return nil
	}
	if err := sub("brightness", func(s string) bool {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return false
		}
		l.st.brightness = l.fromDeviceBrightness(f, b.scale)
		l.st.hasBrightness = true
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("color_temp", func(s string) bool {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 {
			return false
		}
		if cfg.Bool("color_temp_kelvin", false) {
			l.st.colorTemp = kelvinToMired(f)
		} else {
			l.st.colorTemp = int(f)
		}
		if !cfg.Has("color_mode_state_topic") {
			l.st.colorMode = "color_temp"
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("rgb", func(s string) bool {
		f, ok := splitFloats(s, 3)
		if !ok {
			return false
		}
		l.st.rgb = [3]int{int(f[0]), int(f[1]), int(f[2])}
		if !cfg.Has("color_mode_state_topic") {
			l.st.colorMode = "rgb"
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("hs", func(s string) bool {
		f, ok := splitFloats(s, 2)
		if !ok {
			return false
		}
		l.st.hs = [2]float64{f[0], f[1]}
		if !cfg.Has("color_mode_state_topic") {
			l.st.colorMode = "hs"
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("xy", func(s string) bool {
		f, ok := splitFloats(s, 2)
		if !ok {
			return false
		}
		l.st.xy = [2]float64{f[0], f[1]}
		if !cfg.Has("color_mode_state_topic") {
			l.st.colorMode = "xy"
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("effect", func(s string) bool {
		l.st.effect = s
		return true
	}); err != nil {
		return nil, err
	}
	if err := sub("color_mode", func(s string) bool {
		l.st.colorMode = s
		return true
	}); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *basicLight) rendered(l *light, tpl *template.Template, m Message) (string, bool) {
	payload := string(m.Payload)
	if tpl != nil {
		var err error
		payload, err = l.e.render(tpl, m.Payload)
		if err != nil {
			l.e.d.log.Warn("light template failed", "entity_id", l.e.entityID, "err", err)
			return "", false
		}
	}
	if payload == "" {
		return "", false
	}
	return payload, true
}

func splitFloats(s string, n int) ([]float64, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != n {
		return nil, false
	}
	out := make([]float64, n)
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

func (b *basicLight) send(l *light, prop, value string) error {
	if tpl := b.cmdTpls[prop]; tpl != nil {
		var err error
		value, err = l.e.renderCommand(tpl, value)
		if err != nil {
			return err
		}
	}
	return l.e.publish(b.topics[prop], value)
}

func (b *basicLight) turnOn(ctx context.Context, l *light, p turnOnParams) error {
	if b.onCmdType == "first" {
		if err := l.e.publish(b.cmdTopic, b.payloadOn); err != nil {
			return err
		}
	} else if b.onCmdType == "brightness" && p.brightness == nil {
		// HA resends the last brightness (or full) instead of a power
		// command when the device is switched on by brightness.
		saved := l.st.brightness
		if !l.st.hasBrightness || saved == 0 {
			saved = 255
		}
		p.brightness = &saved
	}
	if _, ok := b.topics["brightness"]; ok && p.brightness != nil {
		if err := b.send(l, "brightness", strconv.Itoa(l.deviceBrightness(*p.brightness, b.scale))); err != nil {
			return err
		}
	}
	if _, ok := b.topics["color_temp"]; ok && p.colorTemp != nil {
		v := *p.colorTemp
		if l.e.cfg.Bool("color_temp_kelvin", false) {
			v = miredToKelvin(float64(v))
		}
		if err := b.send(l, "color_temp", strconv.Itoa(v)); err != nil {
			return err
		}
	}
	if p.rgb != nil {
		if err := b.send(l, "rgb", fmt.Sprintf("%d,%d,%d", p.rgb[0], p.rgb[1], p.rgb[2])); err != nil {
			return err
		}
	}
	if p.hs != nil {
		if err := b.send(l, "hs", fmt.Sprintf("%s,%s", template.PyStr(p.hs[0]), template.PyStr(p.hs[1]))); err != nil {
			return err
		}
	}
	if p.xy != nil {
		if err := b.send(l, "xy", fmt.Sprintf("%s,%s", template.PyStr(p.xy[0]), template.PyStr(p.xy[1]))); err != nil {
			return err
		}
	}
	if p.effect != "" {
		if _, ok := b.topics["effect"]; ok {
			if err := b.send(l, "effect", p.effect); err != nil {
				return err
			}
		}
	}
	if b.onCmdType == "last" {
		if err := l.e.publish(b.cmdTopic, b.payloadOn); err != nil {
			return err
		}
	}
	if l.optimistic {
		l.e.mu.Lock()
		l.applyOptimistic(p)
		l.e.state = core.StateOn
		l.e.mu.Unlock()
		l.e.write(false, ctx)
	}
	return nil
}

func (b *basicLight) turnOff(ctx context.Context, l *light) error {
	if err := l.e.publish(b.cmdTopic, b.payloadOff); err != nil {
		return err
	}
	if l.optimistic {
		l.e.setState(core.StateOff, ctx)
	}
	return nil
}
