/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// templateLight is the light schema "template": the device protocol is
// whatever the user's templates produce and parse.
type templateLight struct {
	cmdTopic string
	onTpl    *template.Template
	offTpl   *template.Template
	kelvin   bool // color_temp travels in Kelvin instead of mireds

	stateTpl, brightnessTpl, colorTempTpl, effectTpl *template.Template
	redTpl, greenTpl, blueTpl                        *template.Template
}

func newTemplateLight(l *light) (lightSchema, error) {
	cfg := l.e.cfg
	t := &templateLight{cmdTopic: cfg.Str("command_topic"), kelvin: cfg.Bool("color_temp_kelvin", false)}
	for _, o := range []struct {
		key string
		dst **template.Template
		req bool
	}{
		{"command_on_template", &t.onTpl, true}, {"command_off_template", &t.offTpl, true},
		{"state_template", &t.stateTpl, false}, {"brightness_template", &t.brightnessTpl, false},
		{"color_temp_template", &t.colorTempTpl, false}, {"effect_template", &t.effectTpl, false},
		{"red_template", &t.redTpl, false}, {"green_template", &t.greenTpl, false}, {"blue_template", &t.blueTpl, false},
	} {
		if o.req && !cfg.Has(o.key) {
			return nil, errMissing(o.key)
		}
		tpl, err := cfg.Template(o.key)
		if err != nil {
			return nil, err
		}
		*o.dst = tpl
	}
	rgbCount := 0
	for _, tpl := range []*template.Template{t.redTpl, t.greenTpl, t.blueTpl} {
		if tpl != nil {
			rgbCount++
		}
	}
	if rgbCount != 0 && rgbCount != 3 {
		return nil, fmt.Errorf("red_template, green_template and blue_template must be set together")
	}
	// HA filters the set so a plain on/off or brightness mode never sits
	// next to a real color mode, and exposes the three channels as hs.
	if t.colorTempTpl != nil {
		l.modes = append(l.modes, "color_temp")
	}
	if rgbCount == 3 {
		l.modes = append(l.modes, "hs")
	}
	if len(l.modes) == 0 {
		if t.brightnessTpl != nil {
			l.modes = []string{"brightness"}
		} else {
			l.modes = []string{"onoff"}
		}
	}
	l.effectList = cfg.Strings("effect_list")
	l.features |= featFlash | featTransition
	// Without a state_template the topic cannot tell us the state.
	l.optimistic = cfg.Bool("optimistic", false) || !cfg.Has("state_topic") || t.stateTpl == nil
	if cfg.Has("state_topic") {
		l.e.subscribe(cfg.Str("state_topic"), func(m Message) { t.onState(l, m) })
	}
	return t, nil
}

// render evaluates one state template, reporting a failure once and
// treating an empty result as "not in this message".
func (t *templateLight) render(l *light, tpl *template.Template, m Message) (string, bool) {
	if tpl == nil {
		return "", false
	}
	s, err := l.e.render(tpl, m.Payload)
	if err != nil {
		l.e.d.log.Warn("light template failed", "entity_id", l.e.entityID, "err", err)
		return "", false
	}
	s = strings.TrimSpace(s)
	return s, s != "" && s != "None"
}

func (t *templateLight) channel(l *light, tpl *template.Template, m Message) (int, bool) {
	s, ok := t.render(l, tpl, m)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 255 {
		l.e.d.log.Warn("light template value out of range", "entity_id", l.e.entityID, "value", s)
		return 0, false
	}
	return int(f), true
}

