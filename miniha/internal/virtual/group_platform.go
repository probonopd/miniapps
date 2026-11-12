/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package virtual

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/probonopd/miniha/internal/core"
)

var sensorGroupTypes = map[string]bool{"min": true, "max": true, "mean": true, "median": true, "sum": true, "last": true, "range": true, "product": true, "count": true}

// newPlatform builds a "platform: group" entity of the given domain.
func (g *Groups) newPlatform(domain string, m map[string]any) (*groupEntity, error) {
	allowed := []string{"platform", "name", "entities", "unique_id"}
	switch domain {
	case "light", "switch":
		allowed = append(allowed, "all")
	case "binary_sensor":
		allowed = append(allowed, "all", "device_class")
	case "sensor":
		allowed = append(allowed, "type", "unit_of_measurement", "device_class", "state_class", "ignore_non_numeric")
	case "cover", "fan":
	default:
		return nil, fmt.Errorf("group platform is not implemented for domain %s", domain)
	}
	if err := checkKeys(m, allowed...); err != nil {
		return nil, err
	}
	name, err := strOpt(m, "name")
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if m["entities"] == nil {
		return nil, fmt.Errorf("entities is required")
	}
	members, err := entityList(m["entities"])
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("entities is empty")
	}
	uid, err := strOpt(m, "unique_id")
	if err != nil {
		return nil, err
	}
	if uid == "" {
		uid = "group:" + domain + ":" + name
	}
	all, err := boolOpt(m, "all")
	if err != nil {
		return nil, err
	}
	deviceClass, err := strOpt(m, "device_class")
	if err != nil {
		return nil, err
	}
	e := &groupEntity{g: g, members: members, base: map[string]any{}}
	e.reg = &core.EntityRegistration{Domain: domain, Platform: groupPlatform, UniqueID: uid, OriginalName: name, DeviceClass: deviceClass}
	if deviceClass != "" {
		e.base["device_class"] = deviceClass
	}
	switch domain {
	case "light":
		e.compute = lightCompute(all)
		e.ctl = onOffCtl{e, "light"}
	case "switch":
		e.compute = onOffCompute(all, nil)
		e.ctl = onOffCtl{e, "switch"}
	case "binary_sensor":
		e.compute = onOffCompute(all, nil)
	case "cover":
		e.compute = coverCompute
		e.ctl = coverCtl{e}
	case "fan":
		e.compute = fanCompute
		e.ctl = fanCtl{onOffCtl{e, "fan"}}
	case "sensor":
		typ, err := strOpt(m, "type")
		if err != nil {
			return nil, err
		}
		if !sensorGroupTypes[typ] {
			return nil, fmt.Errorf("type %q is not one of min, max, mean, median, sum, last, range, product, count", typ)
		}
		ignore, err := boolOpt(m, "ignore_non_numeric")
		if err != nil {
			return nil, err
		}
		unit, err := strOpt(m, "unit_of_measurement")
		if err != nil {
			return nil, err
		}
		stateClass, err := strOpt(m, "state_class")
		if err != nil {
			return nil, err
		}
		e.reg.Unit = unit
		if unit != "" {
			e.base["unit_of_measurement"] = unit
		}
		if stateClass != "" {
			e.base["state_class"] = stateClass
		}
		e.compute = sensorCompute(typ, ignore, unit == "")
	}
	return e, nil
}

// --- shared helpers ---

func stateIsOn(s *core.State) bool { return s.State == core.StateOn }

func onOffCompute(all bool, extra func(on []*core.State, attrs map[string]any)) computeFn {
	return func(states []*core.State) (string, map[string]any) {
		on, special := verdict(states, all, stateIsOn)
		if special != "" {
			return special, nil
		}
		attrs := map[string]any{}
		if on {
			if extra != nil {
				var onStates []*core.State
				for _, s := range states {
					if s != nil && s.State == core.StateOn {
						onStates = append(onStates, s)
					}
				}
				extra(onStates, attrs)
			}
			return core.StateOn, attrs
		}
		return core.StateOff, attrs
	}
}

func strSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func attrFloats(states []*core.State, key string) []float64 {
	var out []float64
	for _, s := range states {
		if s == nil {
			continue
		}
		if f, ok := toFloat(s.Attributes[key]); ok {
			out = append(out, f)
		}
	}
	return out
}

func meanOf(fs []float64) float64 {
	var sum float64
	for _, f := range fs {
		sum += f
	}
	return sum / float64(len(fs))
}

// setMean stores the rounded mean of an integer attribute of the given
// states, when any of them has it.
func setMean(attrs map[string]any, name string, states []*core.State, key string) {
	if fs := attrFloats(states, key); len(fs) > 0 {
		attrs[name] = int(math.Round(meanOf(fs)))
	}
}

// setFeatures ORs the supported_features bitmasks of all members: the
// group supports what any member supports.
func setFeatures(attrs map[string]any, states []*core.State) {
	mask, found := 0, false
	for _, s := range states {
		if s == nil {
			continue
		}
		if f, ok := toInt(s.Attributes["supported_features"]); ok {
			mask |= f
			found = true
		}
	}
	if found {
		attrs["supported_features"] = mask
	}
}

// --- light ---

// colorModePriority orders the modes a mixed group can report; the first
// one any switched-on member is in wins.
var colorModePriority = []string{"hs", "xy", "rgbww", "rgbw", "rgb", "color_temp", "white", "brightness", "onoff"}

func lightCompute(all bool) computeFn {
	return func(states []*core.State) (string, map[string]any) {
		on, special := verdict(states, all, stateIsOn)
		attrs := map[string]any{}
		modes := map[string]bool{}
		for _, s := range states {
			if s == nil {
				continue
			}
			for _, m := range strSlice(s.Attributes["supported_color_modes"]) {
				modes[m] = true
			}
		}
		if len(modes) > 0 {
			list := make([]string, 0, len(modes))
			for m := range modes {
				list = append(list, m)
			}
			sort.Strings(list)
			attrs["supported_color_modes"] = anyList(list)
		}
		setFeatures(attrs, states)
		if lo := attrFloats(states, "min_color_temp_kelvin"); len(lo) > 0 {
			attrs["min_color_temp_kelvin"] = int(math.Round(minOf(lo)))
		}
		if hi := attrFloats(states, "max_color_temp_kelvin"); len(hi) > 0 {
			attrs["max_color_temp_kelvin"] = int(math.Round(maxOf(hi)))
		}
		if special != "" {
			return special, attrs
		}
		if !on {
			return core.StateOff, attrs
		}
		var onStates []*core.State
		for _, s := range states {
			if s != nil && s.State == core.StateOn {
				onStates = append(onStates, s)
			}
		}
		setMean(attrs, "brightness", onStates, "brightness")
		setMean(attrs, "color_temp_kelvin", onStates, "color_temp_kelvin")
		present := map[string]bool{}
		for _, s := range onStates {
			if cm, ok := s.Attributes["color_mode"].(string); ok {
				present[cm] = true
			}
		}
		for _, cm := range colorModePriority {
			if present[cm] {
				attrs["color_mode"] = cm
				break
			}
		}
		// The three color spaces of one member stay together, otherwise
		// a group could report an rgb_color that contradicts its hs_color.
		for _, s := range onStates {
			found := false
			for _, key := range []string{"hs_color", "rgb_color", "xy_color"} {
				if v, ok := s.Attributes[key]; ok && v != nil {
					attrs[key] = v
					found = true
				}
			}
			if found {
				break
			}
		}
		return core.StateOn, attrs
	}
}

func minOf(fs []float64) float64 {
	m := fs[0]
	for _, f := range fs[1:] {
		m = math.Min(m, f)
	}
	return m
}

func maxOf(fs []float64) float64 {
	m := fs[0]
	for _, f := range fs[1:] {
		m = math.Max(m, f)
	}
	return m
}

// --- fan ---

