/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package virtual

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

const templatePlatform = "template"

// Templates runs the entities of HA's template: integration. Every
// state_changed event schedules one debounced pass that renders all
// templates again and writes only the entities whose values changed.
type Templates struct {
	c   *core.Core
	env EnvSource
	log *slog.Logger

	ents []*tplEntity

	mu    sync.Mutex
	cache map[string]*template.Template

	deb   *debouncer
	unsub func()
}

var commonKeys = []string{"name", "unique_id", "icon", "picture", "attributes", "availability", "default_entity_id", "object_id"}

// kindOrder fixes the registration order so entity ids are stable.
var kindOrder = []string{"sensor", "binary_sensor", "switch", "number", "select", "button"}

var kindKeys = map[string][]string{
	"sensor":        {"state", "unit_of_measurement", "device_class", "state_class"},
	"binary_sensor": {"state", "device_class", "delay_on", "delay_off"},
	"switch":        {"state", "turn_on", "turn_off", "device_class"},
	"number":        {"state", "set_value", "min", "max", "step", "unit_of_measurement"},
	"select":        {"state", "options", "select_option"},
	"button":        {"press", "device_class"},
}

// NewTemplates validates and registers every entity of the template:
// list. Trigger-based items and the domains this package does not
// implement are rejected, never skipped.
func NewTemplates(c *core.Core, env EnvSource, items []map[string]any, log *slog.Logger) (*Templates, error) {
	if log == nil {
		log = slog.Default()
	}
	t := &Templates{c: c, env: env, log: log, cache: map[string]*template.Template{}}
	for i, item := range items {
		ents, err := t.parseItem(item)
		if err != nil {
			return nil, fmt.Errorf("template[%d]: %w", i, err)
		}
		t.ents = append(t.ents, ents...)
	}
	seen := map[string]bool{}
	for _, e := range t.ents {
		if err := t.register(e, seen); err != nil {
			t.Stop()
			return nil, err
		}
	}
	t.deb = newDebouncer(t.refreshAll)
	t.unsub = c.Subscribe("state_changed", func(core.Event) { t.deb.trigger() })
	t.refreshAll()
	return t, nil
}

// Stop unsubscribes from the core and cancels pending timers. The
// entities stay registered and keep their last state.
func (t *Templates) Stop() {
	if t.unsub != nil {
		t.unsub()
		t.unsub = nil
	}
	if t.deb != nil {
		t.deb.stop()
	}
	for _, e := range t.ents {
		e.stop()
	}
}

func (t *Templates) refreshAll() {
	for _, e := range t.ents {
		e.refresh(core.Context{})
	}
}

func (t *Templates) parse(src string) (*template.Template, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tpl, ok := t.cache[src]; ok {
		return tpl, nil
	}
	tpl, err := template.Parse(src)
	if err != nil {
		return nil, err
	}
	t.cache[src] = tpl
	return tpl, nil
}

func (t *Templates) compile(m map[string]any, key string) (*template.Template, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, nil
	}
	src, err := templateSource(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	tpl, err := t.parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return tpl, nil
}

type namedTpl struct {
	name string
	tpl  *template.Template
}

// tplEntity is one template entity of any kind.
type tplEntity struct {
	t        *Templates
	kind     string
	entityID string
	uniqueID string
	hint     string // suggested object id

	name, state, icon, picture, avail *template.Template
	iconStatic                        string
	attrs                             []namedTpl
	unit, deviceClass, stateClass     string

	delayOn, delayOff *delay

	turnOn, turnOff, setValue, selectOption, press []step
	min, max, stepTpl, options                     *template.Template

	fl flight

	mu        sync.Mutex
	stopped   bool
	written   bool
	lastState string
	lastAttrs map[string]any
	lastErr   string
	passErr   bool
	optSwitch string
	optNumber *float64
	optSelect string
	lastPress string
	binInit   bool
	binEff    bool
	pendTimer *time.Timer
	pendGen   int
}

