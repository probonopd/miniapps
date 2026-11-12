/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/mqtt"
	"github.com/probonopd/miniha/internal/store"
	"github.com/probonopd/miniha/internal/template"
)

// Files the engine reads from the data directory.
var Files = []string{"automations.yaml", "scripts.yaml", "scenes.yaml"}

type Options struct {
	Dir       string
	Location  *time.Location
	Latitude  float64
	Longitude float64
	// Transports feed mqtt triggers; every configured broker is one.
	Transports []mqtt.Transport
}

type Engine struct {
	core *core.Core
	log  *slog.Logger
	opts Options
	now  func() time.Time

	mu          sync.Mutex
	automations map[string]*automation // by entity_id
	scripts     map[string]*script
	scenes      map[string]*scene
	enabled     map[string]bool // automation on/off across reloads
	mtimes      map[string]time.Time
	bpSig       string // blueprint files as of the last reload
	started     bool
	stopWatch   chan struct{}
	unsubs      []func()
}

func New(c *core.Core, opts Options, log *slog.Logger) *Engine {
	if opts.Location == nil {
		opts.Location = time.Local
	}
	e := &Engine{core: c, log: log, opts: opts, now: func() time.Time { return time.Now().In(opts.Location) },
		automations: map[string]*automation{}, scripts: map[string]*script{}, scenes: map[string]*scene{}, enabled: map[string]bool{}, mtimes: map[string]time.Time{}}
	template.Clock = e.now
	e.registerServices()
	return e
}

func (e *Engine) hasLocation() bool { return e.opts.Latitude != 0 || e.opts.Longitude != 0 }

// enabledPath persists which automations were switched off, so a
// restart does not turn them back on.
func (e *Engine) enabledPath() string { return filepath.Join(e.opts.Dir, "automation_state.json") }

func (e *Engine) loadEnabled() {
	raw, err := os.ReadFile(e.enabledPath())
	if err != nil {
		return
	}
	var m map[string]bool
	if json.Unmarshal(raw, &m) == nil {
		e.mu.Lock()
		for k, v := range m {
			e.enabled[k] = v
		}
		e.mu.Unlock()
	}
}

func (e *Engine) saveEnabled() {
	e.mu.Lock()
	raw, err := json.MarshalIndent(e.enabled, "", " ")
	e.mu.Unlock()
	if err == nil {
		if err := store.WriteAtomic(e.enabledPath(), raw, 0o644); err != nil {
			e.log.Error("saving automation state failed", "err", err)
		}
	}
}

// Start loads the files, fires the homeassistant start trigger and
// begins watching the files for changes.
func (e *Engine) Start() error {
	e.loadEnabled()
	if err := e.Reload(); err != nil {
		return err
	}
	e.mu.Lock()
	e.started = true
	e.stopWatch = make(chan struct{})
	e.mu.Unlock()
	e.core.Fire("homeassistant_start", nil, core.NewContext())
	go e.watch()
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if e.stopWatch != nil {
		close(e.stopWatch)
		e.stopWatch = nil
	}
	e.started = false
	e.mu.Unlock()
	e.core.Fire("homeassistant_stop", nil, core.NewContext())
	e.mu.Lock()
	for _, a := range e.automations {
		a.stop()
	}
	for _, s := range e.scripts {
		s.runs.cancelAll()
	}
	e.mu.Unlock()
}

// watch reloads when any of the files changes on disk.
func (e *Engine) watch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		e.mu.Lock()
		stop := e.stopWatch
		e.mu.Unlock()
		select {
		case <-stop:
			return
		case <-t.C:
			if e.filesChanged() {
				e.log.Info("automation files changed, reloading")
				if err := e.Reload(); err != nil {
					e.log.Error("reload failed", "err", err)
				}
			}
		}
	}
}

func (e *Engine) filesChanged() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for _, f := range Files {
		var mt time.Time
		if st, err := os.Stat(filepath.Join(e.opts.Dir, f)); err == nil {
			mt = st.ModTime()
		}
		if !mt.Equal(e.mtimes[f]) {
			changed = true
		}
	}
	return changed || blueprintSignature(e.opts.Dir) != e.bpSig
}

