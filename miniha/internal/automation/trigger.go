/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/mqtt"
	"github.com/probonopd/miniha/internal/template"
)

// setupTrigger arms one trigger; fire runs with the "trigger" variables
// and must return quickly (it starts goroutines itself).
func (e *Engine) setupTrigger(cfg map[string]any, idx int, fire func(vars map[string]any)) (func(), error) {
	platformRaw, _ := pick(cfg, "platform", "trigger")
	platform := str(platformRaw)
	base := func() map[string]any {
		v := map[string]any{"platform": platform, "idx": strconv.Itoa(idx), "id": strconv.Itoa(idx), "alias": nil}
		if id, ok := cfg["id"]; ok {
			v["id"] = str(id)
		}
		return v
	}
	switch platform {
	case "state":
		return e.stateTrigger(cfg, base, fire)
	case "numeric_state":
		return e.numericStateTrigger(cfg, base, fire)
	case "time":
		return e.timeTrigger(cfg, base, fire)
	case "time_pattern":
		return e.timePatternTrigger(cfg, base, fire)
	case "mqtt":
		return e.mqttTrigger(cfg, base, fire)
	case "sun":
		return e.sunTrigger(cfg, base, fire)
	case "template":
		return e.templateTrigger(cfg, base, fire)
	case "event":
		return e.eventTrigger(cfg, base, fire)
	case "homeassistant":
		return e.haTrigger(cfg, base, fire)
	case "webhook":
		return e.webhookTrigger(cfg, base, fire)
	case "tag":
		return e.tagTrigger(cfg, base, fire)
	case "zone":
		return e.zoneTrigger(cfg, base, fire)
	case "device":
		return e.deviceTrigger(cfg, base, fire)
	}
	return nil, fmt.Errorf("unsupported trigger platform %q", platform)
}

// forDuration parses the "for" option; a template is rendered later
// with the trigger variables.
func (e *Engine) forDuration(cfg map[string]any, vars map[string]any) (time.Duration, bool, error) {
	raw, ok := cfg["for"]
	if !ok || raw == nil {
		return 0, false, nil
	}
	if s, isStr := raw.(string); isStr && template.IsTemplate(s) {
		v, err := e.render(s, vars)
		if err != nil {
			return 0, false, err
		}
		raw = v
	}
	d, err := parseDuration(raw)
	if err != nil {
		return 0, false, fmt.Errorf("for: %w", err)
	}
	return d, true, nil
}

// pending tracks "for" timers per entity.
type pending struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newPending() *pending { return &pending{timers: map[string]*time.Timer{}} }

func (p *pending) cancel(key string) {
	p.mu.Lock()
	if t := p.timers[key]; t != nil {
		t.Stop()
		delete(p.timers, key)
	}
	p.mu.Unlock()
}

func (p *pending) start(key string, d time.Duration, fn func()) {
	p.cancel(key)
	p.mu.Lock()
	p.timers[key] = time.AfterFunc(d, func() {
		p.mu.Lock()
		delete(p.timers, key)
		p.mu.Unlock()
		fn()
	})
	p.mu.Unlock()
}

func (p *pending) cancelAll() {
	p.mu.Lock()
	for k, t := range p.timers {
		t.Stop()
		delete(p.timers, k)
	}
	p.mu.Unlock()
}

func stateVar(c *core.Core, s *core.State) any {
	if s == nil {
		return nil
	}
	return stateObj{s, c}
}

func matchesAny(value string, want []string, present bool) bool {
	if !present {
		return true
	}
	for _, w := range want {
		if w == value {
			return true
		}
	}
	return false
}

func attrString(s *core.State, attr string) string {
	if s == nil {
		return ""
	}
	v, ok := s.Attributes[attr]
	if !ok {
		return ""
	}
	return template.PyStr(v)
}

