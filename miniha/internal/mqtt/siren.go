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
	"strings"
	"sync"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["siren"] = newSiren
}

type mqttSiren struct {
	e          *entity
	topic      string
	onTpl      *template.Template
	offTpl     *template.Template
	payloadOn  string
	payloadOff string
	stateOn    string
	stateOff   string
	tones      []string
	duration   bool
	volume     bool
	optimistic bool

	mu    sync.Mutex
	extra map[string]any // tone, duration and volume_level the device reported
}

func newSiren(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	onTpl, err := cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	offTpl, err := cfg.Template("command_off_template")
	if err != nil {
		return nil, err
	}
	if offTpl == nil {
		// HA reuses command_template for turn_off when no off template is set.
		offTpl = onTpl
	}
	stateTpl, err := cfg.Template("state_value_template")
	if err != nil {
		return nil, err
	}
	s := &mqttSiren{
		e: e, topic: cfg.Str("command_topic"), onTpl: onTpl, offTpl: offTpl,
		payloadOn: cfg.StrDef("payload_on", "ON"), payloadOff: cfg.StrDef("payload_off", "OFF"),
		tones:      cfg.Strings("available_tones"),
		duration:   cfg.Bool("support_duration", true),
		volume:     cfg.Bool("support_volume_set", true),
		optimistic: cfg.Bool("optimistic", false) || !cfg.Has("state_topic"),
		extra:      map[string]any{},
	}
	s.stateOn = cfg.StrDef("state_on", s.payloadOn)
	s.stateOff = cfg.StrDef("state_off", s.payloadOff)
	e.attrs = func() map[string]any {
		features := 1 | 2
		a := map[string]any{}
		if len(s.tones) > 0 {
			a["available_tones"] = s.tones
			features |= 4
		}
		if s.duration {
			features |= 8
		}
		if s.volume {
			features |= 16
		}
		a["supported_features"] = features
		if s.optimistic {
			a["assumed_state"] = true
		}
		s.mu.Lock()
		for k, v := range s.extra {
			a[k] = v
		}
		s.mu.Unlock()
		return a
	}
	if cfg.Has("state_topic") {
		e.subscribe(cfg.Str("state_topic"), func(m Message) {
			payload := string(m.Payload)
			if stateTpl != nil {
				var err error
				payload, err = e.render(stateTpl, m.Payload)
				if err != nil {
					e.d.log.Warn("state_value_template failed", "entity_id", e.entityID, "err", err)
					return
				}
			}
			s.apply(payload)
		})
	}
	e.d.core.RegisterController(e.entityID, s)
	e.write(false)
	return s, nil
}

func (s *mqttSiren) stop() {}

// apply takes a plain state payload or a JSON object whose "state" key is
// the state and whose tone, duration and volume_level keys are attributes.
func (s *mqttSiren) apply(payload string) {
	if payload == "" || payload == "{}" {
		return
	}
	var state string
	var haveState bool
	switch payload {
	case s.stateOn, s.stateOff, "None":
		state, haveState = payload, true
	default:
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			s.e.d.log.Warn("siren payload is neither a state nor a JSON object", "entity_id", s.e.entityID, "payload", payload)
			return
		}
		if v, ok := obj["state"]; ok {
			state, haveState = fmt.Sprint(v), true
		}
		s.mu.Lock()
		for _, k := range []string{"tone", "duration", "volume_level"} {
			if v, ok := obj[k]; ok {
				s.extra[k] = v
			}
		}
		s.mu.Unlock()
	}
	if !haveState {
		s.e.write(false)
		return
	}
	switch state {
	case s.stateOn:
		s.e.setState("on", context.Background())
	case s.stateOff:
		s.e.setState("off", context.Background())
	case "None":
		s.e.setState("", context.Background())
	default:
		s.e.d.log.Warn("siren state matches neither state_on nor state_off", "entity_id", s.e.entityID, "state", state)
		s.e.write(false)
	}
}

// onParam is one validated turn_on option in the order HA publishes them.
type onParam struct {
	key string
	val any
}

// params validates turn_on data the way HA's siren service does: options the
// siren does not support are dropped, an unknown tone or an out-of-range
// value is an error.
func (s *mqttSiren) params(data map[string]any) ([]onParam, error) {
	var out []onParam
	if v, ok := data["tone"]; ok && len(s.tones) > 0 {
		tone := coerce(v)
		known := false
		for _, t := range s.tones {
			if t == tone {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("tone %q is not one of available_tones [%s]", tone, strings.Join(s.tones, ", "))
		}
		out = append(out, onParam{"tone", tone})
	}
	if v, ok := data["duration"]; ok && s.duration {
		f, ok := num(v)
		if !ok {
			return nil, fmt.Errorf("duration must be a number")
		}
		out = append(out, onParam{"duration", int(f)})
	}
	if v, ok := data["volume_level"]; ok && s.volume {
		f, ok := num(v)
		if !ok || f < 0 || f > 1 {
			return nil, fmt.Errorf("volume_level must be a number from 0 to 1")
		}
		out = append(out, onParam{"volume_level", f})
	}
	return out, nil
}

func (s *mqttSiren) command(ctx context.Context, tpl *template.Template, state, optimisticState string, params []onParam) error {
	var payload string
	if tpl != nil {
		vars := map[string]any{"value": state, "state": state}
		for _, p := range params {
			vars[p.key] = p.val
		}
		var err error
		payload, err = renderCmdVars(s.e, tpl, vars)
		if err != nil {
			return err
		}
	} else {
		// HA's default payload is a JSON object so the options travel along.
		st, _ := json.Marshal(state)
		var b strings.Builder
		b.WriteString(`{"state":` + string(st))
		for _, p := range params {
			v, _ := json.Marshal(p.val)
			b.WriteString(`,"` + p.key + `":` + string(v))
		}
		b.WriteString("}")
		payload = b.String()
	}
	if err := s.e.publish(s.topic, payload); err != nil {
		return err
	}
	if s.optimistic {
		s.e.setState(optimisticState, ctx)
	}
	return nil
}

func (s *mqttSiren) TurnOn(ctx context.Context, data map[string]any) error {
	params, err := s.params(data)
	if err != nil {
		return err
	}
	return s.command(ctx, s.onTpl, s.payloadOn, "on", params)
}

func (s *mqttSiren) TurnOff(ctx context.Context, _ map[string]any) error {
	return s.command(ctx, s.offTpl, s.payloadOff, "off", nil)
}
