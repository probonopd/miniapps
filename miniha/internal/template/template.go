/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package template implements the subset of Jinja2 that Home Assistant
// templates use, with Python value semantics (int/float distinction,
// str() formatting, truthiness) so that rendered states match HA exactly.
package template

import (
	"fmt"
	"strings"
)

// Env supplies host objects such as "states" to every render.
type Env struct {
	Globals map[string]any
}

// Template is a parsed template, safe for concurrent rendering.
type Template struct {
	src   string
	nodes []node
}

// IsTemplate reports whether a string contains template syntax at all.
// HA treats such strings as templates and everything else as literals.
func IsTemplate(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "{%")
}

func Parse(src string) (*Template, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", src, err)
	}
	p := &parser{toks: toks}
	nodes, err := p.parseBody()
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", src, err)
	}
	return &Template{src: src, nodes: nodes}, nil
}

func MustParse(src string) *Template {
	t, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return t
}

func (t *Template) Source() string { return t.src }

// Render evaluates the template with the given variables (plain Go values
// are converted) and returns the output string.
func (t *Template) Render(vars map[string]any, env *Env) (string, error) {
	scope := make(map[string]any, len(vars))
	for k, v := range vars {
		scope[k] = FromGo(v)
	}
	return t.render(scope, env)
}

func (t *Template) render(scope map[string]any, env *Env) (string, error) {
	c := &evalCtx{scopes: []map[string]any{scope}, env: env}
	if err := c.execNodes(t.nodes); err != nil {
		return "", fmt.Errorf("template %q: %w", t.src, err)
	}
	return c.out.String(), nil
}

// RenderWithPossibleJSON mirrors HA's render_with_possible_json_value:
// "value" is the raw payload and "value_json" is set when it parses as
// JSON. The result is the rendered string, not a parsed value.
func (t *Template) RenderWithPossibleJSON(value string, payload []byte, extra map[string]any) (string, error) {
	scope := map[string]any{"value": value}
	if v, err := DecodeJSON(payload); err == nil {
		scope["value_json"] = v
	}
	for k, v := range extra {
		scope[k] = FromGo(v)
	}
	return t.render(scope, nil)
}

// PyStr formats a plain Go value the way Python's str() would (True, 21.5,
// None), which is how HA coerces discovery payload values to strings.
func PyStr(v any) string { return pystr(FromGo(v)) }

// Equal compares two template values with Python semantics.
func Equal(a, b any) bool { return pyEqual(a, b) }

// ToGoList returns a list value's items, or the value itself as a
// single-item list.
func ToGoList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case Iterable:
		return x.TemplateItems()
	}
	return []any{v}
}