func (e *Engine) stateTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	ids := strList(cfg["entity_id"])
	if len(ids) == 0 {
		return nil, fmt.Errorf("state trigger needs entity_id")
	}
	for _, id := range ids {
		if !core.ValidEntityID(id) {
			return nil, fmt.Errorf("state trigger: invalid entity id %q", id)
		}
	}
	attr := str(cfg["attribute"])
	to, hasTo := cfg["to"]
	from, hasFrom := cfg["from"]
	notTo, hasNotTo := cfg["not_to"]
	notFrom, hasNotFrom := cfg["not_from"]
	toList, fromList := strList(to), strList(from)
	notToList, notFromList := strList(notTo), strList(notFrom)
	if hasTo && to == nil {
		// "to: null" means any state, but not attribute-only changes.
		toList, hasTo = nil, false
		hasTo = false
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	pend := newPending()
	value := func(s *core.State) string {
		if s == nil {
			return ""
		}
		if attr != "" {
			return attrString(s, attr)
		}
		return s.State
	}
	unsub := e.core.Subscribe("state_changed", func(ev core.Event) {
		id, _ := ev.Data["entity_id"].(string)
		if !want[id] {
			return
		}
		oldS, _ := ev.Data["old_state"].(*core.State)
		newS, _ := ev.Data["new_state"].(*core.State)
		oldV, newV := value(oldS), value(newS)
		if oldS != nil && newS != nil && oldV == newV {
			// Attribute-only change: only a trigger without to/from and
			// without attribute fires on those.
			if hasTo || hasFrom || attr != "" || hasNotTo || hasNotFrom {
				return
			}
		}
		if newS == nil || !matchesAny(newV, toList, hasTo) || !matchesAny(oldV, fromList, hasFrom) ||
			(hasNotTo && matchesAny(newV, notToList, true)) || (hasNotFrom && matchesAny(oldV, notFromList, true)) {
			pend.cancel(id)
			return
		}
		vars := base()
		vars["entity_id"] = id
		vars["from_state"] = stateVar(e.core, oldS)
		vars["to_state"] = stateVar(e.core, newS)
		vars["attribute"] = nil
		if attr != "" {
			vars["attribute"] = attr
		}
		vars["for"] = nil
		d, hasFor, err := e.forDuration(cfg, map[string]any{"trigger": vars})
		if err != nil {
			e.log.Error("state trigger", "err", err)
			return
		}
		if !hasFor {
			fire(vars)
			return
		}
		// Re-arm only if the target value is still held after the wait.
		matched := newV
		pend.start(id, d, func() {
			if cur := e.core.GetState(id); cur == nil || value(cur) != matched {
				return
			}
			vars["for"] = template.TimeDelta{D: d}
			fire(vars)
		})
	})
	return func() { unsub(); pend.cancelAll() }, nil
}

func (e *Engine) numericBound(v node, vars map[string]any) (float64, bool, error) {
	if v == nil {
		return 0, false, nil
	}
	if s, ok := v.(string); ok && core.ValidEntityID(s) {
		st := e.core.GetState(s)
		if st == nil {
			return 0, false, fmt.Errorf("entity %s has no state", s)
		}
		f, err := strconv.ParseFloat(st.State, 64)
		return f, true, err
	}
	r, err := e.renderValue(v, vars)
	if err != nil {
		return 0, false, err
	}
	f, err := strconv.ParseFloat(str(r), 64)
	if err != nil {
		return 0, false, fmt.Errorf("not a number: %v", v)
	}
	return f, true, nil
}

