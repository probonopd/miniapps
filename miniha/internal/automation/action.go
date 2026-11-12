/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// errStop ends a sequence quietly (stop action, failed inline condition).
var errStop = errors.New("stop")

type runCtx struct {
	ctx  context.Context
	e    *Engine
	vars map[string]any
	name string
	cctx core.Context

	// Trace bookkeeping; trace is nil for runs that are not traced.
	// seq is the path prefix of the sequence being run, step the path
	// of the action being run and curStep its trace entry.
	trace   *trace
	seq     string
	step    string
	curStep *traceStep
}

func (rc *runCtx) child() *runCtx {
	vars := make(map[string]any, len(rc.vars))
	for k, v := range rc.vars {
		vars[k] = v
	}
	return &runCtx{ctx: rc.ctx, e: rc.e, vars: vars, name: rc.name, cctx: rc.cctx, trace: rc.trace, seq: rc.seq, step: rc.step, curStep: rc.curStep}
}

// runNested runs a sub-sequence of the current action under its own
// trace path, then restores the path for the rest of the action.
func (e *Engine) runNested(rc *runCtx, path string, actions []node) error {
	saved := rc.seq
	rc.seq = path
	defer func() { rc.seq = saved }()
	return e.runSequence(rc, actions)
}

func (e *Engine) runSequence(rc *runCtx, actions []node) error {
	base := rc.seq
	for i, a := range actions {
		if err := rc.ctx.Err(); err != nil {
			return err
		}
		rc.step = fmt.Sprintf("%s/%d", base, i)
		typ := "unknown"
		if m, ok := asMap(a); ok {
			typ = actionType(m)
		}
		st := rc.trace.begin(rc.step, typ, e.now())
		rc.curStep = st
		err := e.runAction(rc, a)
		rc.trace.end(st, e.now(), err)
		if err != nil {
			if errors.Is(err, errStop) || errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("action %d: %w", i+1, err)
		}
	}
	return nil
}

func (e *Engine) runAction(rc *runCtx, a node) error {
	m, ok := asMap(a)
	if !ok {
		return fmt.Errorf("action is not a mapping")
	}
	if en, ok := m["enabled"]; ok && !truthyNode(en) {
		return nil
	}
	switch {
	case has(m, "service", "action"):
		err := e.serviceAction(rc, m)
		if err != nil && truthyNode(m["continue_on_error"]) {
			e.log.Warn("action failed, continuing", "name", rc.name, "err", err)
			rc.trace.setError(rc.curStep, err)
			return nil
		}
		return err
	case has(m, "delay"):
		return e.delayAction(rc, m["delay"])
	case has(m, "wait_template"):
		return e.waitTemplateAction(rc, m)
	case has(m, "wait_for_trigger"):
		return e.waitForTriggerAction(rc, m)
	case has(m, "choose"):
		return e.chooseAction(rc, m)
	case has(m, "if"):
		return e.ifAction(rc, m)
	case has(m, "repeat"):
		return e.repeatAction(rc, m)
	case has(m, "condition") || has(m, "and", "or", "not"):
		ok, err := e.checkCondition(m, rc.vars)
		if err != nil {
			return err
		}
		if !ok {
			return errStop
		}
		return nil
	case has(m, "event"):
		data, err := e.renderValue(m["event_data"], rc.vars)
		if err != nil {
			return err
		}
		dm, _ := data.(map[string]any)
		rc.trace.setDetail(rc.curStep, map[string]any{"event_type": str(m["event"]), "event_data": dm})
		e.core.Fire(str(m["event"]), dm, rc.cctx)
		return nil
	case has(m, "variables"):
		vars, ok := asMap(m["variables"])
		if !ok {
			return fmt.Errorf("variables must be a mapping")
		}
		for k, v := range vars {
			r, err := e.renderValue(v, rc.vars)
			if err != nil {
				return err
			}
			rc.vars[k] = r
		}
		return nil
	case has(m, "stop"):
		if truthyNode(m["error"]) {
			return fmt.Errorf("stopped with error: %s", str(m["stop"]))
		}
		e.log.Info("sequence stopped", "name", rc.name, "message", str(m["stop"]))
		return errStop
	case has(m, "parallel"):
		return e.parallelAction(rc, m["parallel"])
	case has(m, "sequence"):
		return e.runNested(rc, rc.step+"/sequence", asList(m["sequence"]))
	case has(m, "scene"):
		id := str(m["scene"])
		_, err := e.core.CallService(rc.ctx, "scene", "turn_on", map[string]any{"entity_id": id}, rc.cctx)
		return err
	}
	return fmt.Errorf("unknown action %v", keysOf(m))
}