func (e *Engine) readFile(name string) ([]byte, time.Time, error) {
	p := filepath.Join(e.opts.Dir, name)
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := os.ReadFile(p)
	return data, st.ModTime(), err
}

// Validate parses a file's content without applying it. Automations that
// use a blueprint are only checked for shape; ValidateWithBlueprints
// also loads the blueprints.
func Validate(name string, data []byte) error {
	var err error
	switch name {
	case "automations.yaml":
		_, err = parseAutomations(data, "")
	case "scripts.yaml":
		_, err = parseScripts(data)
	case "scenes.yaml":
		_, err = parseScenes(data)
	default:
		return fmt.Errorf("unknown file %q", name)
	}
	return err
}

// ValidateWithBlueprints is Validate for a data directory: blueprint
// files below dataDir are loaded and their inputs checked.
func ValidateWithBlueprints(dataDir, name string, data []byte) error {
	if name != "automations.yaml" {
		return Validate(name, data)
	}
	_, err := parseAutomations(data, dataDir)
	return err
}

// Reload re-reads all files. A file with errors leaves its previous
// definitions in place and the error is returned.
func (e *Engine) Reload() error {
	var errs []error
	autoData, autoMT, err := e.readFile("automations.yaml")
	if err != nil {
		return err
	}
	sig := blueprintSignature(e.opts.Dir)
	autos, err := parseAutomations(autoData, e.opts.Dir)
	if err != nil {
		errs = append(errs, fmt.Errorf("automations.yaml: %w", err))
		autos = nil
	} else {
		e.applyAutomations(autos)
	}
	scriptData, scriptMT, err := e.readFile("scripts.yaml")
	if err != nil {
		return err
	}
	scripts, err := parseScripts(scriptData)
	if err != nil {
		errs = append(errs, fmt.Errorf("scripts.yaml: %w", err))
	} else {
		e.applyScripts(scripts)
	}
	sceneData, sceneMT, err := e.readFile("scenes.yaml")
	if err != nil {
		return err
	}
	scenes, err := parseScenes(sceneData)
	if err != nil {
		errs = append(errs, fmt.Errorf("scenes.yaml: %w", err))
	} else {
		e.applyScenes(scenes)
	}
	e.mu.Lock()
	e.mtimes["automations.yaml"] = autoMT
	e.mtimes["scripts.yaml"] = scriptMT
	e.mtimes["scenes.yaml"] = sceneMT
	e.bpSig = sig
	e.mu.Unlock()
	return errors.Join(errs...)
}

// --- shared run bookkeeping for automations and scripts ---

type run struct {
	cancel context.CancelFunc
}

type runSet struct {
	mu     sync.Mutex
	active map[*run]struct{}
	queue  []func()
}

func newRunSet() *runSet { return &runSet{active: map[*run]struct{}{}} }

func (r *runSet) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

func (r *runSet) cancelAll() {
	r.mu.Lock()
	runs := make([]*run, 0, len(r.active))
	for x := range r.active {
		runs = append(runs, x)
	}
	r.queue = nil
	r.mu.Unlock()
	for _, x := range runs {
		x.cancel()
	}
}

// launch applies the HA run modes; body runs in its own goroutine.
func (e *Engine) launch(name, mode string, max int, rs *runSet, body func(ctx context.Context)) bool {
	rs.mu.Lock()
	active := len(rs.active)
	switch mode {
	case "single":
		if active > 0 {
			rs.mu.Unlock()
			e.log.Warn("already running", "entity", name)
			return false
		}
	case "restart":
		for x := range rs.active {
			x.cancel()
		}
	case "queued":
		if active > 0 {
			if len(rs.queue) >= max {
				rs.mu.Unlock()
				e.log.Warn("queue full", "entity", name)
				return false
			}
			rs.queue = append(rs.queue, func() { e.launch(name, mode, max, rs, body) })
			rs.mu.Unlock()
			return true
		}
	case "parallel":
		if active >= max {
			rs.mu.Unlock()
			e.log.Warn("maximum number of parallel runs reached", "entity", name)
			return false
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &run{cancel: cancel}
	rs.active[x] = struct{}{}
	rs.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			rs.mu.Lock()
			delete(rs.active, x)
			var next func()
			if len(rs.queue) > 0 && len(rs.active) == 0 {
				next = rs.queue[0]
				rs.queue = rs.queue[1:]
			}
			rs.mu.Unlock()
			if next != nil {
				next()
			}
		}()
		body(ctx)
	}()
	return true
}