// numericValue reads the value a numeric_state trigger or condition
// compares: the state, an attribute, or a value_template with "state".
func (e *Engine) numericValue(cfg map[string]any, s *core.State, vars map[string]any) (float64, bool) {
	if s == nil {
		return 0, false
	}
	var raw string
	if attr := str(cfg["attribute"]); attr != "" {
		raw = attrString(s, attr)
	} else {
		raw = s.State
	}
	if tpl := str(cfg["value_template"]); tpl != "" {
		v := map[string]any{"state": stateObj{s, e.core}}
		for k, x := range vars {
			v[k] = x
		}
		r, err := e.render(tpl, v)
		if err != nil {
			e.log.Warn("numeric_state value_template failed", "entity_id", s.EntityID, "err", err)
			return 0, false
		}
		raw = str(r)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func (e *Engine) inRange(cfg map[string]any, f float64, vars map[string]any) (bool, error) {
	above, hasAbove, err := e.numericBound(cfg["above"], vars)
	if err != nil {
		return false, err
	}
	below, hasBelow, err := e.numericBound(cfg["below"], vars)
	if err != nil {
		return false, err
	}
	if !hasAbove && !hasBelow {
		return false, fmt.Errorf("numeric_state needs above or below")
	}
	return (!hasAbove || f > above) && (!hasBelow || f < below), nil
}

func (e *Engine) numericStateTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	ids := strList(cfg["entity_id"])
	if len(ids) == 0 {
		return nil, fmt.Errorf("numeric_state trigger needs entity_id")
	}
	if _, err := e.inRange(cfg, 0, nil); err != nil && strings.Contains(err.Error(), "needs above") {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var mu sync.Mutex
	was := map[string]bool{}
	pend := newPending()
	unsub := e.core.Subscribe("state_changed", func(ev core.Event) {
		id, _ := ev.Data["entity_id"].(string)
		if !want[id] {
			return
		}
		oldS, _ := ev.Data["old_state"].(*core.State)
		newS, _ := ev.Data["new_state"].(*core.State)
		f, ok := e.numericValue(cfg, newS, nil)
		now := false
		if ok {
			var err error
			now, err = e.inRange(cfg, f, nil)
			if err != nil {
				e.log.Warn("numeric_state trigger", "err", err)
				return
			}
		}
		mu.Lock()
		before, known := was[id]
		was[id] = now
		mu.Unlock()
		if !now {
			pend.cancel(id)
			return
		}
		if known && before {
			return
		}
		vars := base()
		vars["entity_id"] = id
		vars["from_state"] = stateVar(e.core, oldS)
		vars["to_state"] = stateVar(e.core, newS)
		vars["above"], vars["below"] = cfg["above"], cfg["below"]
		vars["for"] = nil
		d, hasFor, err := e.forDuration(cfg, map[string]any{"trigger": vars})
		if err != nil {
			e.log.Error("numeric_state trigger", "err", err)
			return
		}
		if !hasFor {
			fire(vars)
			return
		}
		pend.start(id, d, func() {
			cur := e.core.GetState(id)
			cf, ok := e.numericValue(cfg, cur, nil)
			if !ok {
				return
			}
			if in, _ := e.inRange(cfg, cf, nil); !in {
				return
			}
			vars["for"] = template.TimeDelta{D: d}
			fire(vars)
		})
	})
	return func() { unsub(); pend.cancelAll() }, nil
}

// schedule runs fn at times produced by next, until stopped. A value on
// wake (may be nil) makes it ask next again, for schedules whose times
// depend on state that can change while a timer is waiting.
func (e *Engine) schedule(next func(time.Time) (time.Time, bool), fn func(at time.Time), wake <-chan struct{}) func() {
	stop := make(chan struct{})
	go func() {
		for {
			at, ok := next(e.now())
			if !ok {
				if wake == nil {
					return
				}
				// Nothing to wait for now, but an entity change may
				// produce a time.
				select {
				case <-stop:
					return
				case <-wake:
					continue
				}
			}
			t := time.NewTimer(time.Until(at))
			select {
			case <-stop:
				t.Stop()
				return
			case <-wake:
				t.Stop()
			case <-t.C:
				fn(at)
			}
		}
	}()
	return func() { close(stop) }
}

// timeAt is one "at" entry of a time trigger: a fixed time of day, or
// an entity whose state is resolved on every scheduling pass.
type timeAt struct {
	h, m, s int
	entity  string
}

// resolveAt turns an entity state into a daily time of day (clock) or
// an absolute moment: input_datetime states are "HH:MM:SS" for time-only
// helpers and "YYYY-MM-DD HH:MM:SS" when the helper has a date.
func (e *Engine) resolveAt(state string) (clock *timeAt, abs time.Time, err error) {
	if h, m, s, perr := parseClock(state); perr == nil {
		return &timeAt{h: h, m: m, s: s}, time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, perr := time.ParseInLocation(layout, state, e.opts.Location); perr == nil {
			return nil, t, nil
		}
	}
	if t, ok := template.ParseDateTime(state); ok && strings.Contains(state, ":") {
		return nil, t, nil
	}
	return nil, time.Time{}, fmt.Errorf("cannot read %q as a time", state)
}

func (e *Engine) timeTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	ats := strList(cfg["at"])
	if len(ats) == 0 {
		return nil, fmt.Errorf("time trigger needs at")
	}
	var times []timeAt
	entities := map[string]bool{}
	for _, a := range ats {
		if core.ValidEntityID(a) {
			times = append(times, timeAt{entity: a})
			entities[a] = true
			continue
		}
		h, m, s, err := parseClock(a)
		if err != nil {
			return nil, fmt.Errorf("time trigger: %w", err)
		}
		times = append(times, timeAt{h: h, m: m, s: s})
	}
	next := func(now time.Time) (time.Time, bool) {
		var best time.Time
		consider := func(cand time.Time) {
			if cand.After(now) && (best.IsZero() || cand.Before(best)) {
				best = cand
			}
		}
		daily := func(t timeAt) {
			for day := 0; day < 2; day++ {
				d := now.AddDate(0, 0, day)
				cand := time.Date(d.Year(), d.Month(), d.Day(), t.h, t.m, t.s, 0, now.Location())
				if cand.After(now) {
					consider(cand)
					break
				}
			}
		}
		for _, t := range times {
			if t.entity == "" {
				daily(t)
				continue
			}
			st := e.core.GetState(t.entity)
			if st == nil || st.State == core.StateUnknown || st.State == core.StateUnavailable {
				e.log.Warn("time trigger entity has no usable state, waiting for a change", "entity_id", t.entity)
				continue
			}
			clock, abs, err := e.resolveAt(st.State)
			if err != nil {
				e.log.Warn("time trigger entity ignored", "entity_id", t.entity, "err", err)
				continue
			}
			if clock != nil {
				daily(*clock)
			} else {
				consider(abs)
			}
		}
		return best, !best.IsZero()
	}
	var wake chan struct{}
	unsub := func() {}
	if len(entities) > 0 {
		wake = make(chan struct{}, 1)
		unsub = e.core.Subscribe("state_changed", func(ev core.Event) {
			if id, _ := ev.Data["entity_id"].(string); entities[id] {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		})
	}
	stop := e.schedule(next, func(at time.Time) {
		vars := base()
		vars["now"] = template.DateTime{T: at}
		fire(vars)
	}, wake)
	return func() {
		stop()
		unsub()
	}, nil
}

// patternField parses "*", "/N" or "N" for time_pattern.
type patternField struct {
	any   bool
	every int
	exact int
}

func parsePattern(n node, present bool) (patternField, error) {
	if !present {
		return patternField{any: true}, nil
	}
	s := strings.TrimSpace(str(n))
	switch {
	case s == "*":
		return patternField{any: true}, nil
	case strings.HasPrefix(s, "/"):
		k, err := strconv.Atoi(s[1:])
		if err != nil || k <= 0 {
			return patternField{}, fmt.Errorf("invalid time_pattern %q", s)
		}
		return patternField{every: k}, nil
	}
	k, err := strconv.Atoi(s)
	if err != nil || k < 0 {
		return patternField{}, fmt.Errorf("invalid time_pattern %q", s)
	}
	return patternField{exact: k}, nil
}

func (p patternField) match(v int) bool {
	switch {
	case p.any:
		return true
	case p.every > 0:
		return v%p.every == 0
	}
	return v == p.exact
}

func (e *Engine) timePatternTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	hRaw, hasH := cfg["hours"]
	mRaw, hasM := cfg["minutes"]
	sRaw, hasS := cfg["seconds"]
	if !hasH && !hasM && !hasS {
		return nil, fmt.Errorf("time_pattern needs hours, minutes or seconds")
	}
	// Smaller units default to zero when a larger one is given.
	if !hasM && hasH {
		mRaw, hasM = "0", true
	}
	if !hasS && hasM {
		sRaw, hasS = "0", true
	}
	h, err := parsePattern(hRaw, hasH)
	if err != nil {
		return nil, err
	}
	m, err := parsePattern(mRaw, hasM)
	if err != nil {
		return nil, err
	}
	s, err := parsePattern(sRaw, hasS)
	if err != nil {
		return nil, err
	}
	next := func(now time.Time) (time.Time, bool) {
		t := now.Truncate(time.Second).Add(time.Second)
		for i := 0; i < 24*3600+1; i++ {
			if h.match(t.Hour()) && m.match(t.Minute()) && s.match(t.Second()) {
				return t, true
			}
			t = t.Add(time.Second)
		}
		return time.Time{}, false
	}
	return e.schedule(next, func(at time.Time) {
		vars := base()
		vars["now"] = template.DateTime{T: at}
		fire(vars)
	}, nil), nil
}

func (e *Engine) mqttTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	topicRaw, err := e.render(str(cfg["topic"]), nil)
	if err != nil {
		return nil, err
	}
	topic := str(topicRaw)
	if topic == "" {
		return nil, fmt.Errorf("mqtt trigger needs topic")
	}
	if len(e.opts.Transports) == 0 {
		return nil, fmt.Errorf("mqtt trigger: no broker configured")
	}
	wantPayload, hasPayload := cfg["payload"]
	var tpl *template.Template
	if v := str(cfg["value_template"]); v != "" {
		tpl, err = template.Parse(v)
		if err != nil {
			return nil, err
		}
	}
	var unsubs []func()
	for _, tr := range e.opts.Transports {
		unsub, err := tr.Subscribe(topic, func(m mqtt.Message) {
			payload := string(m.Payload)
			if tpl != nil {
				payload, err = tpl.RenderWithPossibleJSON(string(m.Payload), m.Payload, nil)
				if err != nil {
					e.log.Warn("mqtt trigger value_template failed", "topic", m.Topic, "err", err)
					return
				}
			}
			if hasPayload && str(wantPayload) != payload {
				return
			}
			vars := base()
			vars["topic"] = m.Topic
			vars["payload"] = string(m.Payload)
			var js any
			if json.Unmarshal(m.Payload, &js) == nil {
				vars["payload_json"] = js
			}
			vars["qos"] = 0
			fire(vars)
		})
		if err != nil {
			for _, u := range unsubs {
				u()
			}
			return nil, err
		}
		unsubs = append(unsubs, unsub)
	}
	return func() {
		for _, u := range unsubs {
			u()
		}
	}, nil
}