func fanCompute(states []*core.State) (string, map[string]any) {
	state, attrs := onOffCompute(false, func(on []*core.State, attrs map[string]any) {
		setMean(attrs, "percentage", on, "percentage")
	})(states)
	if attrs == nil {
		attrs = map[string]any{}
	}
	setFeatures(attrs, states)
	return state, attrs
}

// --- cover ---

func coverCompute(states []*core.State) (string, map[string]any) {
	attrs := map[string]any{}
	setFeatures(attrs, states)
	setMean(attrs, "current_position", states, "current_position")
	count := map[string]int{}
	known := 0
	unavailable := 0
	for _, s := range states {
		switch {
		case s == nil || s.State == core.StateUnavailable:
			unavailable++
		case s.State == core.StateUnknown:
		default:
			count[s.State]++
			known++
		}
	}
	switch {
	case known == 0 && len(states) > 0 && unavailable == len(states):
		return core.StateUnavailable, attrs
	case known == 0:
		return core.StateUnknown, attrs
	case count["opening"] > 0:
		return "opening", attrs
	case count["closing"] > 0:
		return "closing", attrs
	case count["closed"] == known:
		return "closed", attrs
	case count["open"] > 0 || count["closed"] > 0:
		return "open", attrs
	}
	return core.StateUnknown, attrs
}

// --- sensor ---

// sensorCompute aggregates the numeric members. Members that are not
// numeric make the group unavailable (missing or unavailable members) or
// unknown (anything else) unless ignore is set.
func sensorCompute(typ string, ignore, deriveUnit bool) computeFn {
	return func(states []*core.State) (string, map[string]any) {
		attrs := map[string]any{}
		type reading struct {
			v  float64
			st *core.State
		}
		var rs []reading
		for _, s := range states {
			var f float64
			ok := false
			if s != nil {
				f, ok = toFloat(s.State)
			}
			if ok {
				rs = append(rs, reading{f, s})
				continue
			}
			if ignore {
				continue
			}
			if s == nil || s.State == core.StateUnavailable {
				return core.StateUnavailable, attrs
			}
			return core.StateUnknown, attrs
		}
		if deriveUnit {
			unit, same := "", true
			for i, s := range states {
				u := ""
				if s != nil {
					u, _ = s.Attributes["unit_of_measurement"].(string)
				}
				if i == 0 {
					unit = u
				} else if u != unit {
					same = false
				}
			}
			if same && unit != "" {
				attrs["unit_of_measurement"] = unit
			}
		}
		if len(rs) == 0 {
			return core.StateUnknown, attrs
		}
		vals := make([]float64, len(rs))
		for i, r := range rs {
			vals[i] = r.v
		}
		var out float64
		switch typ {
		case "min":
			out = minOf(vals)
			for _, r := range rs {
				if r.v == out {
					attrs["min_entity_id"] = r.st.EntityID
					break
				}
			}
		case "max":
			out = maxOf(vals)
			for _, r := range rs {
				if r.v == out {
					attrs["max_entity_id"] = r.st.EntityID
					break
				}
			}
		case "mean":
			out = meanOf(vals)
		case "median":
			sorted := append([]float64{}, vals...)
			sort.Float64s(sorted)
			n := len(sorted)
			if n%2 == 1 {
				out = sorted[n/2]
			} else {
				out = (sorted[n/2-1] + sorted[n/2]) / 2
			}
		case "sum":
			for _, v := range vals {
				out += v
			}
		case "product":
			out = 1
			for _, v := range vals {
				out *= v
			}
		case "range":
			out = maxOf(vals) - minOf(vals)
		case "count":
			return strconv.Itoa(len(vals)), attrs
		case "last":
			latest := rs[0]
			for _, r := range rs[1:] {
				if r.st.LastUpdated.After(latest.st.LastUpdated) {
					latest = r
				}
			}
			out = latest.v
			attrs["last_entity_id"] = latest.st.EntityID
		}
		return pyFloat(out), attrs
	}
}
