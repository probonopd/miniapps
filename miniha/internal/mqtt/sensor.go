/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["sensor"] = newSensor
	components["binary_sensor"] = newBinarySensor
}

type sensor struct {
	e *entity
}

func newSensor(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("state_topic") {
		return nil, errMissing("state_topic")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	s := &sensor{e}
	e.attrs = func() map[string]any {
		a := map[string]any{}
		for _, k := range []string{"unit_of_measurement", "device_class", "state_class"} {
			if v := cfg.Str(k); v != "" {
				a[k] = v
			}
		}
		if opts := cfg.Strings("options"); opts != nil {
			a["options"] = opts
		}
		return a
	}
	force := cfg.Bool("force_update", false)
	e.subscribe(cfg.Str("state_topic"), func(m Message) {
		payload := string(m.Payload)
		if tpl != nil {
			payload, err = e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("value_template failed", "entity_id", e.entityID, "err", err)
				return
			}
		}
		// HA ignores an empty render and maps "None" to unknown.
		if payload == "" {
			return
		}
		if payload == "None" {
			payload = ""
		}
		if len(payload) > core.MaxStateLength {
			e.d.log.Warn("state too long", "entity_id", e.entityID)
			return
		}
		e.startExpire()
		e.mu.Lock()
		e.state = payload
		e.mu.Unlock()
		e.write(force)
	})
	e.write(false)
	return s, nil
}

func (s *sensor) stop() {}

type binarySensor struct {
	e *entity
}

func newBinarySensor(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("state_topic") {
		return nil, errMissing("state_topic")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	on := cfg.StrDef("payload_on", "ON")
	off := cfg.StrDef("payload_off", "OFF")
	b := &binarySensor{e}
	e.attrs = func() map[string]any {
		a := map[string]any{}
		if v := cfg.Str("device_class"); v != "" {
			a["device_class"] = v
		}
		return a
	}
	offDelay := cfg.Float("off_delay", 0)
	var offTimer *timerHandle
	force := cfg.Bool("force_update", false)
	e.subscribe(cfg.Str("state_topic"), func(m Message) {
		payload := string(m.Payload)
		if tpl != nil {
			payload, err = e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("value_template failed", "entity_id", e.entityID, "err", err)
				return
			}
		}
		if payload == "" {
			return
		}
		var state string
		switch payload {
		case on:
			state = core.StateOn
		case off:
			state = core.StateOff
		case "None":
			state = ""
		default:
			e.d.log.Warn("binary_sensor payload matches neither payload_on nor payload_off", "entity_id", e.entityID, "payload", payload)
			return
		}
		e.startExpire()
		e.mu.Lock()
		e.state = state
		e.mu.Unlock()
		e.write(force)
		if offDelay > 0 && state == core.StateOn {
			offTimer = after(offTimer, offDelay, func() { e.setState(core.StateOff, context.Background()) })
		}
	})
	e.write(false)
	return b, nil
}

func (b *binarySensor) stop() {}

var _ = fmt.Sprintf
var _ = template.IsTemplate
