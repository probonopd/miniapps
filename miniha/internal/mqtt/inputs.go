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
	"regexp"
	"strconv"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["number"] = newNumber
	components["select"] = newSelect
	components["button"] = newButton
	components["text"] = newText
}

// commander is the shared command side: command_topic plus an optional
// command_template rendered with "value".
type commander struct {
	e      *entity
	topic  string
	tpl    *template.Template
	optimi bool
}

func newCommander(e *entity) (*commander, error) {
	if !e.cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	tpl, err := e.cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	return &commander{e: e, topic: e.cfg.Str("command_topic"), tpl: tpl, optimi: e.cfg.Bool("optimistic", false) || !e.cfg.Has("state_topic")}, nil
}

func (c *commander) send(value any, payload string) error {
	if c.tpl != nil {
		var err error
		payload, err = c.e.renderCommand(c.tpl, value)
		if err != nil {
			return err
		}
	}
	return c.e.publish(c.topic, payload)
}

// subscribeState wires state_topic + value_template to apply.
func subscribeState(e *entity, apply func(string)) error {
	if !e.cfg.Has("state_topic") {
		return nil
	}
	tpl, err := e.cfg.Template("value_template")
	if err != nil {
		return err
	}
	e.subscribe(e.cfg.Str("state_topic"), func(m Message) {
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
		apply(payload)
	})
	return nil
}

// pyNumber formats like HA: integral values without a fraction.
func pyNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return template.PyStr(f)
}

// --- number ---

type number struct {
	c              *commander
	min, max, step float64
}

func newNumber(e *entity) (component, error) {
	c, err := newCommander(e)
	if err != nil {
		return nil, err
	}
	cfg := e.cfg
	n := &number{c: c, min: cfg.Float("min", 1), max: cfg.Float("max", 100), step: cfg.Float("step", 1)}
	if n.min > n.max || n.step <= 0 {
		return nil, fmt.Errorf("number: min must be <= max and step > 0")
	}
	mode := cfg.StrDef("mode", "auto")
	e.attrs = func() map[string]any {
		a := map[string]any{"min": n.min, "max": n.max, "step": n.step, "mode": mode}
		for _, k := range []string{"unit_of_measurement", "device_class"} {
			if v := cfg.Str(k); v != "" {
				a[k] = v
			}
		}
		return a
	}
	reset := cfg.StrDef("payload_reset", "None")
	if err := subscribeState(e, func(payload string) {
		if payload == reset {
			e.setState("", context.Background())
			return
		}
		f, err := strconv.ParseFloat(payload, 64)
		if err != nil {
			e.d.log.Warn("number payload is not numeric", "entity_id", e.entityID, "payload", payload)
			return
		}
		if f < n.min || f > n.max {
			e.d.log.Warn("number payload out of range", "entity_id", e.entityID, "payload", payload)
			return
		}
		e.setState(pyNumber(f), context.Background())
	}); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, n)
	e.write(false)
	return n, nil
}

func (n *number) stop() {}

func (n *number) SetValue(ctx context.Context, value any) error {
	f, ok := num(value)
	if !ok {
		return fmt.Errorf("value must be a number")
	}
	if f < n.min || f > n.max {
		return fmt.Errorf("value %s is out of range %s..%s", pyNumber(f), pyNumber(n.min), pyNumber(n.max))
	}
	s := pyNumber(f)
	if err := n.c.send(s, s); err != nil {
		return err
	}
	if n.c.optimi {
		n.c.e.setState(s, ctx)
	}
	return nil
}

// --- select ---

type selectEntity struct {
	c       *commander
	options []string
}

func newSelect(e *entity) (component, error) {
	c, err := newCommander(e)
	if err != nil {
		return nil, err
	}
	s := &selectEntity{c: c, options: e.cfg.Strings("options")}
	if len(s.options) == 0 {
		return nil, errMissing("options")
	}
	e.attrs = func() map[string]any { return map[string]any{"options": s.options} }
	if err := subscribeState(e, func(payload string) {
		if payload == "None" {
			e.setState("", context.Background())
			return
		}
		if !s.has(payload) {
			e.d.log.Warn("select payload is not an option", "entity_id", e.entityID, "payload", payload)
			return
		}
		e.setState(payload, context.Background())
	}); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, s)
	e.write(false)
	return s, nil
}

func (s *selectEntity) stop() {}

func (s *selectEntity) has(o string) bool {
	for _, x := range s.options {
		if x == o {
			return true
		}
	}
	return false
}

func (s *selectEntity) SelectOption(ctx context.Context, option string) error {
	if !s.has(option) {
		return fmt.Errorf("invalid option %q", option)
	}
	if err := s.c.send(option, option); err != nil {
		return err
	}
	if s.c.optimi {
		s.c.e.setState(option, ctx)
	}
	return nil
}

// --- button ---

type button struct {
	c       *commander
	payload string
}

func newButton(e *entity) (component, error) {
	c, err := newCommander(e)
	if err != nil {
		return nil, err
	}
	b := &button{c: c, payload: e.cfg.StrDef("payload_press", "PRESS")}
	e.attrs = func() map[string]any {
		a := map[string]any{}
		if v := e.cfg.Str("device_class"); v != "" {
			a["device_class"] = v
		}
		return a
	}
	e.d.core.RegisterController(e.entityID, b)
	e.write(false)
	return b, nil
}

func (b *button) stop() {}

// Press publishes and, as HA does, records the press time as the state.
func (b *button) Press(ctx context.Context) error {
	if err := b.c.send(b.payload, b.payload); err != nil {
		return err
	}
	b.c.e.setState(b.c.e.d.core.Now().Format("2006-01-02T15:04:05.000000-07:00"), ctx)
	return nil
}

// --- text ---

type text struct {
	c        *commander
	min, max int
	pattern  *regexp.Regexp
}

func newText(e *entity) (component, error) {
	c, err := newCommander(e)
	if err != nil {
		return nil, err
	}
	t := &text{c: c, min: e.cfg.Int("min", 0), max: e.cfg.Int("max", 255)}
	if t.min < 0 || t.max > 255 || t.min > t.max {
		return nil, fmt.Errorf("text: min/max must be within 0..255")
	}
	if p := e.cfg.Str("pattern"); p != "" {
		t.pattern, err = regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("text pattern: %w", err)
		}
	}
	mode := e.cfg.StrDef("mode", "text")
	e.attrs = func() map[string]any {
		a := map[string]any{"min": t.min, "max": t.max, "mode": mode}
		if t.pattern != nil {
			a["pattern"] = t.pattern.String()
		}
		return a
	}
	if err := subscribeState(e, func(payload string) {
		if err := t.valid(payload); err != nil {
			e.d.log.Warn("text payload rejected", "entity_id", e.entityID, "err", err)
			return
		}
		e.setState(payload, context.Background())
	}); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, t)
	e.write(false)
	return t, nil
}

func (t *text) stop() {}

func (t *text) valid(s string) error {
	n := len([]rune(s))
	if n < t.min || n > t.max {
		return fmt.Errorf("length %d outside %d..%d", n, t.min, t.max)
	}
	if t.pattern != nil && !t.pattern.MatchString(s) {
		return fmt.Errorf("value does not match pattern %s", t.pattern)
	}
	return nil
}

func (t *text) SetValue(ctx context.Context, value any) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("value must be a string")
	}
	if err := t.valid(s); err != nil {
		return err
	}
	if err := t.c.send(s, s); err != nil {
		return err
	}
	if t.c.optimi {
		t.c.e.setState(s, ctx)
	}
	return nil
}
