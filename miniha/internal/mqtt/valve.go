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
	"sync"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["valve"] = newValve
}

// optionalPayload returns a command payload option. HA lets an explicit
// null switch the command off, so a present-but-null key is "disabled".
func optionalPayload(cfg Config, key, def string) (string, bool) {
	v, present := cfg[key]
	if present && v == nil {
		return "", false
	}
	return cfg.StrDef(key, def), true
}

type mqttValve struct {
	e          *entity
	topic      string
	tpl        *template.Template
	openCmd    string
	hasOpen    bool
	closeCmd   string
	hasClose   bool
	stopCmd    string
	hasStop    bool
	posOpen    float64
	posClosed  float64
	reportsPos bool
	optimistic bool
	states     map[string]string // state payload -> HA state

	mu       sync.Mutex
	state    string
	position int // percent, valid when hasPos
	hasPos   bool
}

func newValve(e *entity) (component, error) {
	cfg := e.cfg
	tpl, err := cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	v := &mqttValve{
		e: e, topic: cfg.Str("command_topic"), tpl: tpl,
		posOpen: cfg.Float("position_open", 100), posClosed: cfg.Float("position_closed", 0),
		reportsPos: cfg.Bool("reports_position", false),
		optimistic: cfg.Bool("optimistic", false) || !cfg.Has("state_topic"),
		states:     map[string]string{},
	}
	if v.posOpen == v.posClosed {
		return nil, fmt.Errorf("position_open and position_closed must differ")
	}
	v.openCmd, v.hasOpen = optionalPayload(cfg, "payload_open", "OPEN")
	v.closeCmd, v.hasClose = optionalPayload(cfg, "payload_close", "CLOSE")
	// Stop has no default payload: a valve only supports it when configured.
	v.stopCmd, v.hasStop = cfg.Str("payload_stop"), cfg.Has("payload_stop")
	if v.reportsPos && !cfg.Has("command_topic") {
		return nil, fmt.Errorf("reports_position needs command_topic to set the position")
	}
	for _, s := range []string{"open", "opening", "closed", "closing"} {
		payload := cfg.StrDef("state_"+s, s)
		if other, dup := v.states[payload]; dup {
			// Two states on one payload would make the reported state depend on map order.
			return nil, fmt.Errorf("state_%s and state_%s share the payload %q", other, s, payload)
		}
		v.states[payload] = s
	}
	e.attrs = func() map[string]any {
		a := map[string]any{}
		if dc := cfg.Str("device_class"); dc != "" {
			a["device_class"] = dc
		}
		features := 0
		if cfg.Has("command_topic") {
			if v.hasOpen {
				features |= 1
			}
			if v.hasClose {
				features |= 2
			}
			if v.reportsPos {
				features |= 4
			}
			if v.hasStop {
				features |= 8
			}
		}
		a["supported_features"] = features
		if v.optimistic {
			a["assumed_state"] = true
		}
		v.mu.Lock()
		if v.reportsPos && v.hasPos {
			a["current_position"] = v.position
		}
		v.mu.Unlock()
		return a
	}
	if err := subscribeState(e, v.apply); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, v)
	e.write(false)
	return v, nil
}

func (v *mqttValve) stop() {}

// percent maps a device position onto 0..100 between closed and open.
func (v *mqttValve) percent(pos float64) int {
	return int(math.Round((pos - v.posClosed) / (v.posOpen - v.posClosed) * 100))
}

// devicePosition is the inverse of percent.
func (v *mqttValve) devicePosition(percent int) int {
	return int(math.Round(v.posClosed + float64(percent)/100*(v.posOpen-v.posClosed)))
}

func (v *mqttValve) inRange(pos float64) bool {
	lo, hi := math.Min(v.posOpen, v.posClosed), math.Max(v.posOpen, v.posClosed)
	return pos >= lo && pos <= hi
}

