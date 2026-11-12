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

func init() {
	components["climate"] = newClimate
}

// --- helpers shared by climate, fan, humidifier and water_heater ---

// hvacProp is the command side of one controllable property. HA's MQTT
// climate-like platforms all follow the same scheme: an optional state topic
// with a template, and an optional command topic with a command_template
// that is rendered with "value". A property whose state topic is missing (or
// when optimistic is set) is "assumed": the entity records the commanded
// value itself because the device never reports it back.
type hvacProp struct {
	e       *entity
	topic   string
	tpl     *template.Template
	assumed bool
}

func newHVACProp(e *entity, cmdTopic, cmdTpl, stateTopic string) (*hvacProp, error) {
	tpl, err := e.cfg.Template(cmdTpl)
	if err != nil {
		return nil, err
	}
	return &hvacProp{e: e, topic: e.cfg.Str(cmdTopic), tpl: tpl, assumed: e.cfg.Bool("optimistic", false) || !e.cfg.Has(stateTopic)}, nil
}

func (p *hvacProp) enabled() bool { return p.topic != "" }

// send publishes value through the command template, or plain when the
// property has none.
func (p *hvacProp) send(value any, plain string) error {
	payload := plain
	if p.tpl != nil {
		var err error
		payload, err = p.e.renderCommand(p.tpl, value)
		if err != nil {
			return err
		}
	}
	return p.e.publish(p.topic, payload)
}

// hvacWatch subscribes to a state topic (when configured) and hands the
// payload, after the optional template, to apply. Empty results are dropped
// as in HA, where a template that renders nothing means "no update".
func hvacWatch(e *entity, topicKey, tplKey string, apply func(payload string)) error {
	if !e.cfg.Has(topicKey) {
		return nil
	}
	tpl, err := e.cfg.Template(tplKey)
	if err != nil {
		return err
	}
	e.subscribe(e.cfg.Str(topicKey), func(m Message) {
		payload := string(m.Payload)
		if tpl != nil {
			payload, err = e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("template failed", "entity_id", e.entityID, "option", tplKey, "err", err)
				return
			}
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			return
		}
		apply(payload)
	})
	return nil
}

// hvacSet changes component fields under the entity lock, which also guards
// the attribute callback, and then writes the new state.
func hvacSet(e *entity, ctx context.Context, fn func()) {
	e.mu.Lock()
	fn()
	e.mu.Unlock()
	e.write(false, ctx)
}