func (t *Templates) parseItem(item map[string]any) ([]*tplEntity, error) {
	for k := range item {
		switch k {
		case "sensor", "binary_sensor", "switch", "number", "select", "button", "unique_id":
		case "trigger":
			return nil, fmt.Errorf("trigger-based template entities are not supported")
		case "light", "cover", "fan", "lock", "vacuum", "weather", "image", "alarm_control_panel", "event":
			return nil, fmt.Errorf("template %s entities are not supported", k)
		default:
			return nil, fmt.Errorf("unsupported option %q (trigger-based items with action/variables are not supported)", k)
		}
	}
	var out []*tplEntity
	for _, kind := range kindOrder {
		raw, ok := item[kind]
		if !ok || raw == nil {
			continue
		}
		var list []any
		switch x := raw.(type) {
		case []any:
			list = x
		default:
			list = []any{raw}
		}
		for i, it := range list {
			m, ok := asMap(it)
			if !ok {
				return nil, fmt.Errorf("%s[%d] is not a mapping", kind, i)
			}
			e, err := t.newEntity(kind, m)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", kind, i, err)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func (t *Templates) newEntity(kind string, m map[string]any) (*tplEntity, error) {
	allowed := append(append([]string{}, commonKeys...), kindKeys[kind]...)
	if err := checkKeys(m, allowed...); err != nil {
		return nil, err
	}
	e := &tplEntity{t: t, kind: kind}
	var err error
	if e.name, err = t.compile(m, "name"); err != nil {
		return nil, err
	}
	if e.state, err = t.compile(m, "state"); err != nil {
		return nil, err
	}
	if e.icon, err = t.compile(m, "icon"); err != nil {
		return nil, err
	}
	if e.picture, err = t.compile(m, "picture"); err != nil {
		return nil, err
	}
	if e.avail, err = t.compile(m, "availability"); err != nil {
		return nil, err
	}
	if v, ok := m["icon"].(string); ok && !template.IsTemplate(v) {
		e.iconStatic = v
	}
	if e.uniqueID, err = strOpt(m, "unique_id"); err != nil {
		return nil, err
	}
	for _, key := range []string{"object_id", "default_entity_id"} {
		s, err := strOpt(m, key)
		if err != nil {
			return nil, err
		}
		if s != "" && e.hint == "" {
			_, e.hint = core.SplitEntityID(s)
			if !strings.Contains(s, ".") {
				e.hint = s
			}
		}
	}
	if e.unit, err = strOpt(m, "unit_of_measurement"); err != nil {
		return nil, err
	}
	if e.deviceClass, err = strOpt(m, "device_class"); err != nil {
		return nil, err
	}
	if e.stateClass, err = strOpt(m, "state_class"); err != nil {
		return nil, err
	}
	if a, ok := m["attributes"]; ok && a != nil {
		am, ok := asMap(a)
		if !ok {
			return nil, fmt.Errorf("attributes must be a mapping")
		}
		for k, v := range am {
			src, err := templateSource(v)
			if err != nil {
				return nil, fmt.Errorf("attributes.%s: %w", k, err)
			}
			tpl, err := t.parse(src)
			if err != nil {
				return nil, fmt.Errorf("attributes.%s: %w", k, err)
			}
			e.attrs = append(e.attrs, namedTpl{k, tpl})
		}
		sort.Slice(e.attrs, func(i, j int) bool { return e.attrs[i].name < e.attrs[j].name })
	}
	needState := func() error {
		if e.state == nil {
			return fmt.Errorf("state is required")
		}
		return nil
	}
	steps := func(key string, dst *[]step, required bool) error {
		s, err := parseSteps(m[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if required && len(s) == 0 {
			return fmt.Errorf("%s is required", key)
		}
		*dst = s
		return nil
	}
	switch kind {
	case "sensor":
		err = needState()
	case "binary_sensor":
		if err = needState(); err != nil {
			break
		}
		if e.delayOn, err = parseDelay(m["delay_on"]); err != nil {
			err = fmt.Errorf("delay_on: %w", err)
			break
		}
		if e.delayOff, err = parseDelay(m["delay_off"]); err != nil {
			err = fmt.Errorf("delay_off: %w", err)
		}
	case "switch":
		if err = steps("turn_on", &e.turnOn, true); err == nil {
			err = steps("turn_off", &e.turnOff, true)
		}
	case "number":
		if err = steps("set_value", &e.setValue, true); err != nil {
			break
		}
		for key, dst := range map[string]**template.Template{"min": &e.min, "max": &e.max, "step": &e.stepTpl} {
			if *dst, err = t.compile(m, key); err != nil {
				break
			}
		}
	case "select":
		if e.options, err = t.compile(m, "options"); err != nil {
			break
		}
		if e.options == nil {
			err = fmt.Errorf("options is required")
			break
		}
		err = steps("select_option", &e.selectOption, true)
	case "button":
		err = steps("press", &e.press, true)
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// register adds the entity to the registry and binds its controller.
func (t *Templates) register(e *tplEntity, seen map[string]bool) error {
	env := t.env.TemplateEnv()
	name := ""
	if e.name != nil {
		out, err := e.name.Render(map[string]any{}, env)
		if err != nil {
			return fmt.Errorf("%s name: %w", e.kind, err)
		}
		name = strings.TrimSpace(out)
	}
	uid := e.uniqueID
	switch {
	case uid != "":
		if seen[uid] {
			return fmt.Errorf("duplicate unique_id %q", uid)
		}
	case name == "":
		return fmt.Errorf("template %s needs a name or a unique_id", e.kind)
	default:
		// The unique id space of the core registry is not split per
		// domain, so a sensor and a binary_sensor with one name need
		// distinct ids.
		uid = "template:" + name
		if seen[uid] {
			uid = "template:" + e.kind + ":" + name
		}
		if seen[uid] {
			return fmt.Errorf("duplicate template %s name %q: set unique_id", e.kind, name)
		}
	}
	seen[uid] = true
	e.uniqueID = uid
	entry, err := t.c.RegisterEntity(core.EntityRegistration{
		Domain: e.kind, Platform: templatePlatform, UniqueID: uid, SuggestedObjectID: e.hint,
		OriginalName: name, OriginalIcon: e.iconStatic, DeviceClass: e.deviceClass, Unit: e.unit,
	})
	if err != nil {
		return err
	}
	e.entityID = entry.EntityID
	switch e.kind {
	case "switch":
		t.c.RegisterController(e.entityID, tplSwitch{e})
	case "number":
		t.c.RegisterController(e.entityID, tplNumber{e})
	case "select":
		t.c.RegisterController(e.entityID, tplSelect{e})
	case "button":
		t.c.RegisterController(e.entityID, tplButton{e})
	}
	return nil
}

func (e *tplEntity) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = true
	e.cancelPendingLocked()
}

func (e *tplEntity) cancelPendingLocked() {
	e.pendGen++
	if e.pendTimer != nil {
		e.pendTimer.Stop()
		e.pendTimer = nil
	}
}

// refresh renders the entity and writes the result when it differs from
// what was written last.
func (e *tplEntity) refresh(ctx core.Context) {
	e.fl.do(ctx, func(ctx core.Context) {
		env := e.t.env.TemplateEnv()
		e.mu.Lock()
		if e.stopped {
			e.mu.Unlock()
			return
		}
		state, attrs := e.compute(env)
		if e.written && e.lastState == state && reflect.DeepEqual(e.lastAttrs, attrs) {
			e.mu.Unlock()
			return
		}
		e.lastState, e.lastAttrs, e.written = state, attrs, true
		e.mu.Unlock()
		// Written outside the lock: subscribers run synchronously and may
		// call back into this entity's controller.
		if err := e.t.c.SetState(e.entityID, state, attrs, ctx); err != nil {
			e.t.log.Error("template entity state rejected", "entity_id", e.entityID, "err", err)
			e.mu.Lock()
			e.written = false
			e.mu.Unlock()
		}
	})
}

// thisObj is the "this" template variable: the entity's own state.
type thisObj struct {
	c  *core.Core
	id string
}

func (o thisObj) String() string {
	if st := o.c.GetState(o.id); st != nil {
		return st.State
	}
	return core.StateUnknown
}

func (o thisObj) TemplateAttr(name string) (any, bool) {
	st := o.c.GetState(o.id)
	switch name {
	case "entity_id":
		return o.id, true
	case "state":
		if st == nil {
			return core.StateUnknown, true
		}
		return st.State, true
	case "attributes":
		if st == nil {
			return template.FromGo(map[string]any{}), true
		}
		return template.FromGo(st.Attributes), true
	case "last_changed":
		if st != nil {
			return template.DateTime{T: st.LastChanged}, true
		}
	case "last_updated":
		if st != nil {
			return template.DateTime{T: st.LastUpdated}, true
		}
	}
	return nil, false
}

func (e *tplEntity) vars() map[string]any {
	return map[string]any{"this": thisObj{e.t.c, e.entityID}}
}

// fail records a render error once per distinct message so that a broken
// template does not flood the log on every state change.
func (e *tplEntity) fail(what string, err error) {
	e.passErr = true
	msg := what + ": " + err.Error()
	if msg != e.lastErr {
		e.lastErr = msg
		e.t.log.Warn("template entity render failed", "entity_id", e.entityID, "err", msg)
	}
}

func (e *tplEntity) typed(tpl *template.Template, env *template.Env, vars map[string]any) (any, error) {
	out, err := tpl.Render(vars, env)
	if err != nil {
		return nil, err
	}
	return parseResult(out), nil
}

func (e *tplEntity) staticAttrs() map[string]any {
	a := map[string]any{}
	if e.unit != "" {
		a["unit_of_measurement"] = e.unit
	}
	if e.deviceClass != "" {
		a["device_class"] = e.deviceClass
	}
	if e.stateClass != "" {
		a["state_class"] = e.stateClass
	}
	return a
}

// compute must run with e.mu held.
func (e *tplEntity) compute(env *template.Env) (string, map[string]any) {
	e.passErr = false
	defer func() {
		if !e.passErr {
			e.lastErr = ""
		}
	}()
	vars := e.vars()
	attrs := e.staticAttrs()
	if e.avail != nil {
		v, err := e.typed(e.avail, env, vars)
		on, ok := resultBool(v)
		if err != nil {
			e.fail("availability", err)
		} else if !ok {
			e.fail("availability", fmt.Errorf("%v is not a boolean", v))
		}
		if err != nil || !ok || !on {
			e.cancelPendingLocked()
			return core.StateUnavailable, attrs
		}
	}
	var state string
	var err error
	switch e.kind {
	case "sensor":
		state, err = e.sensorState(env, vars)
	case "binary_sensor":
		state, err = e.binaryState(env, vars)
	case "switch":
		state, err = e.switchState(env, vars)
	case "number":
		state, err = e.numberState(env, vars, attrs)
	case "select":
		state, err = e.selectState(env, vars, attrs)
	case "button":
		state = core.StateUnknown
		if e.lastPress != "" {
			state = e.lastPress
		}
	}
	if err != nil {
		e.fail("state", err)
		state = core.StateUnknown
	}
	if e.kind == "switch" && e.state == nil {
		attrs["assumed_state"] = true
	}
	if e.icon != nil {
		if v, err := e.typed(e.icon, env, vars); err != nil {
			e.fail("icon", err)
		} else if v != nil {
			attrs["icon"] = v
		}
	}
	if e.picture != nil {
		if v, err := e.typed(e.picture, env, vars); err != nil {
			e.fail("picture", err)
		} else if v != nil {
			attrs["entity_picture"] = v
		}
	}
	for _, a := range e.attrs {
		v, err := e.typed(a.tpl, env, vars)
		if err != nil {
			e.fail("attribute "+a.name, err)
			continue
		}
		attrs[a.name] = v
	}
	return state, attrs
}

func (e *tplEntity) sensorState(env *template.Env, vars map[string]any) (string, error) {
	out, err := e.state.Render(vars, env)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "None" {
		return core.StateUnknown, nil
	}
	return out, nil
}

func (e *tplEntity) binaryState(env *template.Env, vars map[string]any) (string, error) {
	v, err := e.typed(e.state, env, vars)
	if err != nil {
		return "", err
	}
	raw, ok := resultBool(v)
	if !ok {
		if s, isStr := v.(string); v == nil || isStr && (s == core.StateUnknown || s == core.StateUnavailable) {
			e.cancelPendingLocked()
			return core.StateUnknown, nil
		}
		return "", fmt.Errorf("%v is not a boolean", v)
	}
	switch {
	case !e.binInit:
		e.binInit, e.binEff = true, raw
	case raw == e.binEff:
		e.cancelPendingLocked()
	default:
		d := e.delayOn
		if !raw {
			d = e.delayOff
		}
		dur, err := d.value(env, vars)
		if err != nil {
			return "", err
		}
		if dur <= 0 {
			e.cancelPendingLocked()
			e.binEff = raw
		} else if e.pendTimer == nil {
			// A pending transition to this value keeps running; the
			// opposite value cancels it above.
			gen := e.pendGen
			e.pendTimer = time.AfterFunc(dur, func() {
				e.mu.Lock()
				if e.stopped || e.pendGen != gen {
					e.mu.Unlock()
					return
				}
				e.binEff = raw
				e.pendTimer = nil
				e.mu.Unlock()
				e.refresh(core.Context{})
			})
		}
	}
	if e.binEff {
		return core.StateOn, nil
	}
	return core.StateOff, nil
}

func (e *tplEntity) switchState(env *template.Env, vars map[string]any) (string, error) {
	if e.state == nil {
		if e.optSwitch == "" {
			return core.StateUnknown, nil
		}
		return e.optSwitch, nil
	}
	v, err := e.typed(e.state, env, vars)
	if err != nil {
		return "", err
	}
	on, ok := resultBool(v)
	if !ok {
		if s, isStr := v.(string); v == nil || isStr && (s == core.StateUnknown || s == core.StateUnavailable) {
			return core.StateUnknown, nil
		}
		return "", fmt.Errorf("%v is not a boolean", v)
	}
	if on {
		return core.StateOn, nil
	}
	return core.StateOff, nil
}

func (e *tplEntity) renderFloat(tpl *template.Template, def float64, env *template.Env, vars map[string]any) (float64, error) {
	if tpl == nil {
		return def, nil
	}
	out, err := tpl.Render(vars, env)
	if err != nil {
		return 0, err
	}
	f, ok := toFloat(out)
	if !ok {
		return 0, fmt.Errorf("%q is not a number", strings.TrimSpace(out))
	}
	return f, nil
}

func (e *tplEntity) numberBounds(env *template.Env, vars map[string]any) (lo, hi, step float64, err error) {
	if lo, err = e.renderFloat(e.min, 0, env, vars); err != nil {
		return 0, 0, 0, fmt.Errorf("min: %w", err)
	}
	if hi, err = e.renderFloat(e.max, 100, env, vars); err != nil {
		return 0, 0, 0, fmt.Errorf("max: %w", err)
	}
	if step, err = e.renderFloat(e.stepTpl, 1, env, vars); err != nil {
		return 0, 0, 0, fmt.Errorf("step: %w", err)
	}
	return lo, hi, step, nil
}

func (e *tplEntity) numberState(env *template.Env, vars map[string]any, attrs map[string]any) (string, error) {
	lo, hi, step, err := e.numberBounds(env, vars)
	if err != nil {
		return "", err
	}
	attrs["min"], attrs["max"], attrs["step"], attrs["mode"] = lo, hi, step, "auto"
	if e.state == nil {
		if e.optNumber == nil {
			return core.StateUnknown, nil
		}
		return pyFloat(*e.optNumber), nil
	}
	out, err := e.state.Render(vars, env)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	switch out {
	case "None", core.StateUnknown, core.StateUnavailable:
		return core.StateUnknown, nil
	}
	f, ok := toFloat(out)
	if !ok {
		return "", fmt.Errorf("%q is not a number", out)
	}
	return pyFloat(f), nil
}

func (e *tplEntity) optionList(env *template.Env, vars map[string]any) ([]string, error) {
	v, err := e.typed(e.options, env, vars)
	if err != nil {
		return nil, err
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("options must render to a list, got %v", v)
	}
	out := make([]string, len(list))
	for i, o := range list {
		if s, ok := o.(string); ok {
			out[i] = s
		} else {
			out[i] = template.PyStr(o)
		}
	}
	return out, nil
}

func (e *tplEntity) selectState(env *template.Env, vars map[string]any, attrs map[string]any) (string, error) {
	opts, err := e.optionList(env, vars)
	if err != nil {
		return "", fmt.Errorf("options: %w", err)
	}
	attrs["options"] = anyList(opts)
	cur := e.optSelect
	if e.state != nil {
		out, err := e.state.Render(vars, env)
		if err != nil {
			return "", err
		}
		cur = strings.TrimSpace(out)
	}
	switch cur {
	case "", "None", core.StateUnknown, core.StateUnavailable:
		return core.StateUnknown, nil
	}
	for _, o := range opts {
		if o == cur {
			return cur, nil
		}
	}
	return "", fmt.Errorf("%q is not one of the options %v", cur, opts)
}

// --- controllers ---

type tplSwitch struct{ e *tplEntity }

func (s tplSwitch) TurnOn(ctx context.Context, _ map[string]any) error {
	return s.e.setSwitch(ctx, true)
}

func (s tplSwitch) TurnOff(ctx context.Context, _ map[string]any) error {
	return s.e.setSwitch(ctx, false)
}

func (e *tplEntity) setSwitch(ctx context.Context, on bool) error {
	steps, st := e.turnOff, core.StateOff
	if on {
		steps, st = e.turnOn, core.StateOn
	}
	if err := e.t.runSteps(ctx, steps, e.vars()); err != nil {
		return err
	}
	if e.state == nil {
		e.mu.Lock()
		e.optSwitch = st
		e.mu.Unlock()
	}
	e.refresh(core.ContextFrom(ctx))
	return nil
}

type tplNumber struct{ e *tplEntity }

func (n tplNumber) SetValue(ctx context.Context, value any) error {
	e := n.e
	f, ok := toFloat(value)
	if !ok {
		return fmt.Errorf("%v is not a number", value)
	}
	env := e.t.env.TemplateEnv()
	lo, hi, _, err := e.numberBounds(env, e.vars())
	if err != nil {
		return err
	}
	if f < lo || f > hi {
		return fmt.Errorf("value %s is outside %s..%s", pyFloat(f), pyFloat(lo), pyFloat(hi))
	}
	vars := e.vars()
	vars["value"] = f
	if err := e.t.runSteps(ctx, e.setValue, vars); err != nil {
		return err
	}
	if e.state == nil {
		e.mu.Lock()
		e.optNumber = &f
		e.mu.Unlock()
	}
	e.refresh(core.ContextFrom(ctx))
	return nil
}

type tplSelect struct{ e *tplEntity }

func (s tplSelect) SelectOption(ctx context.Context, option string) error {
	e := s.e
	opts, err := e.optionList(e.t.env.TemplateEnv(), e.vars())
	if err != nil {
		return fmt.Errorf("options: %w", err)
	}
	found := false
	for _, o := range opts {
		found = found || o == option
	}
	if !found {
		return fmt.Errorf("option %q is not one of %v", option, opts)
	}
	vars := e.vars()
	vars["option"] = option
	if err := e.t.runSteps(ctx, e.selectOption, vars); err != nil {
		return err
	}
	if e.state == nil {
		e.mu.Lock()
		e.optSelect = option
		e.mu.Unlock()
	}
	e.refresh(core.ContextFrom(ctx))
	return nil
}

type tplButton struct{ e *tplEntity }

func (b tplButton) Press(ctx context.Context) error {
	e := b.e
	if err := e.t.runSteps(ctx, e.press, e.vars()); err != nil {
		return err
	}
	e.mu.Lock()
	e.lastPress = e.t.c.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
	e.mu.Unlock()
	e.refresh(core.ContextFrom(ctx))
	return nil
}

// --- durations ---

// delay is a binary_sensor delay_on/delay_off value: a number of seconds,
// an HH:MM:SS string, a mapping of time units, or a template of those.
type delay struct {
	static time.Duration
	tpl    *template.Template
}

func parseDelay(v any) (*delay, error) {
	switch x := v.(type) {
	case nil:
		return &delay{}, nil
	case string:
		if !template.IsTemplate(x) {
			d, err := parseDurationString(x)
			if err != nil {
				return nil, err
			}
			return &delay{static: d}, nil
		}
		tpl, err := template.Parse(x)
		if err != nil {
			return nil, err
		}
		return &delay{tpl: tpl}, nil
	case map[string]any, map[any]any:
		m, _ := asMap(x)
		var d time.Duration
		units := map[string]time.Duration{"days": 24 * time.Hour, "hours": time.Hour, "minutes": time.Minute, "seconds": time.Second, "milliseconds": time.Millisecond}
		for k, val := range m {
			u, ok := units[k]
			f, isNum := toFloat(val)
			if !ok || !isNum {
				return nil, fmt.Errorf("unsupported duration field %q", k)
			}
			d += time.Duration(f * float64(u))
		}
		return &delay{static: d}, nil
	}
	f, ok := toFloat(v)
	if !ok {
		return nil, fmt.Errorf("%v is not a duration", v)
	}
	return &delay{static: time.Duration(f * float64(time.Second))}, nil
}

func parseDurationString(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if f, ok := toFloat(s); ok {
		return time.Duration(f * float64(time.Second)), nil
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("%q is not a duration", s)
	}
	var total float64
	mult := []float64{3600, 60, 1}
	if len(parts) == 2 {
		mult = []float64{60, 1}
	}
	for i, p := range parts {
		f, ok := toFloat(p)
		if !ok || f < 0 {
			return 0, fmt.Errorf("%q is not a duration", s)
		}
		total += f * mult[i]
	}
	return time.Duration(total * float64(time.Second)), nil
}

func (d *delay) value(env *template.Env, vars map[string]any) (time.Duration, error) {
	if d == nil || d.tpl == nil {
		if d == nil {
			return 0, nil
		}
		return d.static, nil
	}
	out, err := d.tpl.Render(vars, env)
	if err != nil {
		return 0, err
	}
	return parseDurationString(out)
}