func has(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (e *Engine) serviceAction(rc *runCtx, m map[string]any) error {
	raw, _ := pick(m, "action", "service")
	nameV, err := e.render(str(raw), rc.vars)
	if err != nil {
		return err
	}
	name := str(nameV)
	domain, svc, ok := strings.Cut(name, ".")
	if !ok || domain == "" || svc == "" {
		return fmt.Errorf("invalid service %q", name)
	}
	data := map[string]any{}
	for _, key := range []string{"data_template", "data"} {
		if raw, ok := m[key]; ok {
			r, err := e.renderValue(raw, rc.vars)
			if err != nil {
				return err
			}
			rm, ok := r.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be a mapping", key)
			}
			for k, v := range rm {
				data[k] = v
			}
		}
	}
	if raw, ok := m["target"]; ok {
		r, err := e.renderValue(raw, rc.vars)
		if err != nil {
			return err
		}
		data["target"] = r
	}
	if raw, ok := m["entity_id"]; ok {
		r, err := e.renderValue(raw, rc.vars)
		if err != nil {
			return err
		}
		data["entity_id"] = r
	}
	rc.trace.setDetail(rc.curStep, map[string]any{"service": name, "data": data})
	_, err = e.core.CallService(rc.ctx, domain, svc, data, rc.cctx)
	return err
}

func (e *Engine) duration(rc *runCtx, raw node) (time.Duration, error) {
	if s, ok := raw.(string); ok && template.IsTemplate(s) {
		v, err := e.render(s, rc.vars)
		if err != nil {
			return 0, err
		}
		raw = v
	}
	return parseDuration(raw)
}

func (e *Engine) delayAction(rc *runCtx, raw node) error {
	d, err := e.duration(rc, raw)
	if err != nil {
		return fmt.Errorf("delay: %w", err)
	}
	rc.trace.setDetail(rc.curStep, map[string]any{"delay": template.TimeDelta{D: d}})
	select {
	case <-time.After(d):
		return nil
	case <-rc.ctx.Done():
		return rc.ctx.Err()
	}
}

func (e *Engine) timeout(rc *runCtx, m map[string]any) (<-chan time.Time, time.Duration, error) {
	raw, ok := m["timeout"]
	if !ok {
		return nil, 0, nil
	}
	d, err := e.duration(rc, raw)
	if err != nil {
		return nil, 0, fmt.Errorf("timeout: %w", err)
	}
	return time.After(d), d, nil
}

