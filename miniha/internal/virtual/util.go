/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package virtual implements HA's entities that have no device behind
// them: the template integration (sensors, switches, ... computed from
// other entities) and groups (old-style group: and platform: group).
// Both are driven purely by state_changed events on the core.
package virtual

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// EnvSource supplies the template environment (states, is_state, ...).
// automation.Engine implements it; the owner passes it in so this
// package stays independent of the automation package.
type EnvSource interface {
	TemplateEnv() *template.Env
}

// debounceDelay coalesces bursts of state_changed events (one service call
// often touches many entities) into a single re-render of all templates.
const debounceDelay = 10 * time.Millisecond

type debouncer struct {
	mu      sync.Mutex
	run     sync.Mutex
	timer   *time.Timer
	stopped bool
	fn      func()
}

func newDebouncer(fn func()) *debouncer { return &debouncer{fn: fn} }

func (d *debouncer) trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.timer != nil {
		return
	}
	d.timer = time.AfterFunc(debounceDelay, d.fire)
}

func (d *debouncer) fire() {
	d.mu.Lock()
	d.timer = nil
	stopped := d.stopped
	d.mu.Unlock()
	if stopped {
		return
	}
	// A trigger arriving while fn runs starts a new timer; the run lock
	// keeps two passes from overlapping.
	d.run.Lock()
	defer d.run.Unlock()
	d.fn()
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// flight serializes recomputation of one entity without ever blocking:
// SetState fires state_changed synchronously, so a handler may re-enter
// the same entity on the same goroutine. A re-entrant or concurrent call
// only marks the entity dirty and the running pass computes again.
type flight struct {
	mu      sync.Mutex
	running bool
	dirty   bool
	ctx     core.Context
}

func (f *flight) do(ctx core.Context, fn func(core.Context)) {
	f.mu.Lock()
	f.ctx = ctx
	if f.running {
		f.dirty = true
		f.mu.Unlock()
		return
	}
	f.running = true
	f.mu.Unlock()
	for {
		f.mu.Lock()
		cur := f.ctx
		f.mu.Unlock()
		fn(cur)
		f.mu.Lock()
		if !f.dirty {
			f.running = false
			f.mu.Unlock()
			return
		}
		f.dirty = false
		f.mu.Unlock()
	}
}

// --- config accessors ---

func asMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = e
		}
		return out, true
	}
	return nil, false
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on":
			return true, nil
		case "false", "no", "off":
			return false, nil
		}
	}
	return false, fmt.Errorf("%v is not a boolean", v)
}

// boolOpt reads an optional boolean option.
func boolOpt(m map[string]any, key string) (bool, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return false, nil
	}
	b, err := asBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

// strOpt reads an optional literal string option.
func strOpt(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

// entityList accepts a YAML list or HA's comma separated string.
func entityList(v any) ([]string, error) {
	var raw []string
	switch x := v.(type) {
	case string:
		raw = strings.Split(x, ",")
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("entity id %v is not a string", e)
			}
			raw = append(raw, s)
		}
	case []string:
		raw = x
	default:
		return nil, fmt.Errorf("entities must be a list or a comma separated string")
	}
	var out []string
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !core.ValidEntityID(s) {
			return nil, fmt.Errorf("invalid entity id %q", s)
		}
		out = append(out, s)
	}
	return out, nil
}

// checkKeys rejects options this package does not implement, so that a
// config is never silently half-applied.
func checkKeys(m map[string]any, allowed ...string) error {
	ok := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		ok[k] = true
	}
	var bad []string
	for k := range m {
		if !ok[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("unsupported option %q", bad[0])
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// --- numbers ---

func toFloat(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint64:
		f = float64(x)
	case float64:
		f = x
	case float32:
		f = float64(x)
	case string:
		p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		f = p
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func toInt(v any) (int, bool) {
	f, ok := toFloat(v)
	if !ok {
		return 0, false
	}
	return int(math.Round(f)), true
}

func pyFloat(f float64) string { return template.PyStr(f) }

// --- template results ---

var (
	intLiteral   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	floatLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// parseResult mirrors HA's literal_eval pass over a rendered template:
// ints, floats, booleans, None and Python/JSON collections become typed.
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
	if strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") || strings.HasPrefix(t, "(") {
		if v, err := parsePyLiteral(t); err == nil {
			return v
		}
	}
	return t
}

// parsePyLiteral reads the subset of Python literals a template can print:
// lists, tuples, dicts with string keys, strings, numbers, True/False/None.
// JSON is a subset of it apart from the lower-case constants accepted here.
func parsePyLiteral(s string) (any, error) {
	p := &litParser{s: s}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, fmt.Errorf("trailing data in literal")
	}
	return v, nil
}

type litParser struct {
	s string
	i int
}

func (p *litParser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *litParser) value() (any, error) {
	p.ws()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unexpected end of literal")
	}
	switch c := p.s[p.i]; {
	case c == '[' || c == '(':
		closer := byte(']')
		if c == '(' {
			closer = ')'
		}
		p.i++
		out := []any{}
		for {
			p.ws()
			if p.i < len(p.s) && p.s[p.i] == closer {
				p.i++
				return out, nil
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			p.ws()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
			}
		}
	case c == '{':
		p.i++
		out := map[string]any{}
		for {
			p.ws()
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
				return out, nil
			}
			k, err := p.value()
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("dict key %v is not a string", k)
			}
			p.ws()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return nil, fmt.Errorf("expected ':' in dict literal")
			}
			p.i++
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			out[ks] = v
			p.ws()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
			}
		}
	case c == '\'' || c == '"':
		return p.str(c)
	}
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune(",]})", rune(p.s[p.i])) {
		p.i++
	}
	tok := strings.TrimSpace(p.s[start:p.i])
	switch tok {
	case "True", "true":
		return true, nil
	case "False", "false":
		return false, nil
	case "None", "null":
		return nil, nil
	}
	if intLiteral.MatchString(tok) {
		if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
			return i, nil
		}
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("invalid literal %q", tok)
}

func (p *litParser) str(quote byte) (string, error) {
	p.i++
	var sb strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == quote:
			p.i++
			return sb.String(), nil
		case c == '\\' && p.i+1 < len(p.s):
			p.i++
			switch p.s[p.i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte(p.s[p.i])
			}
		default:
			sb.WriteByte(c)
		}
		p.i++
	}
	return "", fmt.Errorf("unterminated string literal")
}

// resultBool converts a typed template result the way HA's
// result_as_boolean does; ok is false when it is not a boolean at all.
func resultBool(v any) (on, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case int64:
		return x != 0, true
	case float64:
		return x != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on", "enable", "1":
			return true, true
		case "false", "no", "off", "disable", "0":
			return false, true
		}
	}
	return false, false
}

// templateSource turns a config scalar into template source: strings are
// templates, other scalars print as their Python literal.
func templateSource(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool, int, int64, uint64, float64:
		return template.PyStr(x), nil
	}
	return "", fmt.Errorf("%v (%T) is not a template", v, v)
}
