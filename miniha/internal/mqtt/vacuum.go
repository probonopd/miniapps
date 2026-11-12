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
	"slices"
	"strings"
)

func init() {
	components["vacuum"] = newVacuum
}

// HA VacuumEntityFeature bits.
const (
	vacFeatPause       = 4
	vacFeatStop        = 8
	vacFeatReturnHome  = 16
	vacFeatFanSpeed    = 32
	vacFeatBattery     = 64
	vacFeatSendCommand = 256
	vacFeatLocate      = 512
	vacFeatCleanSpot   = 1024
	vacFeatState       = 4096
	vacFeatStart       = 8192
)

// vacuumFeatures maps the supported_features names of the MQTT vacuum
// (state schema) to HA's feature bits.
var vacuumFeatures = map[string]int{
	"start": vacFeatStart, "stop": vacFeatStop, "pause": vacFeatPause,
	"return_home": vacFeatReturnHome, "battery": vacFeatBattery, "status": vacFeatState,
	"locate": vacFeatLocate, "clean_spot": vacFeatCleanSpot, "fan_speed": vacFeatFanSpeed,
	"send_command": vacFeatSendCommand,
}

var vacuumStates = []string{"cleaning", "docked", "paused", "idle", "returning", "error"}

type mqttVacuum struct {
	e        *entity
	features int
	fanList  []string
	cmdTopic string

	// Guarded by e.mu because e.attrs reads them under that lock.
	battery  *float64
	fanSpeed string
}

func newVacuum(e *entity) (component, error) {
	cfg := e.cfg
	names := []string{"start", "stop", "return_home", "status", "battery", "clean_spot"}
	if cfg.Has("supported_features") {
		names = cfg.Strings("supported_features")
	}
	features := 0
	for _, n := range names {
		bit, ok := vacuumFeatures[n]
		if !ok {
			return nil, fmt.Errorf("unsupported vacuum feature %q", n)
		}
		features |= bit
	}
	v := &mqttVacuum{e: e, features: features, fanList: cfg.Strings("fan_speed_list"), cmdTopic: cfg.Str("command_topic")}
	e.attrs = v.attributes
	if cfg.Has("state_topic") {
		e.subscribe(cfg.Str("state_topic"), v.onState)
	}
	e.d.core.RegisterController(e.entityID, v)
	e.write(false)
	return v, nil
}

func (v *mqttVacuum) stop() {}

// onState handles the JSON state message; keys that are absent keep their
// previous value, as in HA.
func (v *mqttVacuum) onState(m Message) {
	var msg map[string]any
	if err := json.Unmarshal(m.Payload, &msg); err != nil {
		v.e.d.log.Warn("vacuum state payload is not a JSON object", "entity_id", v.e.entityID)
		return
	}
	v.e.mu.Lock()
	if raw, ok := msg["state"]; ok {
		s, _ := raw.(string)
		if slices.Contains(vacuumStates, s) {
			v.e.state = s
		} else {
			v.e.d.log.Warn("invalid vacuum state", "entity_id", v.e.entityID, "state", raw)
		}
	}
	if raw, ok := msg["battery_level"]; ok {
		if f, ok := raw.(float64); ok && f >= 0 && f <= 100 {
			v.battery = &f
		} else {
			v.e.d.log.Warn("invalid vacuum battery_level", "entity_id", v.e.entityID, "value", raw)
		}
	}
	if raw, ok := msg["fan_speed"]; ok {
		if s, ok := raw.(string); ok {
			v.fanSpeed = s
		} else {
			v.e.d.log.Warn("invalid vacuum fan_speed", "entity_id", v.e.entityID, "value", raw)
		}
	}
	v.e.mu.Unlock()
	v.e.write(false)
}

// attributes is called with e.mu held.
func (v *mqttVacuum) attributes() map[string]any {
	a := map[string]any{"supported_features": v.features}
	if v.features&vacFeatBattery != 0 && v.battery != nil {
		a["battery_level"] = int(math.Round(*v.battery))
		a["battery_icon"] = batteryIcon(*v.battery, v.e.state == "docked")
	}
	if v.features&vacFeatFanSpeed != 0 {
		if v.fanSpeed != "" {
			a["fan_speed"] = v.fanSpeed
		}
		a["fan_speed_list"] = append([]string{}, v.fanList...)
	}
	return a
}

// batteryIcon follows HA's icon_for_battery_level.
func batteryIcon(level float64, charging bool) string {
	icon := "mdi:battery"
	switch {
	case charging && level > 10:
		icon += fmt.Sprintf("-charging-%d", int(math.Round(level/20-0.01))*20)
	case charging:
		icon += "-outline"
	case level <= 5:
		icon += "-alert"
	case level < 95:
		icon += fmt.Sprintf("-%d", int(math.Round(level/10-0.01))*10)
	}
	return icon
}

func (v *mqttVacuum) send(feature int, name, topic, payload string) error {
	if v.features&feature == 0 {
		return fmt.Errorf("vacuum %s does not support %s", v.e.entityID, name)
	}
	if topic == "" {
		return fmt.Errorf("vacuum %s has no topic configured for %s", v.e.entityID, name)
	}
	return v.e.publish(topic, payload)
}

func (v *mqttVacuum) Start(context.Context) error {
	return v.send(vacFeatStart, "start", v.cmdTopic, v.e.cfg.StrDef("payload_start", "start"))
}

func (v *mqttVacuum) Pause(context.Context) error {
	return v.send(vacFeatPause, "pause", v.cmdTopic, v.e.cfg.StrDef("payload_pause", "pause"))
}

func (v *mqttVacuum) Stop(context.Context) error {
	return v.send(vacFeatStop, "stop", v.cmdTopic, v.e.cfg.StrDef("payload_stop", "stop"))
}

func (v *mqttVacuum) ReturnToBase(context.Context) error {
	return v.send(vacFeatReturnHome, "return_to_base", v.cmdTopic, v.e.cfg.StrDef("payload_return_to_base", "return_to_base"))
}

func (v *mqttVacuum) CleanSpot(context.Context) error {
	return v.send(vacFeatCleanSpot, "clean_spot", v.cmdTopic, v.e.cfg.StrDef("payload_clean_spot", "clean_spot"))
}

func (v *mqttVacuum) Locate(context.Context) error {
	return v.send(vacFeatLocate, "locate", v.cmdTopic, v.e.cfg.StrDef("payload_locate", "locate"))
}

func (v *mqttVacuum) SetFanSpeed(_ context.Context, speed string) error {
	if len(v.fanList) > 0 && !slices.Contains(v.fanList, speed) {
		return fmt.Errorf("fan speed %q is not one of %s", speed, strings.Join(v.fanList, ", "))
	}
	return v.send(vacFeatFanSpeed, "fan_speed", v.e.cfg.Str("set_fan_speed_topic"), speed)
}

// SendCommand publishes the bare command, or a JSON object carrying the
// parameters next to it, which is the shape HA sends.
func (v *mqttVacuum) SendCommand(_ context.Context, command string, params any) error {
	payload := command
	switch p := params.(type) {
	case nil:
	case map[string]any:
		if len(p) > 0 {
			obj := map[string]any{}
			for k, val := range p {
				obj[k] = val
			}
			obj["command"] = command
			b, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			payload = string(b)
		}
	default:
		return fmt.Errorf("vacuum send_command params must be an object")
	}
	return v.send(vacFeatSendCommand, "send_command", v.e.cfg.Str("send_command_topic"), payload)
}