// hvacNumber parses a reported number; NaN and infinities are rejected
// because they would break the JSON state.
func hvacNumber(payload string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(payload), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func hvacIn(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hvacRound rounds like HA's show_temp: to the entity precision, halves to
// even as Python's round() does.
func hvacRound(v, precision float64) float64 {
	return math.RoundToEven(v/precision) * precision
}

func hvacShow(v *float64, precision float64) any {
	if v == nil {
		return nil
	}
	return math.Round(hvacRound(*v, precision)*100) / 100
}

func hvacCelsiusTo(unit string, c float64) float64 {
	if unit == "F" {
		return c*9/5 + 32
	}
	return c
}

func hvacFahrenheitTo(unit string, f float64) float64 {
	if unit == "C" {
		return (f - 32) * 5 / 9
	}
	return f
}

// hvacUnitPrecision reads temperature_unit and precision. HA defaults the
// precision to tenths for Celsius and whole degrees otherwise.
func hvacUnitPrecision(cfg Config) (string, float64, error) {
	unit := cfg.StrDef("temperature_unit", "C")
	if unit != "C" && unit != "F" {
		return "", 0, fmt.Errorf("temperature_unit must be C or F, got %q", unit)
	}
	precision := 0.1
	if unit == "F" {
		precision = 1
	}
	if cfg.Has("precision") {
		precision = cfg.Float("precision", 0)
		if precision != 0.1 && precision != 0.5 && precision != 1 {
			return "", 0, fmt.Errorf("precision must be 0.1, 0.5 or 1.0")
		}
	}
	return unit, precision, nil
}

// hvacTemperature extracts a numeric service field; absent is not an error.
func hvacTemperature(data map[string]any, key string) (float64, bool, error) {
	v, ok := data[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	f, ok := num(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, true, fmt.Errorf("%s must be a number", key)
	}
	return f, true, nil
}

// --- climate ---

const (
	climateTargetTemperature      = 1
	climateTargetTemperatureRange = 2
	climateTargetHumidity         = 4
	climateFanMode                = 8
	climatePresetMode             = 16
	climateSwingMode              = 32
	climateTurnOff                = 128
	climateTurnOn                 = 256
)

var (
	climateHVACModes   = []string{"off", "heat", "cool", "heat_cool", "auto", "dry", "fan_only"}
	climateHVACActions = []string{"off", "heating", "cooling", "drying", "idle", "fan", "defrosting", "preheating"}
)

type mqttClimate struct {
	e *entity

	modes, fanModes, presetModes, swingModes []string
	minTemp, maxTemp, tempStep, precision    float64
	minHum, maxHum                           float64
	payloadOn, payloadOff                    string
	features                                 int

	mode, temp, tempHi, tempLo, hum, fan, preset, swing, power *hvacProp

	// Runtime values, guarded by e.mu. The hvac mode itself is e.state.
	target, targetHi, targetLo, current, currentHum, humidity *float64
	fanMode, presetMode, swingMode, action, lastMode          string
}

func newClimate(e *entity) (component, error) {
	cfg := e.cfg
	c := &mqttClimate{e: e}
	var err error
	c.modes = hvacListOption(cfg, "modes", []string{"auto", "off", "cool", "heat", "dry", "fan_only"})
	for _, m := range c.modes {
		if !hvacIn(climateHVACModes, m) {
			return nil, fmt.Errorf("modes: %q is not an HVAC mode", m)
		}
	}
	c.fanModes = hvacListOption(cfg, "fan_modes", []string{"auto", "low", "medium", "high"})
	c.swingModes = hvacListOption(cfg, "swing_modes", []string{"on", "off"})
	c.presetModes = cfg.Strings("preset_modes")
	unit, precision, err := hvacUnitPrecision(cfg)
	if err != nil {
		return nil, err
	}
	c.precision = precision
	c.minTemp = cfg.Float("min_temp", hvacCelsiusTo(unit, 7))
	c.maxTemp = cfg.Float("max_temp", hvacCelsiusTo(unit, 35))
	if c.minTemp >= c.maxTemp {
		return nil, fmt.Errorf("min_temp must be below max_temp")
	}
	c.tempStep = cfg.Float("temp_step", 1)
	if c.tempStep <= 0 {
		return nil, fmt.Errorf("temp_step must be positive")
	}
	c.minHum = cfg.Float("min_humidity", 30)
	c.maxHum = cfg.Float("max_humidity", 99)
	if c.minHum >= c.maxHum {
		return nil, fmt.Errorf("min_humidity must be below max_humidity")
	}
	c.payloadOn = cfg.StrDef("payload_on", "ON")
	c.payloadOff = cfg.StrDef("payload_off", "OFF")

	for _, p := range []struct {
		dst                 **hvacProp
		cmd, cmdTpl, stateT string
	}{
		{&c.mode, "mode_command_topic", "mode_command_template", "mode_state_topic"},
		{&c.temp, "temperature_command_topic", "temperature_command_template", "temperature_state_topic"},
		{&c.tempHi, "temperature_high_command_topic", "temperature_high_command_template", "temperature_high_state_topic"},
		{&c.tempLo, "temperature_low_command_topic", "temperature_low_command_template", "temperature_low_state_topic"},
		{&c.hum, "target_humidity_command_topic", "target_humidity_command_template", "target_humidity_state_topic"},
		{&c.fan, "fan_mode_command_topic", "fan_mode_command_template", "fan_mode_state_topic"},
		{&c.preset, "preset_mode_command_topic", "preset_mode_command_template", "preset_mode_state_topic"},
		{&c.swing, "swing_mode_command_topic", "swing_mode_command_template", "swing_mode_state_topic"},
		{&c.power, "power_command_topic", "power_command_template", "power_state_topic"},
	} {
		if *p.dst, err = newHVACProp(e, p.cmd, p.cmdTpl, p.stateT); err != nil {
			return nil, err
		}
	}
	if c.preset.enabled() && len(c.presetModes) == 0 {
		return nil, errMissing("preset_modes")
	}

	if c.temp.enabled() {
		c.features |= climateTargetTemperature
	}
	if c.tempHi.enabled() && c.tempLo.enabled() {
		c.features |= climateTargetTemperatureRange
	}
	if c.hum.enabled() {
		c.features |= climateTargetHumidity
	}
	if c.fan.enabled() {
		c.features |= climateFanMode
	}
	if c.preset.enabled() {
		c.features |= climatePresetMode
	}
	if c.swing.enabled() {
		c.features |= climateSwingMode
	}
	if c.power.enabled() || (hvacIn(c.modes, "off") && len(c.modes) > 1 && c.mode.enabled()) {
		c.features |= climateTurnOff | climateTurnOn
	}

	// A property the device never reports starts at the value HA assumes, so
	// the card is usable before the first command.
	if c.mode.assumed && hvacIn(c.modes, "off") {
		e.state = "off"
	}
	if c.temp.assumed && c.features&climateTargetTemperature != 0 {
		v := cfg.Float("initial", 21)
		c.target = &v
	}
	if c.fan.assumed && c.features&climateFanMode != 0 && hvacIn(c.fanModes, "low") {
		c.fanMode = "low"
	}
	if c.swing.assumed && c.features&climateSwingMode != 0 && hvacIn(c.swingModes, "off") {
		c.swingMode = "off"
	}
	if c.preset.assumed && c.features&climatePresetMode != 0 {
		c.presetMode = "none"
	}

	e.attrs = c.attributes
	if err := c.watch(); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, c)
	e.write(false)
	return c, nil
}

// hvacListOption returns the configured string list, or def when the option
// is absent. An empty list is kept: the device then offers no choices.
func hvacListOption(cfg Config, key string, def []string) []string {
	if !cfg.Has(key) {
		return def
	}
	return cfg.Strings(key)
}

func (c *mqttClimate) stop() {}

// attributes is called by entity.write with e.mu held.
func (c *mqttClimate) attributes() map[string]any {
	a := map[string]any{
		"hvac_modes":          c.modes,
		"min_temp":            hvacShow(&c.minTemp, c.precision),
		"max_temp":            hvacShow(&c.maxTemp, c.precision),
		"target_temp_step":    c.tempStep,
		"current_temperature": hvacShow(c.current, c.precision),
		"supported_features":  c.features,
	}
	if c.features&climateTargetTemperature != 0 {
		a["temperature"] = hvacShow(c.target, c.precision)
	}
	if c.features&climateTargetTemperatureRange != 0 {
		a["target_temp_high"] = hvacShow(c.targetHi, c.precision)
		a["target_temp_low"] = hvacShow(c.targetLo, c.precision)
	}
	if c.currentHum != nil {
		a["current_humidity"] = *c.currentHum
	}
	if c.features&climateTargetHumidity != 0 {
		a["humidity"] = hvacPtr(c.humidity)
		a["min_humidity"] = c.minHum
		a["max_humidity"] = c.maxHum
	}
	if c.features&climateFanMode != 0 {
		a["fan_mode"] = hvacStr(c.fanMode)
		a["fan_modes"] = c.fanModes
	}
	if c.features&climatePresetMode != 0 {
		a["preset_mode"] = hvacStr(c.presetMode)
		a["preset_modes"] = c.presetModes
	}
	if c.features&climateSwingMode != 0 {
		a["swing_mode"] = hvacStr(c.swingMode)
		a["swing_modes"] = c.swingModes
	}
	if c.action != "" {
		a["hvac_action"] = c.action
	}
	return a
}

func hvacPtr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func hvacStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// watch wires every state topic of the climate to its runtime value.
func (c *mqttClimate) watch() error {
	e := c.e
	ctx := context.Background()
	warn := func(what, payload string) {
		e.d.log.Warn("climate payload rejected", "entity_id", e.entityID, "property", what, "payload", payload)
	}
	// number handles the "None" reset and numeric parsing common to all
	// numeric properties.
	number := func(what string, dst **float64) func(string) {
		return func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { *dst = nil })
				return
			}
			f, ok := hvacNumber(payload)
			if !ok {
				warn(what, payload)
				return
			}
			hvacSet(e, ctx, func() { *dst = &f })
		}
	}
	choice := func(what string, list []string, dst *string) func(string) {
		return func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { *dst = "" })
				return
			}
			if !hvacIn(list, payload) {
				warn(what, payload)
				return
			}
			hvacSet(e, ctx, func() { *dst = payload })
		}
	}
	for _, w := range []struct {
		topic, tpl string
		apply      func(string)
	}{
		{"mode_state_topic", "mode_state_template", func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { e.state = "" })
				return
			}
			if !hvacIn(c.modes, payload) {
				warn("mode", payload)
				return
			}
			hvacSet(e, ctx, func() {
				e.state = payload
				if payload != "off" {
					c.lastMode = payload
				}
			})
		}},
		{"temperature_state_topic", "temperature_state_template", number("temperature", &c.target)},
		{"temperature_high_state_topic", "temperature_high_state_template", number("temperature_high", &c.targetHi)},
		{"temperature_low_state_topic", "temperature_low_state_template", number("temperature_low", &c.targetLo)},
		{"current_temperature_topic", "current_temperature_template", number("current_temperature", &c.current)},
		{"current_humidity_topic", "current_humidity_template", number("current_humidity", &c.currentHum)},
		{"target_humidity_state_topic", "target_humidity_state_template", number("target_humidity", &c.humidity)},
		{"fan_mode_state_topic", "fan_mode_state_template", choice("fan_mode", c.fanModes, &c.fanMode)},
		{"swing_mode_state_topic", "swing_mode_state_template", choice("swing_mode", c.swingModes, &c.swingMode)},
		{"action_topic", "action_template", choice("action", climateHVACActions, &c.action)},
		{"preset_mode_state_topic", "preset_mode_value_template", func(payload string) {
			// "none" and the None payload both mean no preset is active.
			if payload == "none" || payload == "None" {
				hvacSet(e, ctx, func() { c.presetMode = "none" })
				return
			}
			if !hvacIn(c.presetModes, payload) {
				warn("preset_mode", payload)
				return
			}
			hvacSet(e, ctx, func() { c.presetMode = payload })
		}},
	} {
		if err := hvacWatch(e, w.topic, w.tpl, w.apply); err != nil {
			return err
		}
	}
	return nil
}