// --- automations ---

type automation struct {
	e        *Engine
	cfg      automationConfig
	entityID string
	runs     *runSet
	stops    []func()
	mu       sync.Mutex
	last     *time.Time
	traces   *traceRing
}

func (e *Engine) applyAutomations(cfgs []automationConfig) {
	e.mu.Lock()
	old := e.automations
	e.automations = map[string]*automation{}
	e.mu.Unlock()
	for _, a := range old {
		a.stop()
	}
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		uniq := cfg.ID
		if uniq == "" {
			uniq = "alias:" + cfg.Alias
		}
		entry, err := e.core.RegisterEntity(core.EntityRegistration{Domain: "automation", Platform: "automation", UniqueID: uniq, OriginalName: cfg.Alias})
		if err != nil {
			e.log.Error("automation registration failed", "alias", cfg.Alias, "err", err)
			continue
		}
		a := &automation{e: e, cfg: cfg, entityID: entry.EntityID, runs: newRunSet(), traces: &traceRing{}}
		if prev, ok := old[entry.EntityID]; ok {
			a.last = prev.last
			// Keep the history across reloads: that is when one looks
			// at why the old definition misbehaved.
			a.traces = prev.traces
		}
		e.mu.Lock()
		if _, ok := e.enabled[entry.EntityID]; !ok {
			e.enabled[entry.EntityID] = cfg.Enabled
		}
		e.automations[entry.EntityID] = a
		e.mu.Unlock()
		seen[entry.EntityID] = true
		e.core.RegisterController(entry.EntityID, a)
		a.writeState()
		if e.isEnabled(entry.EntityID) {
			a.arm()
		}
	}
	for id := range old {
		if !seen[id] {
			e.core.RemoveEntity(id)
		}
	}
}

func (e *Engine) isEnabled(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled[id]
}

// arm sets up the triggers.
func (a *automation) arm() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, t := range a.cfg.Triggers {
		if en, ok := t["enabled"]; ok && !truthyNode(en) {
			continue
		}
		idx := i
		stop, err := a.e.setupTrigger(t, idx, func(vars map[string]any) { a.fire(vars, false) })
		if err != nil {
			a.e.log.Error("automation trigger invalid", "entity_id", a.entityID, "trigger", i+1, "err", err)
			continue
		}
		a.stops = append(a.stops, stop)
	}
}

func (a *automation) stop() {
	a.mu.Lock()
	stops := a.stops
	a.stops = nil
	a.mu.Unlock()
	for _, s := range stops {
		s()
	}
	a.runs.cancelAll()
}

func (a *automation) writeState() {
	st := core.StateOff
	if a.e.isEnabled(a.entityID) {
		st = core.StateOn
	}
	a.mu.Lock()
	var last any
	if a.last != nil {
		last = a.last.UTC().Format("2006-01-02T15:04:05.000000-07:00")
	}
	a.mu.Unlock()
	attrs := map[string]any{"last_triggered": last, "mode": a.cfg.Mode, "current": a.runs.count(), "id": a.cfg.ID}
	if a.cfg.ID == "" {
		attrs["id"] = nil
	}
	if err := a.e.core.SetState(a.entityID, st, attrs, core.Context{}); err != nil {
		a.e.log.Error("automation state write failed", "entity_id", a.entityID, "err", err)
	}
}