func (e *Engine) waitTemplateAction(rc *runCtx, m map[string]any) error {
	src := str(m["wait_template"])
	done := make(chan struct{}, 1)
	check := func() {
		if ok, err := e.renderBool(src, rc.vars); err == nil && ok {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}
	unsub := e.core.Subscribe("state_changed", func(core.Event) { check() })
	defer unsub()
	tmo, d, err := e.timeout(rc, m)
	if err != nil {
		return err
	}
	start := e.now()
	check()
	select {
	case <-done:
		rc.vars["wait"] = map[string]any{"completed": true, "remaining": remaining(d, e.now().Sub(start))}
		return nil
	case <-tmo:
		rc.vars["wait"] = map[string]any{"completed": false, "remaining": 0.0}
		if v, ok := m["continue_on_timeout"]; ok && !truthyNode(v) {
			return errStop
		}
		return nil
	case <-rc.ctx.Done():
		return rc.ctx.Err()
	}
}

func remaining(d, elapsed time.Duration) any {
	if d == 0 {
		return nil
	}
	return max(0, (d - elapsed).Seconds())
}

func (e *Engine) waitForTriggerAction(rc *runCtx, m map[string]any) error {
	fired := make(chan map[string]any, 1)
	var stops []func()
	for i, t := range asList(m["wait_for_trigger"]) {
		tm, ok := asMap(t)
		if !ok {
			return fmt.Errorf("wait_for_trigger: trigger %d is not a mapping", i+1)
		}
		stop, err := e.setupTrigger(tm, i, func(vars map[string]any) {
			select {
			case fired <- vars:
			default:
			}
		})
		if err != nil {
			return fmt.Errorf("wait_for_trigger: %w", err)
		}
		stops = append(stops, stop)
	}
	defer func() {
		for _, s := range stops {
			s()
		}
	}()
	tmo, d, err := e.timeout(rc, m)
	if err != nil {
		return err
	}
	start := e.now()
	select {
	case vars := <-fired:
		rc.vars["wait"] = map[string]any{"trigger": vars, "remaining": remaining(d, e.now().Sub(start))}
		return nil
	case <-tmo:
		rc.vars["wait"] = map[string]any{"trigger": nil, "remaining": 0.0}
		if v, ok := m["continue_on_timeout"]; ok && !truthyNode(v) {
			return errStop
		}
		return nil
	case <-rc.ctx.Done():
		return rc.ctx.Err()
	}
}

func (e *Engine) chooseAction(rc *runCtx, m map[string]any) error {
	step := rc.step
	for i, opt := range asList(m["choose"]) {
		om, ok := asMap(opt)
		if !ok {
			return fmt.Errorf("choose: option %d is not a mapping", i+1)
		}
		conds, _ := pick(om, "conditions", "condition")
		ok, err := e.checkConditions(asList(conds), rc.vars)
		if err != nil {
			return err
		}
		if ok {
			return e.runNested(rc, fmt.Sprintf("%s/choose/%d/sequence", step, i), asList(om["sequence"]))
		}
	}
	if def, ok := m["default"]; ok {
		return e.runNested(rc, step+"/default", asList(def))
	}
	return nil
}

func (e *Engine) ifAction(rc *runCtx, m map[string]any) error {
	step := rc.step
	ok, err := e.checkConditions(asList(m["if"]), rc.vars)
	if err != nil {
		return err
	}
	if ok {
		return e.runNested(rc, step+"/then", asList(m["then"]))
	}
	if els, ok := m["else"]; ok {
		return e.runNested(rc, step+"/else", asList(els))
	}
	return nil
}

func (e *Engine) repeatAction(rc *runCtx, m map[string]any) error {
	rm, ok := asMap(m["repeat"])
	if !ok {
		return fmt.Errorf("repeat must be a mapping")
	}
	seq := asList(rm["sequence"])
	seqPath := rc.step + "/repeat/sequence"
	iteration := func(i int, last bool, item any) error {
		rc.vars["repeat"] = map[string]any{"index": int64(i + 1), "first": i == 0, "last": last, "item": item}
		return e.runNested(rc, seqPath, seq)
	}
	switch {
	case has(rm, "count"):
		cv, err := e.renderValue(rm["count"], rc.vars)
		if err != nil {
			return err
		}
		var n int
		if _, err := fmt.Sscan(str(cv), &n); err != nil || n < 0 {
			return fmt.Errorf("repeat count must be a non-negative integer")
		}
		for i := 0; i < n; i++ {
			if err := iteration(i, i == n-1, nil); err != nil {
				return err
			}
		}
	case has(rm, "for_each"):
		items, err := e.renderValue(rm["for_each"], rc.vars)
		if err != nil {
			return err
		}
		list := template.ToGoList(template.FromGo(items))
		for i, it := range list {
			if err := iteration(i, i == len(list)-1, template.ToGo(it)); err != nil {
				return err
			}
		}
	case has(rm, "while"):
		for i := 0; ; i++ {
			rc.vars["repeat"] = map[string]any{"index": int64(i + 1), "first": i == 0, "last": false}
			ok, err := e.checkConditions(asList(rm["while"]), rc.vars)
			if err != nil {
				return err
			}
			if !ok || i >= 10000 {
				return nil
			}
			if err := iteration(i, false, nil); err != nil {
				return err
			}
		}
	case has(rm, "until"):
		for i := 0; i < 10000; i++ {
			if err := iteration(i, false, nil); err != nil {
				return err
			}
			ok, err := e.checkConditions(asList(rm["until"]), rc.vars)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	default:
		return fmt.Errorf("repeat needs count, for_each, while or until")
	}
	return nil
}

func (e *Engine) parallelAction(rc *runCtx, raw node) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	step := rc.step
	for i, item := range asList(raw) {
		var seq []node
		if m, ok := asMap(item); ok && has(m, "sequence") && len(m) == 1 {
			seq = asList(m["sequence"])
		} else {
			seq = []node{item}
		}
		wg.Add(1)
		child := rc.child()
		path := fmt.Sprintf("%s/parallel/%d/sequence", step, i)
		go func(seq []node) {
			defer wg.Done()
			if err := e.runNested(child, path, seq); err != nil && !errors.Is(err, errStop) {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(seq)
	}
	wg.Wait()
	return errors.Join(errs...)
}
