/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// stateObj is what templates see as states.light.bulb.
type stateObj struct {
	s *core.State
	c *core.Core
}

func (o stateObj) String() string { return o.s.State }

func (o stateObj) TemplateAttr(name string) (any, bool) {
	switch name {
	case "entity_id":
		return o.s.EntityID, true
	case "state":
		return o.s.State, true
	case "attributes":
		return template.FromGo(o.s.Attributes), true
	case "last_changed":
		return template.DateTime{T: o.s.LastChanged}, true
	case "last_updated":
		return template.DateTime{T: o.s.LastUpdated}, true
	case "last_reported":
		return template.DateTime{T: o.s.LastReported}, true
	case "domain":
		return o.s.Domain(), true
	case "object_id":
		_, obj := core.SplitEntityID(o.s.EntityID)
		return obj, true
	case "name":
		if n, ok := o.s.Attributes["friendly_name"].(string); ok {
			return n, true
		}
		_, obj := core.SplitEntityID(o.s.EntityID)
		return obj, true
	case "context":
		return template.FromGo(map[string]any{"id": o.s.Context.ID}), true
	}
	return nil, false
}

// statesObj is the "states" global: callable, attribute per domain or
// entity id, iterable over every state.
type statesObj struct{ c *core.Core }

func (s statesObj) TemplateCall(args []any, _ map[string]any) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("states() takes one entity id")
	}
	id := fmt.Sprint(args[0])
	if st := s.c.GetState(id); st != nil {
		return st.State, nil
	}
	return core.StateUnknown, nil
}

func (s statesObj) TemplateAttr(name string) (any, bool) {
	if strings.Contains(name, ".") {
		if st := s.c.GetState(name); st != nil {
			return stateObj{st, s.c}, true
		}
		return nil, true
	}
	return domainObj{s.c, name}, true
}

func (s statesObj) TemplateItems() []any {
	states := s.c.States()
	out := make([]any, 0, len(states))
	for _, st := range states {
		out = append(out, stateObj{st, s.c})
	}
	return out
}

type domainObj struct {
	c      *core.Core
	domain string
}

func (d domainObj) TemplateAttr(name string) (any, bool) {
	if st := d.c.GetState(d.domain + "." + name); st != nil {
		return stateObj{st, d.c}, true
	}
	return nil, true
}

func (d domainObj) TemplateItems() []any {
	var out []any
	for _, st := range d.c.States() {
		if st.Domain() == d.domain {
			out = append(out, stateObj{st, d.c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(stateObj).s.EntityID < out[j].(stateObj).s.EntityID })
	if out == nil {
		out = []any{}
	}
	return out
}

func (e *Engine) env() *template.Env { return EnvFor(e.core) }

// EnvFor is the template environment (states, is_state, ...) over a
// core, for packages that render templates without an engine.
func EnvFor(c *core.Core) *template.Env {
	getState := func(args []any) *core.State {
		if len(args) == 0 {
			return nil
		}
		return c.GetState(fmt.Sprint(args[0]))
	}
	return &template.Env{Globals: map[string]any{
		"states": statesObj{c},
		"is_state": template.Func(func(args []any, _ map[string]any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("is_state(entity_id, state)")
			}
			st := getState(args)
			if st == nil {
				return false, nil
			}
			for _, want := range template.ToGoList(args[1]) {
				if fmt.Sprint(want) == st.State {
					return true, nil
				}
			}
			return false, nil
		}),
		"state_attr": template.Func(func(args []any, _ map[string]any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("state_attr(entity_id, attribute)")
			}
			st := getState(args)
			if st == nil {
				return nil, nil
			}
			v, ok := st.Attributes[fmt.Sprint(args[1])]
			if !ok {
				return nil, nil
			}
			return template.FromGo(v), nil
		}),
		"is_state_attr": template.Func(func(args []any, _ map[string]any) (any, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("is_state_attr(entity_id, attribute, value)")
			}
			st := getState(args)
			if st == nil {
				return false, nil
			}
			v, ok := st.Attributes[fmt.Sprint(args[1])]
			return ok && template.Equal(template.FromGo(v), args[2]), nil
		}),
		"has_value": template.Func(func(args []any, _ map[string]any) (any, error) {
			st := getState(args)
			return st != nil && st.State != core.StateUnknown && st.State != core.StateUnavailable, nil
		}),
	}}
}

// render evaluates a template string with the run's variables and
// returns a native value the way HA's parse_result does: numbers,
// booleans, None and JSON lists or dicts come back typed.
func (e *Engine) render(src string, vars map[string]any) (any, error) {
	if !template.IsTemplate(src) {
		return src, nil
	}
	tpl, err := template.Parse(src)
	if err != nil {
		return nil, err
	}
	out, err := tpl.Render(vars, e.env())
	if err != nil {
		return nil, err
	}
	return parseResult(out), nil
}

var (
	intLiteral   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	floatLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// parseResult mirrors HA's literal_eval pass over a rendered template:
// ints, floats, booleans, None and JSON collections become typed.
func parseResult(s string) any {
	t := strings.TrimSpace(s)
	switch t {
	case "True":
		return true
	case "False":
		return false
	case "None":
		return nil
	}
	if intLiteral.MatchString(t) {
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return i
		}
	}
	if floatLiteral.MatchString(t) {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
	}
	if strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
		if v, err := template.DecodeJSON([]byte(t)); err == nil {
			return template.ToGo(v)
		}
	}
	return s
}

// renderValue renders templates found anywhere in a config value.
func (e *Engine) renderValue(v node, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return e.render(x, vars)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			r, err := e.renderValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			r, err := e.renderValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// renderBool evaluates a template as a condition, HA-style: "true",
// "yes", "on", "enable", "1" and numbers other than 0 are true.
func (e *Engine) renderBool(src string, vars map[string]any) (bool, error) {
	v, err := e.render(src, vars)
	if err != nil {
		return false, err
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on", "enable", "1":
			return true, nil
		}
		return false, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	case nil:
		return false, nil
	}
	return true, nil
}