// fire checks the conditions and starts a run.
func (a *automation) fire(trigger map[string]any, skipConditions bool) {
	cctx := core.NewContext()
	tr := newTrace(cctx.ID, a.e.now(), trigger)
	reject := func(err error) {
		tr.finish(a.e.now(), err)
		a.traces.add(tr)
	}
	vars := map[string]any{}
	for k, v := range a.cfg.Variables {
		r, err := a.e.renderValue(v, vars)
		if err != nil {
			a.e.log.Error("automation variables failed", "entity_id", a.entityID, "err", err)
			reject(err)
			return
		}
		vars[k] = r
	}
	vars["trigger"] = trigger
	if st := a.e.core.GetState(a.entityID); st != nil {
		vars["this"] = stateObj{st, a.e.core}
	}
	if !skipConditions {
		ok, err := a.e.checkConditions(a.cfg.Conditions, vars)
		if err != nil {
			a.e.log.Error("automation condition failed", "entity_id", a.entityID, "err", err)
			reject(err)
			return
		}
		if !ok {
			reject(nil)
			return
		}
	}
	tr.conditionsPassed = true
	a.traces.add(tr)
	started := a.e.launch(a.entityID, a.cfg.Mode, a.cfg.Max, a.runs, func(ctx context.Context) {
		now := a.e.now()
		a.mu.Lock()
		a.last = &now
		a.mu.Unlock()
		a.writeState()
		a.e.log.Info("automation triggered", "entity_id", a.entityID)
		rc := &runCtx{ctx: ctx, e: a.e, vars: vars, name: a.entityID, cctx: cctx, trace: tr, seq: "action"}
		err := a.e.runSequence(rc, a.cfg.Actions)
		if err != nil && !errors.Is(err, errStop) && !errors.Is(err, context.Canceled) {
			a.e.log.Error("automation failed", "entity_id", a.entityID, "err", err)
		}
		tr.finish(a.e.now(), err)
		a.writeState()
	})
	if !started {
		tr.finish(a.e.now(), fmt.Errorf("not started: %s mode does not allow another run", a.cfg.Mode))
	}
}

func (a *automation) setEnabled(on bool) {
	a.e.mu.Lock()
	was := a.e.enabled[a.entityID]
	a.e.enabled[a.entityID] = on
	a.e.mu.Unlock()
	a.e.saveEnabled()
	if on && !was {
		a.arm()
	}
	if !on && was {
		a.stop()
	}
	a.writeState()
}

func (a *automation) TurnOn(_ context.Context, _ map[string]any) error {
	a.setEnabled(true)
	return nil
}
func (a *automation) TurnOff(_ context.Context, _ map[string]any) error {
	a.setEnabled(false)
	return nil
}

// --- services ---

func (e *Engine) registerServices() {
	c := e.core
	target := map[string]any{"entity": map[string]any{}}
	c.RegisterOnOffDomain("automation")
	c.RegisterService("automation", "trigger", func(ctx context.Context, call core.ServiceCall) error {
		skip := true
		if v, ok := call.Data["skip_condition"]; ok {
			skip = truthyNode(v)
		}
		for _, id := range call.EntityIDs {
			a, ok := c.Controller(id).(*automation)
			if !ok {
				return fmt.Errorf("%s is not an automation", id)
			}
			a.fire(map[string]any{"platform": nil, "id": nil, "idx": nil, "description": "service call"}, skip)
		}
		return nil
	}, core.ServiceDesc{Name: "Trigger", Fields: map[string]any{"skip_condition": map[string]any{"description": "Skip the conditions (default true)"}}, Target: target})
	reload := func(_ context.Context, _ core.ServiceCall) error { return e.Reload() }
	for _, d := range []string{"automation", "script", "scene"} {
		c.RegisterService(d, "reload", reload, core.ServiceDesc{Name: "Reload", Description: "Re-reads automations.yaml, scripts.yaml and scenes.yaml"})
	}
	c.RegisterService("script", "turn_on", func(ctx context.Context, call core.ServiceCall) error {
		vars, _ := call.Data["variables"].(map[string]any)
		for _, id := range call.EntityIDs {
			s, ok := c.Controller(id).(*script)
			if !ok {
				return fmt.Errorf("%s is not a script", id)
			}
			s.start(vars)
		}
		return nil
	}, core.ServiceDesc{Name: "Turn on", Fields: map[string]any{"variables": map[string]any{"description": "Variables for the script"}}, Target: target})
	c.RegisterService("script", "turn_off", func(ctx context.Context, call core.ServiceCall) error {
		for _, id := range call.EntityIDs {
			if s, ok := c.Controller(id).(*script); ok {
				s.runs.cancelAll()
				s.writeState()
			}
		}
		return nil
	}, core.ServiceDesc{Name: "Turn off", Target: target})
	// Any scene controller (YAML scenes here, MQTT scenes elsewhere)
	// activates through OnOff.TurnOn.
	c.RegisterService("scene", "turn_on", func(ctx context.Context, call core.ServiceCall) error {
		var errs []error
		for _, id := range call.EntityIDs {
			s, ok := c.Controller(id).(core.OnOff)
			if !ok {
				errs = append(errs, fmt.Errorf("%s is not a scene", id))
				continue
			}
			errs = append(errs, s.TurnOn(ctx, call.Data))
		}
		return errors.Join(errs...)
	}, core.ServiceDesc{Name: "Activate", Fields: map[string]any{"transition": map[string]any{"description": "Transition in seconds"}}, Target: target})
}