// hvacRequire reports a missing command topic as a service error.
func hvacRequire(p *hvacProp, name, topicOption string) error {
	if !p.enabled() {
		return fmt.Errorf("%s is not supported: %s is not configured", name, topicOption)
	}
	return nil
}

func (c *mqttClimate) checkRange(key string, v float64) error {
	if v < c.minTemp || v > c.maxTemp {
		return fmt.Errorf("%s %s is outside %s..%s", key, template.PyStr(v), template.PyStr(c.minTemp), template.PyStr(c.maxTemp))
	}
	return nil
}

func (c *mqttClimate) SetTemperature(ctx context.Context, data map[string]any) error {
	mode := ""
	if v, ok := data["hvac_mode"]; ok && v != nil {
		mode = fmt.Sprint(v)
		if err := c.checkMode(mode); err != nil {
			return err
		}
	}
	// Validate everything before the first publish so a bad field does not
	// leave the device half updated.
	type change struct {
		prop *hvacProp
		dst  **float64
		val  float64
	}
	var changes []change
	for _, f := range []struct {
		key    string
		prop   *hvacProp
		option string
		dst    **float64
	}{
		{"temperature", c.temp, "temperature_command_topic", &c.target},
		{"target_temp_high", c.tempHi, "temperature_high_command_topic", &c.targetHi},
		{"target_temp_low", c.tempLo, "temperature_low_command_topic", &c.targetLo},
	} {
		v, ok, err := hvacTemperature(data, f.key)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := hvacRequire(f.prop, f.key, f.option); err != nil {
			return err
		}
		if err := c.checkRange(f.key, v); err != nil {
			return err
		}
		changes = append(changes, change{f.prop, f.dst, v})
	}
	if len(changes) == 0 && mode == "" {
		return fmt.Errorf("one of temperature, target_temp_high, target_temp_low or hvac_mode is required")
	}
	if mode != "" {
		if err := c.SetHVACMode(ctx, mode); err != nil {
			return err
		}
	}
	for _, ch := range changes {
		if err := ch.prop.send(ch.val, template.PyStr(ch.val)); err != nil {
			return err
		}
		if ch.prop.assumed {
			hvacSet(c.e, ctx, func() { v := ch.val; *ch.dst = &v })
		}
	}
	return nil
}

