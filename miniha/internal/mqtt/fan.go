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
)

func init() {
	components["fan"] = newFan
}

const (
	fanSetSpeed   = 1
	fanOscillate  = 2
	fanDirection  = 4
	fanPresetMode = 8
	fanTurnOff    = 16
	fanTurnOn     = 32
)

type mqttFan struct {
	e *entity

	payloadOn, payloadOff       string
	payloadOscOn, payloadOscOff string
	resetPercentage             string
	resetPreset                 string
	speedMin, speedMax          int
	presetModes                 []string
	features                    int

	power  *hvacProp // on/off command; assumed when no state_topic
	pct    *hvacProp
	preset *hvacProp
	osc    *hvacProp
	dir    *hvacProp

	// Runtime values, guarded by e.mu. The on/off state is e.state.
	percentage  *int
	presetMode  string
	oscillating *bool
	direction   string
}

func newFan(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	f := &mqttFan{
		e:               e,
		payloadOn:       cfg.StrDef("payload_on", "ON"),
		payloadOff:      cfg.StrDef("payload_off", "OFF"),
		payloadOscOn:    cfg.StrDef("payload_oscillation_on", "oscillate_on"),
		payloadOscOff:   cfg.StrDef("payload_oscillation_off", "oscillate_off"),
		resetPercentage: cfg.StrDef("payload_reset_percentage", "None"),
		resetPreset:     cfg.StrDef("payload_reset_preset_mode", "None"),
		speedMin:        cfg.Int("speed_range_min", 1),
		speedMax:        cfg.Int("speed_range_max", 100),
		presetModes:     cfg.Strings("preset_modes"),
		features:        fanTurnOff | fanTurnOn,
	}
	if f.speedMin < 1 || f.speedMax <= f.speedMin {
		return nil, fmt.Errorf("speed_range_min must be at least 1 and below speed_range_max")
	}
	var err error
	if f.power, err = newHVACProp(e, "command_topic", "command_template", "state_topic"); err != nil {
		return nil, err
	}
	for _, p := range []struct {
		dst                 **hvacProp
		cmd, cmdTpl, stateT string
	}{
		{&f.pct, "percentage_command_topic", "percentage_command_template", "percentage_state_topic"},
		{&f.preset, "preset_mode_command_topic", "preset_mode_command_template", "preset_mode_state_topic"},
		{&f.osc, "oscillation_command_topic", "oscillation_command_template", "oscillation_state_topic"},
		{&f.dir, "direction_command_topic", "direction_command_template", "direction_state_topic"},
	} {
		if *p.dst, err = newHVACProp(e, p.cmd, p.cmdTpl, p.stateT); err != nil {
			return nil, err
		}
	}
	if f.pct.enabled() {
		f.features |= fanSetSpeed
	}
	if f.osc.enabled() {
		f.features |= fanOscillate
	}
	if f.dir.enabled() {
		f.features |= fanDirection
	}
	if f.preset.enabled() {
		if len(f.presetModes) == 0 {
			return nil, errMissing("preset_modes")
		}
		f.features |= fanPresetMode
	}

	if f.power.assumed {
		e.state = core.StateOff
	}
	e.attrs = f.attributes
	if err := f.watch(); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, f)
	e.write(false)
	return f, nil
}

func (f *mqttFan) stop() {}

// speedCount is the number of discrete speeds the device offers, as HA
// computes it for the percentage step.
func (f *mqttFan) speedCount() int {
	return min(f.speedMax-f.speedMin+1, 100)
}

// toDevice and toPercentage follow HA's percentage_to_ranged_value (ceiling)
// and ranged_value_to_percentage (floor) so a value round-trips.
func (f *mqttFan) toDevice(percentage int) int {
	states := f.speedMax - f.speedMin + 1
	return int(math.Ceil(float64(states)*float64(percentage)/100)) + f.speedMin - 1
}

