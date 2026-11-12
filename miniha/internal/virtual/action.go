/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package virtual

import (
	"context"
	"fmt"
	"strings"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// step is one service call of an HA action sequence. Only plain service
// calls are implemented; flow control (choose, delay, ...) is rejected at
// load time because running half a script would be worse than not at all.
type step struct {
	service string
	target  map[string]any
	data    map[string]any
	entity  any
}

var stepKeys = []string{"alias", "service", "action", "target", "data", "data_template", "entity_id", "metadata", "enabled", "continue_on_error"}

// parseSteps reads an action list (or a single action) from config.
func parseSteps(v any) ([]step, error) {
	if v == nil {
		return nil, nil
	}
	var items []any
	switch x := v.(type) {
	case []any:
		items = x
	default:
		items = []any{v}
	}
	var out []step
	for i, it := range items {
		m, ok := asMap(it)
		if !ok {
			return nil, fmt.Errorf("action %d is not a mapping", i)
		}
		if err := checkKeys(m, stepKeys...); err != nil {
			return nil, fmt.Errorf("action %d: %w (only service calls are supported)", i, err)
		}
		if en, ok := m["enabled"]; ok {
			on, err := asBool(en)
			if err != nil {
				return nil, fmt.Errorf("action %d: enabled: %w", i, err)
			}
			if !on {
				continue
			}
		}
		svc, _ := m["service"].(string)
		if act, ok := m["action"].(string); ok {
			svc = act
		}
		if svc == "" {
			return nil, fmt.Errorf("action %d has no service", i)
		}
		s := step{service: svc, entity: m["entity_id"]}
		if t, ok := m["target"]; ok && t != nil {
			tm, ok := asMap(t)
			if !ok {
				return nil, fmt.Errorf("action %d: target must be a mapping", i)
			}
			s.target = tm
		}
		for _, key := range []string{"data", "data_template"} {
			if d, ok := m[key]; ok && d != nil {
				dm, ok := asMap(d)
				if !ok {
					return nil, fmt.Errorf("action %d: %s must be a mapping", i, key)
				}
				if s.data == nil {
					s.data = map[string]any{}
				}
				for k, e := range dm {
					s.data[k] = e
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// run executes the steps in order through the service registry; vars are
// the template variables of this run (value, option, this).
func (t *Templates) runSteps(ctx context.Context, steps []step, vars map[string]any) error {
	env := t.env.TemplateEnv()
	for i, s := range steps {
		if err := t.runStep(ctx, s, vars, env); err != nil {
			return fmt.Errorf("action %d (%s): %w", i, s.service, err)
		}
	}
	return nil
}

func (t *Templates) runStep(ctx context.Context, s step, vars map[string]any, env *template.Env) error {
	svcAny, err := t.renderValue(env, s.service, vars)
	if err != nil {
		return err
	}
	svc, _ := svcAny.(string)
	domain, name, ok := strings.Cut(svc, ".")
	if !ok || domain == "" || name == "" {
		return fmt.Errorf("service %q is not of the form domain.service", svc)
	}
	data := map[string]any{}
	if s.data != nil {
		r, err := t.renderValue(env, s.data, vars)
		if err != nil {
			return err
		}
		for k, v := range r.(map[string]any) {
			data[k] = v
		}
	}
	// HA merges target into the service data; core resolves entity_id,
	// device_id and area_id from the top level.
	if s.target != nil {
		r, err := t.renderValue(env, s.target, vars)
		if err != nil {
			return err
		}
		for k, v := range r.(map[string]any) {
			data[k] = v
		}
	}
	if s.entity != nil {
		v, err := t.renderValue(env, s.entity, vars)
		if err != nil {
			return err
		}
		data["entity_id"] = v
	}
	_, err = t.c.CallService(ctx, domain, name, data, core.ContextFrom(ctx))
	return err
}

// renderValue renders templates found anywhere in a config value and
// returns typed results.
func (t *Templates) renderValue(env *template.Env, v any, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if !template.IsTemplate(x) {
			return x, nil
		}
		tpl, err := t.parse(x)
		if err != nil {
			return nil, err
		}
		out, err := tpl.Render(vars, env)
		if err != nil {
			return nil, err
		}
		return parseResult(out), nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			r, err := t.renderValue(env, item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case map[any]any:
		m, _ := asMap(x)
		return t.renderValue(env, m, vars)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			r, err := t.renderValue(env, item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}