func (t *templateLight) onState(l *light, m Message) {
	e := l.e
	// All templates are rendered before the lock is taken: rendering
	// reads entity names from the core.
	var (
		state                        string
		haveState, reset             bool
		brightness, red, green, blue int
		haveBrightness, haveRGB      bool
		colorTemp                    int
		haveColorTemp                bool
		effect                       string
		haveEffect                   bool
		rawState                     string
	)
	if tpl := t.stateTpl; tpl != nil {
		s, err := e.render(tpl, m.Payload)
		if err != nil {
			e.d.log.Warn("light template failed", "entity_id", e.entityID, "err", err)
		} else {
			rawState = strings.TrimSpace(s)
			switch rawState {
			case core.StateOn, core.StateOff:
				state, haveState = rawState, true
			case "None":
				reset = true
			case "":
			default:
				e.d.log.Warn("invalid light state value received", "entity_id", e.entityID, "value", rawState)
			}
		}
	}
	brightness, haveBrightness = t.channel(l, t.brightnessTpl, m)
	if s, ok := t.render(l, t.colorTempTpl, m); ok {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 {
			e.d.log.Warn("invalid color_temp value received", "entity_id", e.entityID, "value", s)
		} else {
			colorTemp, haveColorTemp = int(f), true
			if t.kelvin {
				colorTemp = kelvinToMired(f)
			}
		}
	}
	if t.redTpl != nil {
		var okR, okG, okB bool
		red, okR = t.channel(l, t.redTpl, m)
		green, okG = t.channel(l, t.greenTpl, m)
		blue, okB = t.channel(l, t.blueTpl, m)
		haveRGB = okR && okG && okB
	}
	effect, haveEffect = t.render(l, t.effectTpl, m)

	e.mu.Lock()
	switch {
	case haveState:
		e.state = state
	case reset:
		e.state = ""
	}
	if haveBrightness && l.supportsBrightness() {
		l.st.brightness, l.st.hasBrightness = brightness, true
	}
	if haveColorTemp && l.supports("color_temp") {
		l.st.colorTemp, l.st.colorMode = colorTemp, "color_temp"
	}
	if haveRGB && l.supports("hs") {
		h, s := rgbToHS(float64(red), float64(green), float64(blue))
		l.st.hs, l.st.colorMode = [2]float64{h, s}, "hs"
	}
	if haveEffect && len(l.effectList) > 0 {
		l.st.effect = effect
	}
	if l.st.colorMode == "" && len(l.modes) == 1 {
		l.st.colorMode = l.modes[0]
	}
	e.mu.Unlock()
	e.write(false)
}

func (t *templateLight) turnOn(ctx context.Context, l *light, p turnOnParams) error {
	vars := map[string]any{"state": true}
	if p.brightness != nil && l.supportsBrightness() {
		vars["brightness"] = *p.brightness
	}
	if p.colorTemp != nil {
		if t.kelvin {
			vars["color_temp"] = miredToKelvin(float64(*p.colorTemp))
		} else {
			vars["color_temp"] = *p.colorTemp
		}
	}
	if p.hs != nil {
		// A device that reports brightness itself must get the full-value
		// color; otherwise the brightness is baked into the channels.
		b := 255
		if t.brightnessTpl == nil {
			l.e.mu.Lock()
			if p.brightness != nil {
				b = *p.brightness
			} else if l.st.hasBrightness {
				b = l.st.brightness
			}
			l.e.mu.Unlock()
		}
		rgb := hsToRGB(p.hs[0], p.hs[1])
		for i := range rgb {
			rgb[i] = int(math.RoundToEven(float64(rgb[i]) * float64(b) / 255))
		}
		vars["red"], vars["green"], vars["blue"] = rgb[0], rgb[1], rgb[2]
		vars["hue"], vars["sat"] = p.hs[0], p.hs[1]
	}
	if p.effect != "" {
		vars["effect"] = p.effect
	}
	if p.flash != "" {
		vars["flash"] = p.flash
	}
	if p.transition != nil {
		vars["transition"] = *p.transition
	}
	if err := t.publish(l, t.onTpl, vars); err != nil {
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

func (t *templateLight) turnOff(ctx context.Context, l *light) error {
	if err := t.publish(l, t.offTpl, map[string]any{"state": false}); err != nil {
		return err
	}
	if l.optimistic {
		l.e.setState(core.StateOff, ctx)
	}
	return nil
}

func (t *templateLight) publish(l *light, tpl *template.Template, vars map[string]any) error {
	vars["entity_id"] = l.e.entityID
	vars["name"] = l.e.d.core.FriendlyName(l.e.entityID)
	payload, err := tpl.Render(vars, nil)
	if err != nil {
		return err
	}
	return l.e.publish(t.cmdTopic, payload)
}