func (f *mqttFan) toPercentage(value int) int {
	states := f.speedMax - f.speedMin + 1
	return int(math.Floor(float64(value-(f.speedMin-1)) * 100 / float64(states)))
}

// attributes is called by entity.write with e.mu held.
func (f *mqttFan) attributes() map[string]any {
	a := map[string]any{"supported_features": f.features}
	if f.features&fanOscillate != 0 {
		if f.oscillating == nil {
			a["oscillating"] = nil
		} else {
			a["oscillating"] = *f.oscillating
		}
	}
	if f.features&fanSetSpeed != 0 {
		if f.percentage == nil {
			a["percentage"] = nil
		} else {
			a["percentage"] = *f.percentage
		}
		a["percentage_step"] = 100 / float64(f.speedCount())
	}
	if f.features&fanDirection != 0 {
		a["direction"] = hvacStr(f.direction)
	}
	if f.features&fanPresetMode != 0 {
		a["preset_mode"] = hvacStr(f.presetMode)
		a["preset_modes"] = f.presetModes
	}
	return a
}

func (f *mqttFan) watch() error {
	e := f.e
	ctx := context.Background()
	warn := func(what, payload string) {
		e.d.log.Warn("fan payload rejected", "entity_id", e.entityID, "property", what, "payload", payload)
	}
	for _, w := range []struct {
		topic, tpl string
		apply      func(string)
	}{
		{"state_topic", "state_value_template", func(payload string) {
			switch payload {
			case f.payloadOn:
				e.setState(core.StateOn, ctx)
			case f.payloadOff:
				e.setState(core.StateOff, ctx)
			case "None":
				e.setState("", ctx)
			default:
				warn("state", payload)
			}
		}},
		{"percentage_state_topic", "percentage_value_template", func(payload string) {
			if payload == f.resetPercentage {
				hvacSet(e, ctx, func() { f.percentage = nil })
				return
			}
			v, ok := hvacNumber(payload)
			if !ok || v != math.Trunc(v) {
				warn("percentage", payload)
				return
			}
			p := f.toPercentage(int(v))
			if p < 0 || p > 100 {
				warn("percentage", payload)
				return
			}
			hvacSet(e, ctx, func() { f.percentage = &p })
		}},
		{"preset_mode_state_topic", "preset_mode_value_template", func(payload string) {
			if payload == f.resetPreset {
				hvacSet(e, ctx, func() { f.presetMode = "" })
				return
			}
			if !hvacIn(f.presetModes, payload) {
				warn("preset_mode", payload)
				return
			}
			hvacSet(e, ctx, func() { f.presetMode = payload })
		}},
		{"oscillation_state_topic", "oscillation_value_template", func(payload string) {
			switch payload {
			case f.payloadOscOn:
				hvacSet(e, ctx, func() { v := true; f.oscillating = &v })
			case f.payloadOscOff:
				hvacSet(e, ctx, func() { v := false; f.oscillating = &v })
			case "None":
				hvacSet(e, ctx, func() { f.oscillating = nil })
			default:
				warn("oscillation", payload)
			}
		}},
		{"direction_state_topic", "direction_value_template", func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { f.direction = "" })
				return
			}
			hvacSet(e, ctx, func() { f.direction = payload })
		}},
	} {
		if err := hvacWatch(e, w.topic, w.tpl, w.apply); err != nil {
			return err
		}
	}
	return nil
}

// Percentage is what fan.increase_speed and decrease_speed start from.
func (f *mqttFan) Percentage() int {
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	if f.percentage == nil {
		return 0
	}
	return *f.percentage
}

