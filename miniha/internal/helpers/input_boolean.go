/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"

	"github.com/probonopd/miniha/internal/core"
)

type inputBooleanDef struct {
	name, icon string
	initial    *bool
}

type inputBoolean struct {
	base
	d  inputBooleanDef
	on bool
}

func parseInputBoolean(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputBoolean, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputBooleanDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	if d.initial, err = o.boolean("initial"); err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	return &inputBoolean{base: newBase(h, domInputBoolean, id, d.name, d.icon), d: d}, nil
}

func (i *inputBoolean) def() any { return i.d }

func (i *inputBoolean) view() (string, map[string]any) {
	st := core.StateOff
	if i.on {
		st = core.StateOn
	}
	return st, map[string]any{"editable": true}
}

func (i *inputBoolean) record() record {
	st, _ := i.view()
	return record{State: st}
}

// An explicit initial wins over the persisted value, as in HA, so that a
// helper declared with initial: true is on after every restart.
func (i *inputBoolean) restore(r *record, reload bool) {
	i.on = false
	if i.d.initial != nil && !reload {
		i.on = *i.d.initial
		return
	}
	if r != nil && (r.State == core.StateOn || r.State == core.StateOff) {
		i.on = r.State == core.StateOn
		return
	}
	if i.d.initial != nil {
		i.on = *i.d.initial
	}
}

func (i *inputBoolean) resume() {}
func (i *inputBoolean) stop()   {}

func (i *inputBoolean) set(ctx context.Context, on bool) error {
	return i.h.update(ctx, i, func() ([]event, error) {
		i.on = on
		return nil, nil
	})
}

func (i *inputBoolean) TurnOn(ctx context.Context, _ map[string]any) error {
	return i.set(ctx, true)
}

func (i *inputBoolean) TurnOff(ctx context.Context, _ map[string]any) error {
	return i.set(ctx, false)
}
