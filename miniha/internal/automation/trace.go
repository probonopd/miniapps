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
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// tracesPerAutomation is how many runs are kept in memory; traces are
// a debugging aid, not an audit log.
const tracesPerAutomation = 5

type traceStep struct {
	path     string
	typ      string
	started  time.Time
	finished time.Time
	err      string
	detail   map[string]any
}

// trace records one automation run. Steps are appended while the run
// executes, possibly from parallel branches, so every access locks.
type trace struct {
	mu               sync.Mutex
	runID            string
	started          time.Time
	finished         time.Time
	trigger          map[string]any
	conditionsPassed bool
	steps            []*traceStep
	err              string
}

func newTrace(runID string, started time.Time, trigger map[string]any) *trace {
	return &trace{runID: runID, started: started, trigger: sanitizeMap(trigger)}
}

// begin opens a step. All trace methods accept a nil receiver so that
// scripts, which run without a trace, share the action code.
func (t *trace) begin(path, typ string, now time.Time) *traceStep {
	if t == nil {
		return nil
	}
	st := &traceStep{path: path, typ: typ, started: now}
	t.mu.Lock()
	t.steps = append(t.steps, st)
	t.mu.Unlock()
	return st
}

func (t *trace) end(st *traceStep, now time.Time, err error) {
	if t == nil || st == nil {
		return
	}
	t.mu.Lock()
	st.finished = now
	switch {
	case err == nil, errors.Is(err, errStop):
	case errors.Is(err, context.Canceled):
		st.err = "cancelled"
	default:
		st.err = err.Error()
	}
	t.mu.Unlock()
}

// setError notes an error the run survived (continue_on_error).
func (t *trace) setError(st *traceStep, err error) {
	if t == nil || st == nil {
		return
	}
	t.mu.Lock()
	st.err = err.Error()
	t.mu.Unlock()
}

func (t *trace) setDetail(st *traceStep, detail map[string]any) {
	if t == nil || st == nil {
		return
	}
	d := sanitizeMap(detail)
	t.mu.Lock()
	st.detail = d
	t.mu.Unlock()
}

func (t *trace) finish(now time.Time, err error) {
	t.mu.Lock()
	t.finished = now
	switch {
	case err == nil, errors.Is(err, errStop):
	case errors.Is(err, context.Canceled):
		t.err = "run cancelled"
	default:
		t.err = err.Error()
	}
	t.mu.Unlock()
}

func (t *trace) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	steps := make([]any, 0, len(t.steps))
	for _, st := range t.steps {
		m := map[string]any{"path": st.path, "type": st.typ, "started": st.started, "finished": nil}
		if !st.finished.IsZero() {
			m["finished"] = st.finished
		}
		if st.err != "" {
			m["error"] = st.err
		}
		if st.detail != nil {
			m["detail"] = st.detail
		}
		steps = append(steps, m)
	}
	out := map[string]any{"run_id": t.runID, "started": t.started, "finished": nil, "trigger": t.trigger, "conditions_passed": t.conditionsPassed, "steps": steps}
	if !t.finished.IsZero() {
		out["finished"] = t.finished
	}
	if t.err != "" {
		out["error"] = t.err
	}
	return out
}

// traceRing keeps the newest runs of one automation.
type traceRing struct {
	mu   sync.Mutex
	list []*trace // oldest first
}

func (r *traceRing) add(t *trace) {
	r.mu.Lock()
	r.list = append(r.list, t)
	if len(r.list) > tracesPerAutomation {
		r.list = r.list[len(r.list)-tracesPerAutomation:]
	}
	r.mu.Unlock()
}

func (r *traceRing) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.list)
}

// snapshots returns the traces newest first.
func (r *traceRing) snapshots() []map[string]any {
	r.mu.Lock()
	list := append([]*trace(nil), r.list...)
	r.mu.Unlock()
	out := make([]map[string]any, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i].snapshot())
	}
	return out
}

// Traces returns the recorded runs of an automation, newest first; nil
// when the entity is not an automation. Runs still executing are
// included with a nil "finished".
func (e *Engine) Traces(entityID string) []map[string]any {
	e.mu.Lock()
	a := e.automations[entityID]
	e.mu.Unlock()
	if a == nil {
		return nil
	}
	return a.traces.snapshots()
}

// actionType names an action the way the trace reports it; the order
// mirrors the dispatch in runAction.
func actionType(m map[string]any) string {
	for _, k := range []struct{ key, name string }{
		{"service", "service"}, {"action", "service"}, {"delay", "delay"}, {"wait_template", "wait_template"},
		{"wait_for_trigger", "wait_for_trigger"}, {"choose", "choose"}, {"if", "if"}, {"repeat", "repeat"},
		{"condition", "condition"}, {"and", "condition"}, {"or", "condition"}, {"not", "condition"},
		{"event", "event"}, {"variables", "variables"}, {"stop", "stop"}, {"parallel", "parallel"},
		{"sequence", "sequence"}, {"scene", "scene"},
	} {
		if _, ok := m[k.key]; ok {
			return k.name
		}
	}
	return "unknown"
}

// sanitizeMap converts run values into plain JSON-friendly data: state
// objects become state dictionaries, template date and time types
// become strings.
func sanitizeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = sanitize(v)
	}
	return out
}

func sanitize(v any) any {
	switch x := v.(type) {
	case nil, string, bool, int, int64, uint64, float64:
		return x
	case map[string]any:
		return sanitizeMap(x)
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = sanitize(it)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = it
		}
		return out
	case stateObj:
		return x.s
	case *core.State:
		return x
	case template.DateTime:
		return x.T.Format(time.RFC3339Nano)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}
