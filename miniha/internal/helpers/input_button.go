/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"time"
)

// buttonTimeLayout is how HA renders the press time of a button: ISO 8601
// with microseconds and a numeric offset.
const buttonTimeLayout = "2006-01-02T15:04:05.000000-07:00"

type inputButtonDef struct {
	name, icon string
}

type inputButton struct {
	base
	d       inputButtonDef
	pressed string
}

func parseInputButton(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputButton, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputButtonDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	return &inputButton{base: newBase(h, domInputButton, id, d.name, d.icon), d: d}, nil
}

func (i *inputButton) def() any { return i.d }

func (i *inputButton) view() (string, map[string]any) {
	return i.pressed, map[string]any{"editable": true}
}

func (i *inputButton) record() record { return record{State: i.pressed} }

func (i *inputButton) restore(r *record, _ bool) {
	i.pressed = "unknown"
	if r == nil {
		return
	}
	if _, err := time.Parse(buttonTimeLayout, r.State); err == nil {
		i.pressed = r.State
	}
}

func (i *inputButton) resume() {}
func (i *inputButton) stop()   {}

func (i *inputButton) Press(ctx context.Context) error {
	return i.h.update(ctx, i, func() ([]event, error) {
		i.pressed = i.h.c.Now().UTC().Format(buttonTimeLayout)
		return nil, nil
	})
}

func (h *Helpers) registerInputButtonServices() {
	svc(h, domInputButton, "press", "Press", nil, func(ctx context.Context, e *inputButton, _ map[string]any) error {
		return e.Press(ctx)
	})
}
