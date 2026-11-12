/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["lawn_mower"] = newLawnMower
}

var lawnMowerActivities = map[string]bool{"mowing": true, "docked": true, "paused": true, "error": true, "returning": true}

// mowerCommand is one of the three command channels; HA publishes the
// command name as the payload unless a template rewrites it.
type mowerCommand struct {
	name  string
	topic string
	tpl   *template.Template
	state string // activity assumed after the command in optimistic mode
	bit   int
}

type mqttLawnMower struct {
	e          *entity
	cmds       map[string]*mowerCommand
	optimistic bool
}

func newLawnMower(e *entity) (component, error) {
	cfg := e.cfg
	m := &mqttLawnMower{e: e, cmds: map[string]*mowerCommand{}, optimistic: cfg.Bool("optimistic", false) || !cfg.Has("activity_state_topic")}
	features := 0
	for _, c := range []mowerCommand{
		{name: "start_mowing", state: "mowing", bit: 1},
		{name: "pause", state: "paused", bit: 2},
		{name: "dock", state: "docked", bit: 4},
	} {
		c := c
		c.topic = cfg.Str(c.name + "_command_topic")
		if !cfg.Has(c.name + "_command_topic") {
			continue
		}
		var err error
		if c.tpl, err = cfg.Template(c.name + "_command_template"); err != nil {
			return nil, err
		}
		m.cmds[c.name] = &c
		features |= c.bit
	}
	e.attrs = func() map[string]any {
		a := map[string]any{"supported_features": features}
		if m.optimistic {
			a["assumed_state"] = true
		}
		return a
	}
	if cfg.Has("activity_state_topic") {
		tpl, err := cfg.Template("activity_value_template")
		if err != nil {
			return nil, err
		}
		e.subscribe(cfg.Str("activity_state_topic"), func(msg Message) {
			payload := string(msg.Payload)
			if tpl != nil {
				var err error
				payload, err = e.render(tpl, msg.Payload)
				if err != nil {
					e.d.log.Warn("activity_value_template failed", "entity_id", e.entityID, "err", err)
					return
				}
			}
			switch {
			case payload == "":
			case payload == "None":
				e.setState("", context.Background())
			case lawnMowerActivities[payload]:
				e.setState(payload, context.Background())
			default:
				e.d.log.Warn("lawn_mower payload is not a valid activity", "entity_id", e.entityID, "payload", payload)
			}
		})
	}
	e.d.core.RegisterController(e.entityID, m)
	e.write(false)
	return m, nil
}

func (m *mqttLawnMower) stop() {}

func (m *mqttLawnMower) run(ctx context.Context, name string) error {
	c, ok := m.cmds[name]
	if !ok {
		return fmt.Errorf("lawn_mower does not support %s: no %s_command_topic", name, name)
	}
	payload := name
	if c.tpl != nil {
		var err error
		payload, err = m.e.renderCommand(c.tpl, name)
		if err != nil {
			return err
		}
	}
	if err := m.e.publish(c.topic, payload); err != nil {
		return err
	}
	if m.optimistic {
		m.e.setState(c.state, ctx)
	}
	return nil
}

func (m *mqttLawnMower) StartMowing(ctx context.Context) error { return m.run(ctx, "start_mowing") }
func (m *mqttLawnMower) Pause(ctx context.Context) error       { return m.run(ctx, "pause") }
func (m *mqttLawnMower) Dock(ctx context.Context) error        { return m.run(ctx, "dock") }
