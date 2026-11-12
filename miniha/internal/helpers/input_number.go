/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type inputNumberDef struct {
	name, icon, unit, mode string
	min, max, step         float64
	initial                *float64
}

type inputNumber struct {
	base
	d     inputNumberDef
	value float64
}

func parseInputNumber(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputNumber, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputNumberDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	lo, err := o.number("min")
	if err != nil {
		return nil, err
	}
	hi, err := o.number("max")
	if err != nil {
		return nil, err
	}
	step, err := o.number("step")
	if err != nil {
		return nil, err
	}
	if d.initial, err = o.number("initial"); err != nil {
		return nil, err
	}
	if d.mode, err = o.str("mode"); err != nil {
		return nil, err
	}
	if d.unit, err = o.str("unit_of_measurement"); err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	if lo == nil {
		return nil, o.errf("min", "is required")
	}
	if hi == nil {
		return nil, o.errf("max", "is required")
	}
	d.min, d.max = *lo, *hi
	if d.min >= d.max {
		return nil, o.errf("max", "%v must be greater than min %v", d.max, d.min)
	}
	d.step = 1
	if step != nil {
		d.step = *step
	}
	if d.step < 0.001 || d.step > d.max-d.min {
		return nil, o.errf("step", "%v must be between 0.001 and max - min (%v)", d.step, d.max-d.min)
	}
	switch d.mode {
	case "":
		d.mode = "slider"
	case "slider", "box":
	default:
		return nil, o.errf("mode", "%q must be slider or box", d.mode)
	}
	if d.initial != nil && (*d.initial < d.min || *d.initial > d.max) {
		return nil, o.errf("initial", "%v is outside the range %v to %v", *d.initial, d.min, d.max)
	}
	return &inputNumber{base: base{h: h, domain: domInputNumber, objectID: id, name: d.name, icon: d.icon, unit: d.unit}, d: d}, nil
}

func (n *inputNumber) def() any { return n.d }

// format prints integral values without a fraction when the step is
// integral, so a slider with step 1 reports "20" rather than "20.0"; with
// a fractional step the Python float form "20.0" is kept.
func (n *inputNumber) format(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") && n.d.step != math.Trunc(n.d.step) {
		s += ".0"
	}
	return s
}

func (n *inputNumber) view() (string, map[string]any) {
	attrs := map[string]any{
		"initial":  nil,
		"editable": true,
		"min":      n.d.min,
		"max":      n.d.max,
		"step":     n.d.step,
		"mode":     n.d.mode,
	}
	if n.d.initial != nil {
		attrs["initial"] = *n.d.initial
	}
	if n.d.unit != "" {
		attrs["unit_of_measurement"] = n.d.unit
	}
	return n.format(n.value), attrs
}

func (n *inputNumber) record() record {
	st, _ := n.view()
	return record{State: st}
}

func (n *inputNumber) restore(r *record, reload bool) {
	n.value = n.d.min
	if n.d.initial != nil {
		n.value = *n.d.initial
		if !reload {
			return
		}
	}
	if r == nil {
		return
	}
	if v, err := strconv.ParseFloat(r.State, 64); err == nil && v >= n.d.min && v <= n.d.max {
		n.value = v
	}
}

func (n *inputNumber) resume() {}
func (n *inputNumber) stop()   {}

func (n *inputNumber) setLocked(v float64) error {
	if v < n.d.min || v > n.d.max {
		return fmt.Errorf("value %v is outside the range %v to %v", v, n.d.min, n.d.max)
	}
	n.value = v
	return nil
}

// SetValue implements core.ValueSetter.
func (n *inputNumber) SetValue(ctx context.Context, v any) error {
	f, err := toFloat(v)
	if err != nil {
		return fmt.Errorf("value %w", err)
	}
	return n.h.update(ctx, n, func() ([]event, error) {
		return nil, n.setLocked(f)
	})
}

// shift moves by one step and stops at the bounds, as HA does, instead of
// failing like set_value.
func (n *inputNumber) shift(ctx context.Context, dir float64) error {
	return n.h.update(ctx, n, func() ([]event, error) {
		v := n.value + dir*n.d.step
		// Rounding hides binary noise such as 0.1 + 0.2.
		v = math.Round(v*1e9) / 1e9
		n.value = math.Max(n.d.min, math.Min(n.d.max, v))
		return nil, nil
	})
}

func (h *Helpers) registerInputNumberServices() {
	svc(h, domInputNumber, "set_value", "Set", map[string]any{"value": fld("The target value", true)}, func(ctx context.Context, e *inputNumber, data map[string]any) error {
		v, ok := data["value"]
		if !ok {
			return fmt.Errorf("value is required")
		}
		return e.SetValue(ctx, v)
	})
	svc(h, domInputNumber, "increment", "Increment", nil, func(ctx context.Context, e *inputNumber, _ map[string]any) error {
		return e.shift(ctx, 1)
	})
	svc(h, domInputNumber, "decrement", "Decrement", nil, func(ctx context.Context, e *inputNumber, _ map[string]any) error {
		return e.shift(ctx, -1)
	})
}
