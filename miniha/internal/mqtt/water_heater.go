/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"
	"strings"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["water_heater"] = newWaterHeater
}

const (
	waterHeaterTargetTemperature = 1
	waterHeaterOperationMode     = 2
	waterHeaterOnOff             = 8
)

// The operation modes HA's water_heater platform knows.
var waterHeaterAllModes = []string{"off", "eco", "electric", "gas", "heat_pump", "high_demand", "performance"}

type mqttWaterHeater struct {
	e *entity

	modes                 []string
	minTemp, maxTemp      float64
	precision             float64
	payloadOn, payloadOff string
	features              int
	mode, temp, power     *hvacProp
	target, current       *float64 // guarded by e.mu
}

func newWaterHeater(e *entity) (component, error) {
	cfg := e.cfg
	w := &mqttWaterHeater{e: e, payloadOn: cfg.StrDef("payload_on", "ON"), payloadOff: cfg.StrDef("payload_off", "OFF")}
	w.modes = hvacListOption(cfg, "modes", waterHeaterAllModes)
	for _, m := range w.modes {
		if !hvacIn(waterHeaterAllModes, m) {
			return nil, fmt.Errorf("modes: %q is not a water heater operation mode", m)
		}
	}
	unit, precision, err := hvacUnitPrecision(cfg)
	if err != nil {
		return nil, err
	}
	w.precision = precision
	// HA's defaults are 110 and 140 degrees Fahrenheit.
	w.minTemp = cfg.Float("min_temp", hvacFahrenheitTo(unit, 110))
	w.maxTemp = cfg.Float("max_temp", hvacFahrenheitTo(unit, 140))
	if w.minTemp >= w.maxTemp {
		return nil, fmt.Errorf("min_temp must be below max_temp")
	}
	for _, p := range []struct {
		dst                 **hvacProp
		cmd, cmdTpl, stateT string
	}{
		{&w.mode, "mode_command_topic", "mode_command_template", "mode_state_topic"},
		{&w.temp, "temperature_command_topic", "temperature_command_template", "temperature_state_topic"},
		{&w.power, "power_command_topic", "power_command_template", "power_state_topic"},
	} {
		if *p.dst, err = newHVACProp(e, p.cmd, p.cmdTpl, p.stateT); err != nil {
			return nil, err
		}
	}
	if w.temp.enabled() {
		w.features |= waterHeaterTargetTemperature
	}
	if w.mode.enabled() {
		w.features |= waterHeaterOperationMode
	}
	if w.power.enabled() {
		w.features |= waterHeaterOnOff
	}
	if w.temp.assumed && w.temp.enabled() && cfg.Has("initial") {
		v := cfg.Float("initial", 0)
		w.target = &v
	}
	e.attrs = w.attributes
	if err := w.watch(); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, w)
	e.write(false)
	return w, nil
}

func (w *mqttWaterHeater) stop() {}

// attributes is called by entity.write with e.mu held; the state is the
// current operation mode.
func (w *mqttWaterHeater) attributes() map[string]any {
	a := map[string]any{
		"current_temperature": hvacShow(w.current, w.precision),
		"temperature":         hvacShow(w.target, w.precision),
		"target_temp_high":    nil,
		"target_temp_low":     nil,
		"min_temp":            hvacShow(&w.minTemp, w.precision),
		"max_temp":            hvacShow(&w.maxTemp, w.precision),
		"supported_features":  w.features,
	}
	if w.features&waterHeaterOperationMode != 0 {
		a["operation_list"] = w.modes
		a["operation_mode"] = hvacStr(w.e.state)
	}
	return a
}

func (w *mqttWaterHeater) watch() error {
	e := w.e
	ctx := context.Background()
	warn := func(what, payload string) {
		e.d.log.Warn("water heater payload rejected", "entity_id", e.entityID, "property", what, "payload", payload)
	}
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
	for _, s := range []struct {
		topic, tpl string
		apply      func(string)
	}{
		{"mode_state_topic", "mode_state_template", func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { e.state = "" })
				return
			}
			if !hvacIn(w.modes, payload) {
				warn("mode", payload)
				return
			}
			hvacSet(e, ctx, func() { e.state = payload })
		}},
		{"temperature_state_topic", "temperature_state_template", number("temperature", &w.target)},
		{"current_temperature_topic", "current_temperature_template", number("current_temperature", &w.current)},
	} {
		if err := hvacWatch(e, s.topic, s.tpl, s.apply); err != nil {
			return err
		}
	}
	return nil
}

func (w *mqttWaterHeater) SetOperationMode(ctx context.Context, mode string) error {
	if err := hvacRequire(w.mode, "operation_mode", "mode_command_topic"); err != nil {
		return err
	}
	if !hvacIn(w.modes, mode) {
		return fmt.Errorf("operation_mode %q is not one of %s", mode, strings.Join(w.modes, ", "))
	}
	if err := w.mode.send(mode, mode); err != nil {
		return err
	}
	if w.mode.assumed {
		hvacSet(w.e, ctx, func() { w.e.state = mode })
	}
	return nil
}

func (w *mqttWaterHeater) SetTemperature(ctx context.Context, data map[string]any) error {
	t, ok, err := hvacTemperature(data, "temperature")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("temperature is required")
	}
	if err := hvacRequire(w.temp, "temperature", "temperature_command_topic"); err != nil {
		return err
	}
	if t < w.minTemp || t > w.maxTemp {
		return fmt.Errorf("temperature %s is outside %s..%s", template.PyStr(t), template.PyStr(w.minTemp), template.PyStr(w.maxTemp))
	}
	mode := ""
	if v, ok := data["operation_mode"]; ok && v != nil {
		mode = fmt.Sprint(v)
		if !hvacIn(w.modes, mode) {
			return fmt.Errorf("operation_mode %q is not one of %s", mode, strings.Join(w.modes, ", "))
		}
		if err := hvacRequire(w.mode, "operation_mode", "mode_command_topic"); err != nil {
			return err
		}
	}
	if mode != "" {
		if err := w.SetOperationMode(ctx, mode); err != nil {
			return err
		}
	}
	if err := w.temp.send(t, template.PyStr(t)); err != nil {
		return err
	}
	if w.temp.assumed {
		hvacSet(w.e, ctx, func() { w.target = &t })
	}
	return nil
}

func (w *mqttWaterHeater) TurnOn(_ context.Context, _ map[string]any) error {
	if err := hvacRequire(w.power, "turn_on", "power_command_topic"); err != nil {
		return err
	}
	return w.power.send(w.payloadOn, w.payloadOn)
}

func (w *mqttWaterHeater) TurnOff(ctx context.Context, _ map[string]any) error {
	if err := hvacRequire(w.power, "turn_off", "power_command_topic"); err != nil {
		return err
	}
	if err := w.power.send(w.payloadOff, w.payloadOff); err != nil {
		return err
	}
	if w.mode.assumed && hvacIn(w.modes, "off") {
		hvacSet(w.e, ctx, func() { w.e.state = "off" })
	}
	return nil
}

var (
	_ core.OnOff       = (*mqttWaterHeater)(nil)
	_ core.WaterHeater = (*mqttWaterHeater)(nil)
)
