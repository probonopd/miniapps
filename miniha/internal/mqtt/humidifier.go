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
	components["humidifier"] = newHumidifier
}

const humidifierModes = 1

var humidifierActions = []string{"off", "humidifying", "drying", "idle"}

type mqttHumidifier struct {
	e *entity

	payloadOn, payloadOff       string
	resetHumidity, resetMode    string
	minHum, maxHum              float64
	modes                       []string
	deviceClass                 string
	features                    int
	power, humidityCmd, modeCmd *hvacProp

	// Runtime values, guarded by e.mu. The on/off state is e.state.
	target, current *float64
	modeValue       string
	action          string
}

func newHumidifier(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	if !cfg.Has("target_humidity_command_topic") {
		return nil, errMissing("target_humidity_command_topic")
	}
	h := &mqttHumidifier{
		e:             e,
		payloadOn:     cfg.StrDef("payload_on", "ON"),
		payloadOff:    cfg.StrDef("payload_off", "OFF"),
		resetHumidity: cfg.StrDef("payload_reset_humidity", "100"),
		resetMode:     cfg.StrDef("payload_reset_mode", "None"),
		minHum:        cfg.Float("min_humidity", 0),
		maxHum:        cfg.Float("max_humidity", 100),
		modes:         cfg.Strings("modes"),
		deviceClass:   cfg.StrDef("device_class", "humidifier"),
	}
	if h.minHum >= h.maxHum {
		return nil, fmt.Errorf("min_humidity must be below max_humidity")
	}
	if h.deviceClass != "humidifier" && h.deviceClass != "dehumidifier" {
		return nil, fmt.Errorf("device_class must be humidifier or dehumidifier, got %q", h.deviceClass)
	}
	var err error
	if h.power, err = newHVACProp(e, "command_topic", "command_template", "state_topic"); err != nil {
		return nil, err
	}
	if h.humidityCmd, err = newHVACProp(e, "target_humidity_command_topic", "target_humidity_command_template", "target_humidity_state_topic"); err != nil {
		return nil, err
	}
	if h.modeCmd, err = newHVACProp(e, "mode_command_topic", "mode_command_template", "mode_state_topic"); err != nil {
		return nil, err
	}
	if h.modeCmd.enabled() {
		if len(h.modes) == 0 {
			return nil, errMissing("modes")
		}
		h.features |= humidifierModes
	}
	if h.power.assumed {
		e.state = core.StateOff
	}
	e.attrs = h.attributes
	if err := h.watch(); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, h)
	e.write(false)
	return h, nil
}

func (h *mqttHumidifier) stop() {}

// attributes is called by entity.write with e.mu held.
func (h *mqttHumidifier) attributes() map[string]any {
	a := map[string]any{
		"device_class":       h.deviceClass,
		"min_humidity":       h.minHum,
		"max_humidity":       h.maxHum,
		"supported_features": h.features,
		"humidity":           nil,
	}
	if h.target != nil {
		a["humidity"] = int(*h.target)
	}
	if h.current != nil {
		a["current_humidity"] = *h.current
	}
	if h.features&humidifierModes != 0 {
		a["mode"] = hvacStr(h.modeValue)
		a["available_modes"] = h.modes
	}
	if h.action != "" {
		a["action"] = h.action
	}
	return a
}

func (h *mqttHumidifier) watch() error {
	e := h.e
	ctx := context.Background()
	warn := func(what, payload string) {
		e.d.log.Warn("humidifier payload rejected", "entity_id", e.entityID, "property", what, "payload", payload)
	}
	for _, w := range []struct {
		topic, tpl string
		apply      func(string)
	}{
		{"state_topic", "state_value_template", func(payload string) {
			switch payload {
			case h.payloadOn:
				e.setState(core.StateOn, ctx)
			case h.payloadOff:
				e.setState(core.StateOff, ctx)
			case "None":
				e.setState("", ctx)
			default:
				warn("state", payload)
			}
		}},
		{"target_humidity_state_topic", "target_humidity_state_template", func(payload string) {
			if payload == h.resetHumidity {
				hvacSet(e, ctx, func() { h.target = nil })
				return
			}
			f, ok := hvacNumber(payload)
			if !ok {
				warn("target_humidity", payload)
				return
			}
			f = math.RoundToEven(f)
			if f < h.minHum || f > h.maxHum {
				warn("target_humidity", payload)
				return
			}
			hvacSet(e, ctx, func() { h.target = &f })
		}},
		{"current_humidity_topic", "current_humidity_template", func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { h.current = nil })
				return
			}
			f, ok := hvacNumber(payload)
			if !ok {
				warn("current_humidity", payload)
				return
			}
			hvacSet(e, ctx, func() { h.current = &f })
		}},
		{"mode_state_topic", "mode_state_template", func(payload string) {
			if payload == h.resetMode {
				hvacSet(e, ctx, func() { h.modeValue = "" })
				return
			}
			if !hvacIn(h.modes, payload) {
				warn("mode", payload)
				return
			}
			hvacSet(e, ctx, func() { h.modeValue = payload })
		}},
		{"action_topic", "action_template", func(payload string) {
			if payload == "None" {
				hvacSet(e, ctx, func() { h.action = "" })
				return
			}
			if !hvacIn(humidifierActions, payload) {
				warn("action", payload)
				return
			}
			hvacSet(e, ctx, func() { h.action = payload })
		}},
	} {
		if err := hvacWatch(e, w.topic, w.tpl, w.apply); err != nil {
			return err
		}
	}
	return nil
}

func (h *mqttHumidifier) TurnOn(ctx context.Context, _ map[string]any) error {
	if err := h.power.send(h.payloadOn, h.payloadOn); err != nil {
		return err
	}
	if h.power.assumed {
		h.e.setState(core.StateOn, ctx)
	}
	return nil
}

func (h *mqttHumidifier) TurnOff(ctx context.Context, _ map[string]any) error {
	if err := h.power.send(h.payloadOff, h.payloadOff); err != nil {
		return err
	}
	if h.power.assumed {
		h.e.setState(core.StateOff, ctx)
	}
	return nil
}

func (h *mqttHumidifier) SetHumidity(ctx context.Context, humidity int) error {
	if f := float64(humidity); f < h.minHum || f > h.maxHum {
		return fmt.Errorf("humidity %d is outside %s..%s", humidity, pyNumber(h.minHum), pyNumber(h.maxHum))
	}
	if err := h.humidityCmd.send(humidity, strconv.Itoa(humidity)); err != nil {
		return err
	}
	if h.humidityCmd.assumed {
		hvacSet(h.e, ctx, func() { v := float64(humidity); h.target = &v })
	}
	return nil
}

func (h *mqttHumidifier) SetMode(ctx context.Context, mode string) error {
	if err := hvacRequire(h.modeCmd, "mode", "mode_command_topic"); err != nil {
		return err
	}
	if !hvacIn(h.modes, mode) {
		return fmt.Errorf("mode %q is not one of %s", mode, strings.Join(h.modes, ", "))
	}
	if err := h.modeCmd.send(mode, mode); err != nil {
		return err
	}
	if h.modeCmd.assumed {
		hvacSet(h.e, ctx, func() { h.modeValue = mode })
	}
	return nil
}

var (
	_ core.OnOff          = (*mqttHumidifier)(nil)
	_ core.Humidifier     = (*mqttHumidifier)(nil)
	_ core.HumidifierMode = (*mqttHumidifier)(nil)
)