func (f *mqttFan) TurnOn(ctx context.Context, data map[string]any) error {
	// Validate the optional speed and preset before publishing anything.
	percentage, hasPct := 0, false
	if v, ok := data["percentage"]; ok && v != nil {
		p, ok := num(v)
		if !ok || p != math.Trunc(p) || p < 0 || p > 100 {
			return fmt.Errorf("percentage must be an integer between 0 and 100")
		}
		percentage, hasPct = int(p), true
	}
	preset := ""
	if v, ok := data["preset_mode"]; ok && v != nil {
		preset = fmt.Sprint(v)
		if err := f.checkPreset(preset); err != nil {
			return err
		}
	}
	if hasPct {
		if err := hvacRequire(f.pct, "percentage", "percentage_command_topic"); err != nil {
			return err
		}
	}
	if err := f.power.send(f.payloadOn, f.payloadOn); err != nil {
		return err
	}
	if f.power.assumed {
		f.e.setState(core.StateOn, ctx)
	}
	if preset != "" {
		if err := f.SetPresetMode(ctx, preset); err != nil {
			return err
		}
	}
	// HA skips a speed of 0 here: it would only restate the off payload.
	if hasPct && percentage != 0 {
		return f.SetPercentage(ctx, percentage)
	}
	return nil
}

func (f *mqttFan) TurnOff(ctx context.Context, _ map[string]any) error {
	if err := f.power.send(f.payloadOff, f.payloadOff); err != nil {
		return err
	}
	if f.power.assumed {
		f.e.setState(core.StateOff, ctx)
	}
	return nil
}

func (f *mqttFan) SetPercentage(ctx context.Context, percentage int) error {
	if err := hvacRequire(f.pct, "percentage", "percentage_command_topic"); err != nil {
		return err
	}
	if percentage < 0 || percentage > 100 {
		return fmt.Errorf("percentage %d is outside 0..100", percentage)
	}
	device := f.toDevice(percentage)
	if err := f.pct.send(device, strconv.Itoa(device)); err != nil {
		return err
	}
	if f.pct.assumed {
		hvacSet(f.e, ctx, func() { f.percentage = &percentage })
	}
	return nil
}

func (f *mqttFan) checkPreset(mode string) error {
	if !hvacIn(f.presetModes, mode) {
		return fmt.Errorf("preset_mode %q is not one of %s", mode, strings.Join(f.presetModes, ", "))
	}
	return nil
}

func (f *mqttFan) SetPresetMode(ctx context.Context, mode string) error {
	if err := hvacRequire(f.preset, "preset_mode", "preset_mode_command_topic"); err != nil {
		return err
	}
	if err := f.checkPreset(mode); err != nil {
		return err
	}
	if err := f.preset.send(mode, mode); err != nil {
		return err
	}
	if f.preset.assumed {
		hvacSet(f.e, ctx, func() { f.presetMode = mode })
	}
	return nil
}

func (f *mqttFan) Oscillate(ctx context.Context, on bool) error {
	if err := hvacRequire(f.osc, "oscillation", "oscillation_command_topic"); err != nil {
		return err
	}
	payload := f.payloadOscOff
	if on {
		payload = f.payloadOscOn
	}
	if err := f.osc.send(payload, payload); err != nil {
		return err
	}
	if f.osc.assumed {
		hvacSet(f.e, ctx, func() { f.oscillating = &on })
	}
	return nil
}

func (f *mqttFan) SetDirection(ctx context.Context, direction string) error {
	if err := hvacRequire(f.dir, "direction", "direction_command_topic"); err != nil {
		return err
	}
	if direction != "forward" && direction != "reverse" {
		return fmt.Errorf("direction must be forward or reverse, got %q", direction)
	}
	if err := f.dir.send(direction, direction); err != nil {
		return err
	}
	if f.dir.assumed {
		hvacSet(f.e, ctx, func() { f.direction = direction })
	}
	return nil
}

var (
	_ core.OnOff          = (*mqttFan)(nil)
	_ core.FanSpeed       = (*mqttFan)(nil)
	_ core.FanOscillation = (*mqttFan)(nil)
	_ core.FanDirection   = (*mqttFan)(nil)
	_ core.PresetMode     = (*mqttFan)(nil)
)
