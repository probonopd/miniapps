/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"fmt"
	"strings"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

// checkConditions is true when every condition passes (an empty list
// passes).
func (e *Engine) checkConditions(list []node, vars map[string]any) (bool, error) {
	for _, c := range list {
		ok, err := e.checkCondition(c, vars)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) checkCondition(c node, vars map[string]any) (bool, error) {
	if s, ok := c.(string); ok {
		// Shorthand: a bare template.
		return e.renderBool(s, vars)
	}
	m, ok := asMap(c)
	if !ok {
		return false, fmt.Errorf("condition is neither a mapping nor a template")
	}
	if en, ok := m["enabled"]; ok && !truthyNode(en) {
		return true, nil
	}
	typ := str(m["condition"])
	switch typ {
	case "state":
		return e.stateCondition(m)
	case "numeric_state":
		return e.numericCondition(m, vars)
	case "template":
		return e.renderBool(str(m["value_template"]), vars)
	case "time":
		return e.timeCondition(m)
	case "sun":
		return e.sunCondition(m)
	case "trigger":
		return e.triggerCondition(m, vars)
	case "zone":
		return e.zoneCondition(m)
	case "device":
		return false, fmt.Errorf("device conditions are not supported")
	case "and", "or", "not":
		subs := asList(m["conditions"])
		if len(subs) == 0 {
			return false, fmt.Errorf("%s condition needs conditions", typ)
		}
		results := make([]bool, len(subs))
		for i, s := range subs {
			r, err := e.checkCondition(s, vars)
			if err != nil {
				return false, err
			}
			results[i] = r
		}
		switch typ {
		case "and":
			for _, r := range results {
				if !r {
					return false, nil
				}
			}
			return true, nil
		case "or":
			for _, r := range results {
				if r {
					return true, nil
				}
			}
			return false, nil
		default:
			for _, r := range results {
				if r {
					return false, nil
				}
			}
			return true, nil
		}
	case "":
		// Shorthand "and"/"or"/"not" with the key as the condition type.
		for _, k := range []string{"and", "or", "not"} {
			if v, ok := m[k]; ok {
				return e.checkCondition(map[string]any{"condition": k, "conditions": v}, vars)
			}
		}
		return false, fmt.Errorf("condition without type")
	}
	return false, fmt.Errorf("unsupported condition %q", typ)
}

func (e *Engine) stateCondition(m map[string]any) (bool, error) {
	ids := strList(m["entity_id"])
	if len(ids) == 0 {
		return false, fmt.Errorf("state condition needs entity_id")
	}
	want := strList(m["state"])
	attr := str(m["attribute"])
	matchAny := str(m["match"]) == "any"
	var minFor time.Duration
	hasFor := false
	if raw, ok := m["for"]; ok && raw != nil {
		var err error
		minFor, err = parseDuration(raw)
		if err != nil {
			return false, err
		}
		hasFor = true
	}
	for _, id := range ids {
		st := e.core.GetState(id)
		ok := false
		if st != nil {
			v := st.State
			if attr != "" {
				v = attrString(st, attr)
			}
			for _, w := range want {
				if w == v {
					ok = true
					break
				}
			}
			if ok && hasFor && e.now().Sub(st.LastChanged) < minFor {
				ok = false
			}
		}
		if matchAny && ok {
			return true, nil
		}
		if !matchAny && !ok {
			return false, nil
		}
	}
	return !matchAny, nil
}

func (e *Engine) numericCondition(m map[string]any, vars map[string]any) (bool, error) {
	ids := strList(m["entity_id"])
	if len(ids) == 0 {
		return false, fmt.Errorf("numeric_state condition needs entity_id")
	}
	for _, id := range ids {
		st := e.core.GetState(id)
		f, ok := e.numericValue(m, st, vars)
		if !ok {
			return false, nil
		}
		in, err := e.inRange(m, f, vars)
		if err != nil {
			return false, err
		}
		if !in {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) clockOf(raw node) (time.Time, bool, error) {
	if raw == nil {
		return time.Time{}, false, nil
	}
	s := str(raw)
	if core.ValidEntityID(s) {
		st := e.core.GetState(s)
		if st == nil {
			return time.Time{}, false, fmt.Errorf("entity %s has no state", s)
		}
		s = st.State
		if t, ok := template.ParseDateTime(s); ok {
			now := e.now()
			t = t.In(now.Location())
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location()), true, nil
		}
	}
	h, mi, sec, err := parseClock(s)
	if err != nil {
		return time.Time{}, false, err
	}
	now := e.now()
	return time.Date(now.Year(), now.Month(), now.Day(), h, mi, sec, 0, now.Location()), true, nil
}

func (e *Engine) timeCondition(m map[string]any) (bool, error) {
	after, hasAfter, err := e.clockOf(m["after"])
	if err != nil {
		return false, err
	}
	before, hasBefore, err := e.clockOf(m["before"])
	if err != nil {
		return false, err
	}
	now := e.now()
	if days := strList(m["weekday"]); len(days) > 0 {
		names := map[time.Weekday]string{time.Monday: "mon", time.Tuesday: "tue", time.Wednesday: "wed", time.Thursday: "thu", time.Friday: "fri", time.Saturday: "sat", time.Sunday: "sun"}
		found := false
		for _, d := range days {
			if strings.ToLower(d) == names[now.Weekday()] {
				found = true
			}
		}
		if !found {
			return false, nil
		}
	}
	switch {
	case hasAfter && hasBefore && before.Before(after):
		// Window across midnight, e.g. after 22:00 before 06:00.
		return !now.Before(after) || now.Before(before), nil
	case hasAfter && now.Before(after):
		return false, nil
	case hasBefore && !now.Before(before):
		return false, nil
	}
	return true, nil
}

func (e *Engine) sunCondition(m map[string]any) (bool, error) {
	if !e.hasLocation() {
		return false, fmt.Errorf("sun condition needs latitude and longitude")
	}
	now := e.now()
	rise, set, ok := sunTimes(now, e.opts.Latitude, e.opts.Longitude)
	if !ok {
		return false, fmt.Errorf("no sunrise or sunset today at this location")
	}
	pickEvent := func(ev string) time.Time {
		if ev == "sunset" {
			return set
		}
		return rise
	}
	if a := str(m["after"]); a != "" {
		t := pickEvent(a)
		if raw, ok := m["after_offset"]; ok {
			d, err := parseSignedDuration(raw)
			if err != nil {
				return false, err
			}
			t = t.Add(d)
		}
		if now.Before(t) {
			return false, nil
		}
	}
	if b := str(m["before"]); b != "" {
		t := pickEvent(b)
		if raw, ok := m["before_offset"]; ok {
			d, err := parseSignedDuration(raw)
			if err != nil {
				return false, err
			}
			t = t.Add(d)
		}
		if !now.Before(t) {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) triggerCondition(m map[string]any, vars map[string]any) (bool, error) {
	trig, _ := vars["trigger"].(map[string]any)
	id := str(trig["id"])
	for _, want := range strList(m["id"]) {
		if want == id {
			return true, nil
		}
	}
	return false, nil
}