func (e *Engine) sunTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	event := str(cfg["event"])
	if event != "sunrise" && event != "sunset" {
		return nil, fmt.Errorf("sun trigger needs event sunrise or sunset")
	}
	if !e.hasLocation() {
		return nil, fmt.Errorf("sun trigger needs homeassistant.latitude and longitude in configuration.yaml")
	}
	var offset time.Duration
	if raw, ok := cfg["offset"]; ok {
		var err error
		offset, err = parseSignedDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("sun trigger: %w", err)
		}
	}
	next := func(now time.Time) (time.Time, bool) {
		return nextSunEvent(now, e.opts.Latitude, e.opts.Longitude, event, offset)
	}
	return e.schedule(next, func(at time.Time) {
		vars := base()
		vars["event"] = event
		vars["offset"] = template.TimeDelta{D: offset}
		fire(vars)
	}, nil), nil
}

// parseSignedDuration allows a leading "-" (sun offsets).
func parseSignedDuration(raw node) (time.Duration, error) {
	if s, ok := raw.(string); ok && strings.HasPrefix(strings.TrimSpace(s), "-") {
		d, err := parseDuration(strings.TrimPrefix(strings.TrimSpace(s), "-"))
		return -d, err
	}
	return parseDuration(raw)
}

func (e *Engine) templateTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	src := str(cfg["value_template"])
	if !template.IsTemplate(src) {
		return nil, fmt.Errorf("template trigger needs value_template")
	}
	if _, err := template.Parse(src); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	last, err := e.renderBool(src, nil)
	if err != nil {
		e.log.Warn("template trigger initial evaluation failed", "err", err)
	}
	pend := newPending()
	unsub := e.core.Subscribe("state_changed", func(ev core.Event) {
		now, err := e.renderBool(src, nil)
		if err != nil {
			return
		}
		mu.Lock()
		was := last
		last = now
		mu.Unlock()
		if !now {
			pend.cancel("t")
			return
		}
		if was {
			return
		}
		id, _ := ev.Data["entity_id"].(string)
		oldS, _ := ev.Data["old_state"].(*core.State)
		newS, _ := ev.Data["new_state"].(*core.State)
		vars := base()
		vars["entity_id"] = id
		vars["from_state"] = stateVar(e.core, oldS)
		vars["to_state"] = stateVar(e.core, newS)
		vars["for"] = nil
		d, hasFor, err := e.forDuration(cfg, map[string]any{"trigger": vars})
		if err != nil {
			e.log.Error("template trigger", "err", err)
			return
		}
		if !hasFor {
			fire(vars)
			return
		}
		pend.start("t", d, func() {
			if ok, _ := e.renderBool(src, nil); !ok {
				return
			}
			vars["for"] = template.TimeDelta{D: d}
			fire(vars)
		})
	})
	return func() { unsub(); pend.cancelAll() }, nil
}

