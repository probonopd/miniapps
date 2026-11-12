/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"fmt"

	"github.com/probonopd/miniha/internal/core"
)

func init() {
	components["device_automation"] = newDeviceAutomation
}

// newDeviceAutomation announces one device trigger. Like tags it is not a
// state-bearing entity: the constructor never writes a state, each matching
// message only fires a "device_trigger" event. Devices usually announce
// several triggers (one per button and gesture) under one device block.
func newDeviceAutomation(e *entity) (component, error) {
	cfg := e.cfg
	if cfg.Str("automation_type") != "trigger" {
		return nil, fmt.Errorf("automation_type must be \"trigger\"")
	}
	if !cfg.Has("topic") {
		return nil, errMissing("topic")
	}
	if !cfg.Has("type") {
		return nil, errMissing("type")
	}
	if !cfg.Has("subtype") {
		return nil, errMissing("subtype")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	topic, trigType, subtype := cfg.Str("topic"), cfg.Str("type"), cfg.Str("subtype")
	e.subscribe(topic, func(m Message) {
		payload := string(m.Payload)
		if tpl != nil {
			payload, err = e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("device trigger value_template failed", "entity_id", e.entityID, "err", err)
				return
			}
		}
		if cfg.Has("payload") && payload != cfg.Str("payload") {
			return
		}
		var deviceID any
		if e.deviceID != "" {
			deviceID = e.deviceID
		}
		e.d.core.Fire("device_trigger", map[string]any{
			"device_id": deviceID, "type": trigType, "subtype": subtype, "payload": payload, "topic": m.Topic,
		}, core.NewContext())
	})
	return &tagComponent{}, nil
}
