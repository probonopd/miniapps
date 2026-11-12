/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["switch"] = newSwitch
}

type mqttSwitch struct {
	e          *entity
	cmdTopic   string
	payloadOn  string
	payloadOff string
	cmdTpl     *template.Template
	optimistic bool
}

func newSwitch(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	cmdTpl, err := cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	s := &mqttSwitch{e: e, cmdTopic: cfg.Str("command_topic"), payloadOn: cfg.StrDef("payload_on", "ON"), payloadOff: cfg.StrDef("payload_off", "OFF"), cmdTpl: cmdTpl}
	s.optimistic = cfg.Bool("optimistic", false) || !cfg.Has("state_topic")
	stateOn := cfg.StrDef("state_on", s.payloadOn)
	stateOff := cfg.StrDef("state_off", s.payloadOff)
	e.attrs = func() map[string]any {
		a := map[string]any{}
		if v := cfg.Str("device_class"); v != "" {
			a["device_class"] = v
		}
		if s.optimistic {
			a["assumed_state"] = true
		}
		return a
	}
	if cfg.Has("state_topic") {
		e.subscribe(cfg.Str("state_topic"), func(m Message) {
			payload := string(m.Payload)
			if tpl != nil {
				payload, err = e.render(tpl, m.Payload)
				if err != nil {
					e.d.log.Warn("value_template failed", "entity_id", e.entityID, "err", err)
					return
				}
			}
			switch payload {
			case stateOn:
				e.setState(core.StateOn, context.Background())
			case stateOff:
				e.setState(core.StateOff, context.Background())
			case "None":
				e.setState("", context.Background())
			case "":
			default:
				e.d.log.Warn("switch payload matches neither state_on nor state_off", "entity_id", e.entityID, "payload", payload)
			}
		})
	}
	e.d.core.RegisterController(e.entityID, s)
	e.write(false)
	return s, nil
}

func (s *mqttSwitch) stop() {}

func (s *mqttSwitch) command(ctx context.Context, payload, state string) error {
	if s.cmdTpl != nil {
		var err error
		payload, err = s.e.renderCommand(s.cmdTpl, payload)
		if err != nil {
			return err
		}
	}
	if err := s.e.publish(s.cmdTopic, payload); err != nil {
		return err
	}
	if s.optimistic {
		s.e.setState(state, ctx)
	}
	return nil
}

func (s *mqttSwitch) TurnOn(ctx context.Context, _ map[string]any) error {
	return s.command(ctx, s.payloadOn, core.StateOn)
}

func (s *mqttSwitch) TurnOff(ctx context.Context, _ map[string]any) error {
	return s.command(ctx, s.payloadOff, core.StateOff)
}