func subsetMatch(want map[string]any, have map[string]any) bool {
	for k, v := range want {
		hv, ok := have[k]
		if !ok || template.PyStr(v) != template.PyStr(hv) {
			return false
		}
	}
	return true
}

func (e *Engine) eventTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	types := strList(cfg["event_type"])
	if len(types) == 0 {
		return nil, fmt.Errorf("event trigger needs event_type")
	}
	want, _ := asMap(cfg["event_data"])
	var unsubs []func()
	for _, typ := range types {
		unsub := e.core.Subscribe(typ, func(ev core.Event) {
			if want != nil && !subsetMatch(want, ev.Data) {
				return
			}
			vars := base()
			vars["event"] = map[string]any{"event_type": ev.Type, "data": ev.Data, "time_fired": template.DateTime{T: ev.TimeFired}, "context": map[string]any{"id": ev.Context.ID}}
			fire(vars)
		})
		unsubs = append(unsubs, unsub)
	}
	return func() {
		for _, u := range unsubs {
			u()
		}
	}, nil
}

func (e *Engine) haTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	event := str(cfg["event"])
	var typ string
	switch event {
	case "start":
		typ = "homeassistant_start"
	case "shutdown":
		typ = "homeassistant_stop"
	default:
		return nil, fmt.Errorf("homeassistant trigger needs event start or shutdown")
	}
	unsub := e.core.Subscribe(typ, func(core.Event) {
		vars := base()
		vars["event"] = event
		fire(vars)
	})
	return unsub, nil
}