func (c *mqttClimate) checkMode(mode string) error {
	if !hvacIn(c.modes, mode) {
		return fmt.Errorf("hvac mode %q is not one of %s", mode, strings.Join(c.modes, ", "))
	}
	return nil
}

func (c *mqttClimate) SetHVACMode(ctx context.Context, mode string) error {
	if err := c.checkMode(mode); err != nil {
		return err
	}
	if err := hvacRequire(c.mode, "hvac_mode", "mode_command_topic"); err != nil {
		return err
	}
	if err := c.mode.send(mode, mode); err != nil {
		return err
	}
	if c.mode.assumed {
		c.recordMode(ctx, mode)
	}
	return nil
}

func (c *mqttClimate) recordMode(ctx context.Context, mode string) {
	hvacSet(c.e, ctx, func() {
		c.e.state = mode
		if mode != "off" {
			c.lastMode = mode
		}
	})
}

// onMode is the mode turn_on restores: the last active mode, else the first
// of HA's defaults the device offers.
func (c *mqttClimate) onMode() (string, error) {
	c.e.mu.Lock()
	last := c.lastMode
	c.e.mu.Unlock()
	if last != "" {
		return last, nil
	}
	for _, m := range []string{"heat_cool", "heat", "cool"} {
		if hvacIn(c.modes, m) {
			return m, nil
		}
	}
	return "", fmt.Errorf("turn_on is not supported: no active mode is known")
}