// apply takes the state payload: a state word, or when the valve reports
// its position a number or a JSON object with "state" and "position".
func (v *mqttValve) apply(payload string) {
	if !v.reportsPos {
		state, ok := v.states[payload]
		if !ok {
			v.e.d.log.Warn("valve payload matches no state", "entity_id", v.e.entityID, "payload", payload)
			return
		}
		v.e.setState(state, context.Background())
		return
	}
	statePayload, posPayload := payload, payload
	if strings.HasPrefix(payload, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			v.e.d.log.Warn("valve payload is not valid JSON", "entity_id", v.e.entityID, "payload", payload)
			return
		}
		statePayload, posPayload = "", ""
		if s, ok := obj["state"]; ok && s != nil {
			statePayload = fmt.Sprint(s)
		}
		if p, ok := obj["position"]; ok && p != nil {
			posPayload = fmt.Sprint(p)
		}
	}
	state := ""
	if s := v.states[statePayload]; s == "opening" || s == "closing" {
		state = s
	}
	hasPos := false
	var pos float64
	if posPayload != "" {
		f, err := strconv.ParseFloat(posPayload, 64)
		if err != nil || !v.inRange(f) {
			if state == "" {
				v.e.d.log.Warn("valve position payload is not a position in range", "entity_id", v.e.entityID, "payload", payload)
				return
			}
		} else {
			hasPos, pos = true, f
		}
	}
	if state == "" && !hasPos {
		v.e.d.log.Warn("valve payload carries neither a transitional state nor a position", "entity_id", v.e.entityID, "payload", payload)
		return
	}
	v.mu.Lock()
	if hasPos {
		v.position, v.hasPos = v.percent(pos), true
		if state == "" {
			state = "open"
			if pos == v.posClosed {
				state = "closed"
			}
		}
	}
	v.mu.Unlock()
	v.e.setState(state, context.Background())
}

func (v *mqttValve) ValveState() string {
	v.e.mu.Lock()
	defer v.e.mu.Unlock()
	return v.e.state
}

func (v *mqttValve) send(value any) error {
	if v.topic == "" {
		return fmt.Errorf("valve has no command_topic")
	}
	payload := fmt.Sprint(value)
	if v.tpl != nil {
		var err error
		payload, err = v.e.renderCommand(v.tpl, value)
		if err != nil {
			return err
		}
	}
	return v.e.publish(v.topic, payload)
}

func (v *mqttValve) OpenValve(ctx context.Context) error {
	if !v.hasOpen {
		return fmt.Errorf("valve does not support open: payload_open is null")
	}
	if err := v.send(v.openCmd); err != nil {
		return err
	}
	if v.optimistic {
		v.mu.Lock()
		v.position, v.hasPos = 100, true
		v.mu.Unlock()
		v.e.setState("open", ctx)
	}
	return nil
}

func (v *mqttValve) CloseValve(ctx context.Context) error {
	if !v.hasClose {
		return fmt.Errorf("valve does not support close: payload_close is null")
	}
	if err := v.send(v.closeCmd); err != nil {
		return err
	}
	if v.optimistic {
		v.mu.Lock()
		v.position, v.hasPos = 0, true
		v.mu.Unlock()
		v.e.setState("closed", ctx)
	}
	return nil
}

func (v *mqttValve) StopValve(ctx context.Context) error {
	if !v.hasStop {
		return fmt.Errorf("valve does not support stop: payload_stop is not configured")
	}
	return v.send(v.stopCmd)
}

func (v *mqttValve) SetValvePosition(ctx context.Context, position int) error {
	if !v.reportsPos {
		return fmt.Errorf("valve does not support set position: reports_position is false")
	}
	if position < 0 || position > 100 {
		return fmt.Errorf("position must be 0..100")
	}
	if err := v.send(v.devicePosition(position)); err != nil {
		return err
	}
	if v.optimistic {
		v.mu.Lock()
		v.position, v.hasPos = position, true
		v.mu.Unlock()
		state := "open"
		if position == 0 {
			state = "closed"
		}
		v.e.setState(state, ctx)
	}
	return nil
}
