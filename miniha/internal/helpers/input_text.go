/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"regexp"
	"unicode/utf8"
)

type inputTextDef struct {
	name, icon, mode, pattern string
	min, max                  int
	initial                   string
	hasInitial                bool
}

type inputText struct {
	base
	d     inputTextDef
	re    *regexp.Regexp
	value string
}

func parseInputText(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputText, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputTextDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	lo, err := o.integer("min")
	if err != nil {
		return nil, err
	}
	hi, err := o.integer("max")
	if err != nil {
		return nil, err
	}
	if v, ok := o.get("initial"); ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, o.errf("initial", "must be a string, got %T", v)
		}
		d.initial, d.hasInitial = s, true
	}
	if d.pattern, err = o.str("pattern"); err != nil {
		return nil, err
	}
	if d.mode, err = o.str("mode"); err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	d.min, d.max = 0, 100
	if lo != nil {
		d.min = *lo
	}
	if hi != nil {
		d.max = *hi
	}
	if d.min < 0 || d.min > 255 {
		return nil, o.errf("min", "%d must be between 0 and 255", d.min)
	}
	if d.max < 1 || d.max > 255 {
		return nil, o.errf("max", "%d must be between 1 and 255", d.max)
	}
	if d.min > d.max {
		return nil, o.errf("min", "%d is greater than max %d", d.min, d.max)
	}
	switch d.mode {
	case "":
		d.mode = "text"
	case "text", "password":
	default:
		return nil, o.errf("mode", "%q must be text or password", d.mode)
	}
	t := &inputText{base: newBase(h, domInputText, id, d.name, d.icon), d: d}
	if d.pattern != "" {
		// Anchored on both ends because the frontend applies the pattern
		// to the whole value.
		if t.re, err = regexp.Compile("^(?:" + d.pattern + ")$"); err != nil {
			return nil, o.errf("pattern", "%v", err)
		}
	}
	if d.hasInitial {
		if err := t.validate(d.initial); err != nil {
			return nil, o.errf("initial", "%v", err)
		}
	}
	return t, nil
}

func (t *inputText) def() any { return t.d }

func (t *inputText) validate(s string) error {
	n := utf8.RuneCountInString(s)
	if n < t.d.min || n > t.d.max {
		return fmt.Errorf("length %d is outside the range %d to %d", n, t.d.min, t.d.max)
	}
	if t.re != nil && !t.re.MatchString(s) {
		return fmt.Errorf("%q does not match the pattern %q", s, t.d.pattern)
	}
	return nil
}

func (t *inputText) view() (string, map[string]any) {
	attrs := map[string]any{
		"editable": true,
		"min":      t.d.min,
		"max":      t.d.max,
		"pattern":  nil,
		"mode":     t.d.mode,
	}
	if t.d.pattern != "" {
		attrs["pattern"] = t.d.pattern
	}
	return t.value, attrs
}

func (t *inputText) record() record { return record{State: t.value} }

func (t *inputText) restore(r *record, reload bool) {
	t.value = ""
	if t.d.hasInitial {
		t.value = t.d.initial
		if !reload {
			return
		}
	}
	if r != nil && t.validate(r.State) == nil {
		t.value = r.State
	}
}

func (t *inputText) resume() {}
func (t *inputText) stop()   {}

// SetValue implements core.ValueSetter.
func (t *inputText) SetValue(ctx context.Context, v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("value must be a string, got %T", v)
	}
	return t.h.update(ctx, t, func() ([]event, error) {
		if err := t.validate(s); err != nil {
			return nil, err
		}
		t.value = s
		return nil, nil
	})
}

func (h *Helpers) registerInputTextServices() {
	svc(h, domInputText, "set_value", "Set value", map[string]any{"value": fld("The target value", true)}, func(ctx context.Context, e *inputText, data map[string]any) error {
		v, ok := data["value"]
		if !ok {
			return fmt.Errorf("value is required")
		}
		return e.SetValue(ctx, v)
	})
}