func (c *mqttClimate) TurnOn(ctx context.Context, _ map[string]any) error {
	if c.power.enabled() {
		if err := c.power.send(c.payloadOn, c.payloadOn); err != nil {
			return err
		}
		if c.mode.assumed {
			if m, err := c.onMode(); err == nil {
				c.recordMode(ctx, m)
			}
		}
		return nil
	}
	m, err := c.onMode()
	if err != nil {
		return err
	}
	return c.SetHVACMode(ctx, m)
}

func (c *mqttClimate) TurnOff(ctx context.Context, _ map[string]any) error {
	if c.power.enabled() {
		if err := c.power.send(c.payloadOff, c.payloadOff); err != nil {
			return err
		}
		if c.mode.assumed && hvacIn(c.modes, "off") {
			c.recordMode(ctx, "off")
		}
		return nil
	}
	return c.SetHVACMode(ctx, "off")
}

// setChoice is the common path of fan, preset and swing mode commands.
func (c *mqttClimate) setChoice(ctx context.Context, p *hvacProp, name, option string, list []string, value string, dst *string) error {
	if err := hvacRequire(p, name, option); err != nil {
		return err
	}
	if !hvacIn(list, value) {
		return fmt.Errorf("%s %q is not one of %s", name, value, strings.Join(list, ", "))
	}
	if err := p.send(value, value); err != nil {
		return err
	}
	if p.assumed {
		hvacSet(c.e, ctx, func() { *dst = value })
	}
	return nil
}

func (c *mqttClimate) SetFanMode(ctx context.Context, mode string) error {
	return c.setChoice(ctx, c.fan, "fan_mode", "fan_mode_command_topic", c.fanModes, mode, &c.fanMode)
}

func (c *mqttClimate) SetPresetMode(ctx context.Context, mode string) error {
	return c.setChoice(ctx, c.preset, "preset_mode", "preset_mode_command_topic", c.presetModes, mode, &c.presetMode)
}

func (c *mqttClimate) SetSwingMode(ctx context.Context, mode string) error {
	return c.setChoice(ctx, c.swing, "swing_mode", "swing_mode_command_topic", c.swingModes, mode, &c.swingMode)
}

func (c *mqttClimate) SetHumidity(ctx context.Context, humidity int) error {
	if err := hvacRequire(c.hum, "humidity", "target_humidity_command_topic"); err != nil {
		return err
	}
	if h := float64(humidity); h < c.minHum || h > c.maxHum {
		return fmt.Errorf("humidity %d is outside %s..%s", humidity, template.PyStr(c.minHum), template.PyStr(c.maxHum))
	}
	if err := c.hum.send(humidity, strconv.Itoa(humidity)); err != nil {
		return err
	}
	if c.hum.assumed {
		hvacSet(c.e, ctx, func() { v := float64(humidity); c.humidity = &v })
	}
	return nil
}

var (
	_ core.OnOff             = (*mqttClimate)(nil)
	_ core.Climate           = (*mqttClimate)(nil)
	_ core.ClimateFanMode    = (*mqttClimate)(nil)
	_ core.ClimatePresetMode = (*mqttClimate)(nil)
	_ core.ClimateSwingMode  = (*mqttClimate)(nil)
	_ core.ClimateHumidity   = (*mqttClimate)(nil)
)
