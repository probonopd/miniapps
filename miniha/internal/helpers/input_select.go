/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type inputSelectDef struct {
	name, icon string
	options    []string
	initial    string
	hasInitial bool
}

type inputSelect struct {
	base
	d       inputSelectDef
	options []string // runtime list; set_options replaces it until the next restart
	current string
}

func parseInputSelect(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputSelect, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputSelectDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	rawOpts, ok := o.get("options")
	if !ok {
		return nil, o.errf("options", "is required")
	}
	if d.options, err = parseOptions(rawOpts); err != nil {
		return nil, o.errf("options", "%v", err)
	}
	if v, ok := o.get("initial"); ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, o.errf("initial", "must be a string, got %T", v)
		}
		if !slices.Contains(d.options, s) {
			return nil, o.errf("initial", "%q is not one of the options", s)
		}
		d.initial, d.hasInitial = s, true
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	return &inputSelect{base: newBase(h, domInputSelect, id, d.name, d.icon), d: d}, nil
}

func (s *inputSelect) def() any { return s.d }

func (s *inputSelect) view() (string, map[string]any) {
	return s.current, map[string]any{"options": slices.Clone(s.options), "editable": true}
}

func (s *inputSelect) record() record { return record{State: s.current} }

func (s *inputSelect) restore(r *record, reload bool) {
	s.options = slices.Clone(s.d.options)
	s.current = s.options[0]
	if s.d.hasInitial {
		s.current = s.d.initial
		if !reload {
			return
		}
	}
	if r != nil && slices.Contains(s.options, r.State) {
		s.current = r.State
	}
}

func (s *inputSelect) resume() {}
func (s *inputSelect) stop()   {}

func (s *inputSelect) selectLocked(option string) error {
	if !slices.Contains(s.options, option) {
		return fmt.Errorf("option %q is not valid (possible options: %s)", option, strings.Join(s.options, ", "))
	}
	s.current = option
	return nil
}

// SelectOption implements core.OptionSelector.
func (s *inputSelect) SelectOption(ctx context.Context, option string) error {
	return s.h.update(ctx, s, func() ([]event, error) {
		return nil, s.selectLocked(option)
	})
}

// offset moves through the list; without cycling it stops at the ends.
func (s *inputSelect) offset(ctx context.Context, by int, cycle bool) error {
	return s.h.update(ctx, s, func() ([]event, error) {
		n := len(s.options)
		i := slices.Index(s.options, s.current) + by
		if cycle {
			i = ((i % n) + n) % n
		} else {
			i = max(0, min(n-1, i))
		}
		s.current = s.options[i]
		return nil, nil
	})
}

func (s *inputSelect) edge(ctx context.Context, last bool) error {
	return s.h.update(ctx, s, func() ([]event, error) {
		if last {
			s.current = s.options[len(s.options)-1]
		} else {
			s.current = s.options[0]
		}
		return nil, nil
	})
}

// setOptions keeps the current option when it is still listed and falls
// back to the first one otherwise, as HA does.
func (s *inputSelect) setOptions(ctx context.Context, options []string) error {
	return s.h.update(ctx, s, func() ([]event, error) {
		s.options = slices.Clone(options)
		if !slices.Contains(s.options, s.current) {
			s.current = s.options[0]
		}
		return nil, nil
	})
}

func cycleArg(data map[string]any) (bool, error) {
	v, ok := data["cycle"]
	if !ok || v == nil {
		return true, nil
	}
	b, err := toBool(v)
	if err != nil {
		return false, fmt.Errorf("cycle %w", err)
	}
	return b, nil
}

func (h *Helpers) registerInputSelectServices() {
	svc(h, domInputSelect, "select_option", "Select", map[string]any{"option": fld("Option to be selected", true)}, func(ctx context.Context, e *inputSelect, data map[string]any) error {
		o, ok := data["option"].(string)
		if !ok {
			return fmt.Errorf("option must be a string")
		}
		return e.SelectOption(ctx, o)
	})
	cycle := map[string]any{"cycle": fld("If the option should cycle from the last to the first (default true)", false)}
	svc(h, domInputSelect, "select_next", "Next", cycle, func(ctx context.Context, e *inputSelect, data map[string]any) error {
		c, err := cycleArg(data)
		if err != nil {
			return err
		}
		return e.offset(ctx, 1, c)
	})
	svc(h, domInputSelect, "select_previous", "Previous", cycle, func(ctx context.Context, e *inputSelect, data map[string]any) error {
		c, err := cycleArg(data)
		if err != nil {
			return err
		}
		return e.offset(ctx, -1, c)
	})
	svc(h, domInputSelect, "select_first", "First", nil, func(ctx context.Context, e *inputSelect, _ map[string]any) error {
		return e.edge(ctx, false)
	})
	svc(h, domInputSelect, "select_last", "Last", nil, func(ctx context.Context, e *inputSelect, _ map[string]any) error {
		return e.edge(ctx, true)
	})
	svc(h, domInputSelect, "set_options", "Set options", map[string]any{"options": fld("Options for the input select entity", true)}, func(ctx context.Context, e *inputSelect, data map[string]any) error {
		v, ok := data["options"]
		if !ok {
			return fmt.Errorf("options is required")
		}
		opts, err := parseOptions(v)
		if err != nil {
			return fmt.Errorf("options %w", err)
		}
		return e.setOptions(ctx, opts)
	})
}
