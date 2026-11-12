/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"
	"regexp"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["lock"] = newLock
}

// renderCmdVars renders a command_template with the entity variables plus
// the component specific ones (code, action, tone, ...).
func renderCmdVars(e *entity, tpl *template.Template, vars map[string]any) (string, error) {
	all := map[string]any{"entity_id": e.entityID, "name": e.d.core.FriendlyName(e.entityID)}
	for k, v := range vars {
		all[k] = v
	}
	return tpl.Render(all, nil)
}

// codeVar hands a service code to a template; HA passes None when the
// caller gave no code, so templates can test it with "if code".
func codeVar(code string) any {
	if code == "" {
		return nil
	}
	return code
}

type mqttLock struct {
	e          *entity
	topic      string
	tpl        *template.Template
	payloads   map[string]string // service -> command payload
	hasOpen    bool
	reset      string
	states     map[string]string // state payload -> HA state
	codeFormat string
	codeRegexp *regexp.Regexp
	optimistic bool
}

func newLock(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	tpl, err := cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	l := &mqttLock{
		e: e, topic: cfg.Str("command_topic"), tpl: tpl,
		payloads: map[string]string{
			"lock":   cfg.StrDef("payload_lock", "LOCK"),
			"unlock": cfg.StrDef("payload_unlock", "UNLOCK"),
			"open":   cfg.StrDef("payload_open", "OPEN"),
		},
		hasOpen:    cfg.Has("payload_open"),
		reset:      cfg.StrDef("payload_reset", "None"),
		states:     map[string]string{},
		optimistic: cfg.Bool("optimistic", false) || !cfg.Has("state_topic"),
	}
	for _, s := range []struct{ state, payload string }{
		{"locked", "LOCKED"}, {"locking", "LOCKING"}, {"unlocked", "UNLOCKED"}, {"unlocking", "UNLOCKING"},
		{"jammed", "JAMMED"}, {"open", "OPEN"}, {"opening", "OPENING"},
	} {
		payload := cfg.StrDef("state_"+s.state, s.payload)
		if other, dup := l.states[payload]; dup {
			// Two states on one payload would make the reported state depend on map order.
			return nil, fmt.Errorf("state_%s and state_%s share the payload %q", other, s.state, payload)
		}
		l.states[payload] = s.state
	}
	if cfg.Has("code_format") {
		l.codeFormat = cfg.Str("code_format")
		// Anchored at the start only, like Python's re.match which HA uses.
		l.codeRegexp, err = regexp.Compile(`^(?:` + l.codeFormat + `)`)
		if err != nil {
			return nil, fmt.Errorf("code_format: %w", err)
		}
	}
	e.attrs = func() map[string]any {
		a := map[string]any{}
		if l.codeFormat != "" {
			a["code_format"] = l.codeFormat
		}
		if l.hasOpen {
			a["supported_features"] = 1
		} else {
			a["supported_features"] = 0
		}
		if l.optimistic {
			a["assumed_state"] = true
		}
		return a
	}
	if err := subscribeState(e, func(payload string) {
		if payload == l.reset {
			e.setState("", context.Background())
			return
		}
		state, ok := l.states[payload]
		if !ok {
			e.d.log.Warn("lock payload matches no state", "entity_id", e.entityID, "payload", payload)
			return
		}
		e.setState(state, context.Background())
	}); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, l)
	e.write(false)
	return l, nil
}

func (l *mqttLock) stop() {}

func (l *mqttLock) send(ctx context.Context, action, code, optimisticState string) error {
	if l.codeRegexp != nil && !l.codeRegexp.MatchString(code) {
		return fmt.Errorf("code does not match code_format %q", l.codeFormat)
	}
	payload := l.payloads[action]
	if l.tpl != nil {
		var err error
		payload, err = renderCmdVars(l.e, l.tpl, map[string]any{"value": payload, "code": codeVar(code)})
		if err != nil {
			return err
		}
	}
	if err := l.e.publish(l.topic, payload); err != nil {
		return err
	}
	if l.optimistic {
		l.e.setState(optimisticState, ctx)
	}
	return nil
}

func (l *mqttLock) Lock(ctx context.Context, code string) error {
	return l.send(ctx, "lock", code, "locked")
}

func (l *mqttLock) Unlock(ctx context.Context, code string) error {
	return l.send(ctx, "unlock", code, "unlocked")
}

func (l *mqttLock) Open(ctx context.Context, code string) error {
	if !l.hasOpen {
		return fmt.Errorf("lock does not support open: payload_open is not configured")
	}
	return l.send(ctx, "open", code, "open")
}