// webhookMethods are the methods the HTTP API serves for webhooks.
var webhookMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true}

// isLocalRemote tells whether an "ip:port" remote address is loopback or
// private. An unparsable address is an error, never treated as local.
func isLocalRemote(remote string) (bool, error) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false, fmt.Errorf("remote address %q: %w", remote, err)
	}
	ip := ap.Addr().Unmap()
	return ip.IsLoopback() || ip.IsPrivate(), nil
}

func (e *Engine) webhookTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	id := str(cfg["webhook_id"])
	if id == "" {
		return nil, fmt.Errorf("webhook trigger needs webhook_id")
	}
	methods := map[string]bool{"POST": true, "PUT": true}
	if raw, ok := cfg["allowed_methods"]; ok {
		methods = map[string]bool{}
		for _, m := range strList(raw) {
			m = strings.ToUpper(m)
			if !webhookMethods[m] {
				return nil, fmt.Errorf("webhook trigger: unsupported method %q", m)
			}
			methods[m] = true
		}
		if len(methods) == 0 {
			return nil, fmt.Errorf("webhook trigger: allowed_methods is empty")
		}
	}
	localOnly := true
	if raw, ok := cfg["local_only"]; ok {
		localOnly = truthyNode(raw)
	}
	unsub := e.core.Subscribe("webhook", func(ev core.Event) {
		if str(ev.Data["webhook_id"]) != id {
			return
		}
		if !methods[strings.ToUpper(str(ev.Data["method"]))] {
			e.log.Debug("webhook method not allowed", "webhook_id", id, "method", ev.Data["method"])
			return
		}
		if localOnly {
			local, err := isLocalRemote(str(ev.Data["remote"]))
			if err != nil {
				e.log.Warn("webhook rejected", "webhook_id", id, "err", err)
				return
			}
			if !local {
				e.log.Warn("webhook from a non-local address rejected (set local_only: false to allow)", "webhook_id", id, "remote", ev.Data["remote"])
				return
			}
		}
		vars := base()
		vars["webhook_id"] = id
		vars["json"] = ev.Data["json"]
		vars["data"] = ev.Data["data"]
		vars["query"] = ev.Data["query"]
		fire(vars)
	})
	return unsub, nil
}

