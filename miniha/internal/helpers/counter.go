/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"strconv"
)

type counterDef struct {
	name, icon string
	initial    int
	step       int
	minimum    *int
	maximum    *int
	restore    bool
}

type counter struct {
	base
	d     counterDef
	value int
}

func parseCounter(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domCounter, id, raw)
	if err != nil {
		return nil, err
	}
	d := counterDef{step: 1, restore: true}
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	initial, err := o.integer("initial")
	if err != nil {
		return nil, err
	}
	step, err := o.integer("step")
	if err != nil {
		return nil, err
	}
	if d.minimum, err = o.integer("minimum"); err != nil {
		return nil, err
	}
	if d.maximum, err = o.integer("maximum"); err != nil {
		return nil, err
	}
	restore, err := o.boolean("restore")
	if err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	if initial != nil {
		d.initial = *initial
	}
	if step != nil {
		d.step = *step
	}
	if restore != nil {
		d.restore = *restore
	}
	if d.step < 1 {
		return nil, o.errf("step", "%d must be at least 1", d.step)
	}
	if d.minimum != nil && d.maximum != nil && *d.minimum > *d.maximum {
		return nil, o.errf("minimum", "%d is greater than maximum %d", *d.minimum, *d.maximum)
	}
	if !d.inBounds(d.initial) {
		return nil, o.errf("initial", "%d is outside minimum and maximum", d.initial)
	}
	return &counter{base: newBase(h, domCounter, id, d.name, d.icon), d: d}, nil
}

func (d counterDef) inBounds(v int) bool {
	return (d.minimum == nil || v >= *d.minimum) && (d.maximum == nil || v <= *d.maximum)
}

func (d counterDef) clamp(v int) int {
	if d.minimum != nil && v < *d.minimum {
		return *d.minimum
	}
	if d.maximum != nil && v > *d.maximum {
		return *d.maximum
	}
	return v
}

func (c *counter) def() any { return c.d }

func (c *counter) view() (string, map[string]any) {
	attrs := map[string]any{"initial": c.d.initial, "editable": true, "step": c.d.step}
	if c.d.minimum != nil {
		attrs["minimum"] = *c.d.minimum
	}
	if c.d.maximum != nil {
		attrs["maximum"] = *c.d.maximum
	}
	return strconv.Itoa(c.value), attrs
}

func (c *counter) record() record {
	st, _ := c.view()
	return record{State: st}
}

func (c *counter) restore(r *record, reload bool) {
	c.value = c.d.initial
	if r == nil || !(reload || c.d.restore) {
		return
	}
	if v, err := strconv.Atoi(r.State); err == nil && c.d.inBounds(v) {
		c.value = v
	}
}

func (c *counter) resume() {}
func (c *counter) stop()   {}

func (c *counter) change(ctx context.Context, fn func(cur int) int) error {
	return c.h.update(ctx, c, func() ([]event, error) {
		c.value = c.d.clamp(fn(c.value))
		return nil, nil
	})
}

func (h *Helpers) registerCounterServices() {
	svc(h, domCounter, "increment", "Increment", nil, func(ctx context.Context, e *counter, _ map[string]any) error {
		return e.change(ctx, func(cur int) int { return cur + e.d.step })
	})
	svc(h, domCounter, "decrement", "Decrement", nil, func(ctx context.Context, e *counter, _ map[string]any) error {
		return e.change(ctx, func(cur int) int { return cur - e.d.step })
	})
	svc(h, domCounter, "reset", "Reset", nil, func(ctx context.Context, e *counter, _ map[string]any) error {
		return e.change(ctx, func(int) int { return e.d.initial })
	})
	svc(h, domCounter, "set_value", "Set", map[string]any{"value": fld("The new counter value", true)}, func(ctx context.Context, e *counter, data map[string]any) error {
		v, ok := data["value"]
		if !ok {
			return fmt.Errorf("value is required")
		}
		n, err := toInt(v)
		if err != nil {
			return fmt.Errorf("value %w", err)
		}
		return e.change(ctx, func(int) int { return n })
	})
}