// Summary lists automations for the UI.
func (e *Engine) Summary() []map[string]any {
	e.mu.Lock()
	ids := make([]string, 0, len(e.automations))
	for id := range e.automations {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		e.mu.Lock()
		a := e.automations[id]
		e.mu.Unlock()
		if a == nil {
			continue
		}
		var last any
		a.mu.Lock()
		if a.last != nil {
			last = a.last.Format(time.RFC3339)
		}
		a.mu.Unlock()
		out = append(out, map[string]any{"entity_id": id, "alias": a.cfg.Alias, "description": a.cfg.Description, "enabled": e.isEnabled(id), "last_triggered": last, "running": a.runs.count(), "mode": a.cfg.Mode, "traces": a.traces.count()})
	}
	return out
}

// SubscribeTrigger arms a trigger config for an outside subscriber (the
// WebSocket API's subscribe_trigger); fire gets the trigger variables.
func (e *Engine) SubscribeTrigger(cfg map[string]any, fire func(vars map[string]any)) (func(), error) {
	return e.setupTrigger(cfg, 0, fire)
}

// ValidateConfig checks trigger, condition and action lists the way the
// WebSocket validate_config command does; each key is "valid" or an
// error message.
func (e *Engine) ValidateConfig(triggers, conditions, actions node) map[string]any {
	out := map[string]any{}
	if triggers != nil {
		res := "valid"
		for i, t := range asList(triggers) {
			tm, ok := asMap(t)
			if !ok {
				res = fmt.Sprintf("trigger %d is not a mapping", i+1)
				break
			}
			stop, err := e.setupTrigger(tm, i, func(map[string]any) {})
			if err != nil {
				res = err.Error()
				break
			}
			stop()
		}
		out["triggers"] = res
	}
	if conditions != nil {
		res := "valid"
		for _, c := range asList(conditions) {
			if _, err := e.checkCondition(c, map[string]any{"trigger": map[string]any{}}); err != nil {
				res = err.Error()
				break
			}
		}
		out["conditions"] = res
	}
	if actions != nil {
		res := "valid"
		for i, a := range asList(actions) {
			m, ok := asMap(a)
			if !ok {
				res = fmt.Sprintf("action %d is not a mapping", i+1)
				break
			}
			if !has(m, "service", "action", "delay", "wait_template", "wait_for_trigger", "choose", "if", "repeat", "condition", "and", "or", "not", "event", "variables", "stop", "parallel", "sequence", "scene") {
				res = fmt.Sprintf("action %d: unknown action %v", i+1, keysOf(m))
				break
			}
		}
		out["actions"] = res
	}
	return out
}

// Env exposes the template environment (states, is_state, ...) to the
// API's render_template.
func (e *Engine) TemplateEnv() *template.Env { return e.env() }