func (e *Engine) tagTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	tags := strList(cfg["tag_id"])
	if len(tags) == 0 {
		return nil, fmt.Errorf("tag trigger needs tag_id")
	}
	devices := strList(cfg["device_id"])
	unsub := e.core.Subscribe("tag_scanned", func(ev core.Event) {
		tag := str(ev.Data["tag_id"])
		device := str(ev.Data["device_id"])
		if !matchesAny(tag, tags, true) || !matchesAny(device, devices, len(devices) > 0) {
			return
		}
		vars := base()
		vars["tag_id"] = tag
		vars["device_id"] = ev.Data["device_id"]
		fire(vars)
	})
	return unsub, nil
}

func (e *Engine) zoneTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	entities, zones, err := zoneIDs("zone trigger", cfg["entity_id"], cfg["zone"])
	if err != nil {
		return nil, err
	}
	if len(zones) != 1 {
		return nil, fmt.Errorf("zone trigger takes exactly one zone")
	}
	zoneID := zones[0]
	event := str(cfg["event"])
	if event != "enter" && event != "leave" {
		return nil, fmt.Errorf("zone trigger needs event enter or leave")
	}
	want := map[string]bool{}
	for _, id := range entities {
		want[id] = true
	}
	unsub := e.core.Subscribe("state_changed", func(ev core.Event) {
		id, _ := ev.Data["entity_id"].(string)
		if !want[id] {
			return
		}
		oldS, _ := ev.Data["old_state"].(*core.State)
		newS, _ := ev.Data["new_state"].(*core.State)
		zone := e.core.GetState(zoneID)
		if zone == nil {
			e.log.Warn("zone trigger: zone has no state", "zone", zoneID)
			return
		}
		was, now := inZone(oldS, zone), inZone(newS, zone)
		if (event == "enter" && (was || !now)) || (event == "leave" && (!was || now)) {
			return
		}
		vars := base()
		vars["entity_id"] = id
		vars["from_state"] = stateVar(e.core, oldS)
		vars["to_state"] = stateVar(e.core, newS)
		vars["zone"] = stateVar(e.core, zone)
		vars["event"] = event
		fire(vars)
	})
	return unsub, nil
}

func (e *Engine) deviceTrigger(cfg map[string]any, base func() map[string]any, fire func(map[string]any)) (func(), error) {
	if str(cfg["domain"]) != "mqtt" {
		return nil, fmt.Errorf("device triggers are only supported for domain mqtt")
	}
	deviceID, typ, subtype := str(cfg["device_id"]), str(cfg["type"]), str(cfg["subtype"])
	if deviceID == "" || typ == "" || subtype == "" {
		return nil, fmt.Errorf("device trigger needs device_id, type and subtype")
	}
	unsub := e.core.Subscribe("device_trigger", func(ev core.Event) {
		if str(ev.Data["device_id"]) != deviceID || str(ev.Data["type"]) != typ || str(ev.Data["subtype"]) != subtype {
			return
		}
		vars := base()
		vars["device_id"] = deviceID
		vars["type"] = typ
		vars["subtype"] = subtype
		vars["payload"] = ev.Data["payload"]
		fire(vars)
	})
	return unsub, nil
}
