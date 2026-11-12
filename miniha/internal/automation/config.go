/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package automation runs HA-style automations, scripts and scenes from
// automations.yaml, scripts.yaml and scenes.yaml.
package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/probonopd/miniha/internal/core"
)

// node is a decoded YAML value: map[string]any, []any or a scalar.
type node = any

type automationConfig struct {
	ID          string
	Alias       string
	Description string
	Mode        string
	Max         int
	Enabled     bool
	Triggers    []map[string]any
	Conditions  []node
	Actions     []node
	Variables   map[string]any
}

type scriptConfig struct {
	ID        string
	Alias     string
	Mode      string
	Max       int
	Sequence  []node
	Variables map[string]any
}

type sceneConfig struct {
	ID       string
	Name     string
	Entities map[string]any
}

func asMap(n node) (map[string]any, bool) {
	m, ok := n.(map[string]any)
	return m, ok
}

// asList accepts a list or a single item (HA allows both everywhere).
func asList(n node) []node {
	switch x := n.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []node{n}
}

// pick returns the first present key (HA renamed several keys over time).
func pick(m map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

func str(n node) string {
	switch x := n.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(n)
}

func strList(n node) []string {
	var out []string
	for _, it := range asList(n) {
		s := strings.TrimSpace(str(it))
		if strings.Contains(s, ",") && !strings.Contains(s, "{") {
			for _, p := range strings.Split(s, ",") {
				out = append(out, strings.TrimSpace(p))
			}
			continue
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseAutomations reads automations.yaml. Entries that use a blueprint
// are expanded from <dataDir>/blueprints/automation; with an empty
// dataDir only their shape is checked and they are left out of the
// result.
func parseAutomations(data []byte, dataDir string) ([]automationConfig, error) {
	var root node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	var out []automationConfig
	for i, item := range asList(root) {
		m, ok := asMap(item)
		if !ok {
			return nil, fmt.Errorf("automation %d: not a mapping", i+1)
		}
		if ub, ok := m["use_blueprint"]; ok {
			use, err := parseUseBlueprint(ub)
			if err != nil {
				return nil, fmt.Errorf("automation %d: %w", i+1, err)
			}
			if dataDir == "" {
				continue
			}
			expanded, err := expandBlueprint(dataDir, use, m)
			if err != nil {
				return nil, fmt.Errorf("automation %d (%s): %w", i+1, blueprintLabel(m), err)
			}
			m = expanded
		}
		a, err := parseAutomation(m, i)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func parseAutomation(m map[string]any, i int) (automationConfig, error) {
	a := automationConfig{Mode: "single", Max: 10, Enabled: true}
	a.ID = str(m["id"])
	a.Alias = str(m["alias"])
	a.Description = str(m["description"])
	if a.Alias == "" {
		if a.ID == "" {
			return a, fmt.Errorf("automation %d: alias is required", i+1)
		}
		a.Alias = a.ID
	}
	if v, ok := m["mode"]; ok {
		a.Mode = str(v)
	}
	switch a.Mode {
	case "single", "restart", "queued", "parallel":
	default:
		return a, fmt.Errorf("automation %q: invalid mode %q", a.Alias, a.Mode)
	}
	if v, ok := m["max"]; ok {
		n, err := strconv.Atoi(str(v))
		if err != nil || n < 1 {
			return a, fmt.Errorf("automation %q: max must be a positive integer", a.Alias)
		}
		a.Max = n
	}
	if v, ok := pick(m, "initial_state", "enabled"); ok {
		a.Enabled = truthyNode(v)
	}
	trig, ok := pick(m, "triggers", "trigger")
	if !ok {
		return a, fmt.Errorf("automation %q: trigger is required", a.Alias)
	}
	for j, t := range asList(trig) {
		tm, ok := asMap(t)
		if !ok {
			return a, fmt.Errorf("automation %q: trigger %d is not a mapping", a.Alias, j+1)
		}
		if _, ok := pick(tm, "platform", "trigger"); !ok {
			return a, fmt.Errorf("automation %q: trigger %d has no platform", a.Alias, j+1)
		}
		a.Triggers = append(a.Triggers, tm)
	}
	if v, ok := pick(m, "conditions", "condition"); ok {
		a.Conditions = asList(v)
	}
	act, ok := pick(m, "actions", "action")
	if !ok {
		return a, fmt.Errorf("automation %q: action is required", a.Alias)
	}
	a.Actions = asList(act)
	if v, ok := asMap(m["variables"]); ok {
		a.Variables = v
	}
	for k := range m {
		switch k {
		case "id", "alias", "description", "mode", "max", "initial_state", "enabled", "triggers", "trigger", "conditions", "condition", "actions", "action", "variables", "trace", "max_exceeded", "trigger_variables":
		default:
			return a, fmt.Errorf("automation %q: unknown key %q", a.Alias, k)
		}
	}
	return a, nil
}

func parseScripts(data []byte) ([]scriptConfig, error) {
	var root node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	m, ok := asMap(root)
	if !ok {
		if root == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("scripts.yaml must be a mapping of script id to script")
	}
	var out []scriptConfig
	for id, raw := range m {
		sm, ok := asMap(raw)
		if !ok {
			return nil, fmt.Errorf("script %q: not a mapping", id)
		}
		if core.Slugify(id) != id {
			return nil, fmt.Errorf("script %q: id must be a slug", id)
		}
		s := scriptConfig{ID: id, Alias: str(sm["alias"]), Mode: "single", Max: 10}
		if s.Alias == "" {
			s.Alias = id
		}
		if v, ok := sm["mode"]; ok {
			s.Mode = str(v)
		}
		if v, ok := sm["max"]; ok {
			s.Max, _ = strconv.Atoi(str(v))
		}
		seq, ok := sm["sequence"]
		if !ok {
			return nil, fmt.Errorf("script %q: sequence is required", id)
		}
		s.Sequence = asList(seq)
		if v, ok := asMap(sm["variables"]); ok {
			s.Variables = v
		}
		out = append(out, s)
	}
	return out, nil
}

func parseScenes(data []byte) ([]sceneConfig, error) {
	var root node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	var out []sceneConfig
	for i, item := range asList(root) {
		m, ok := asMap(item)
		if !ok {
			return nil, fmt.Errorf("scene %d: not a mapping", i+1)
		}
		s := sceneConfig{ID: str(m["id"]), Name: str(m["name"])}
		if s.Name == "" {
			return nil, fmt.Errorf("scene %d: name is required", i+1)
		}
		ents, ok := asMap(m["entities"])
		if !ok {
			return nil, fmt.Errorf("scene %q: entities is required", s.Name)
		}
		for eid := range ents {
			if !core.ValidEntityID(eid) {
				return nil, fmt.Errorf("scene %q: invalid entity id %q", s.Name, eid)
			}
		}
		s.Entities = ents
		out = append(out, s)
	}
	return out, nil
}

func truthyNode(n node) bool {
	switch x := n.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(x) {
		case "true", "on", "yes", "1":
			return true
		}
		return false
	}
	return n != nil
}

// parseDuration reads HA's duration forms: "HH:MM:SS", "MM:SS", a number
// of seconds, or a mapping with days/hours/minutes/seconds/milliseconds.
func parseDuration(n node) (time.Duration, error) {
	switch x := n.(type) {
	case nil:
		return 0, fmt.Errorf("duration missing")
	case map[string]any:
		var d time.Duration
		for k, unit := range map[string]time.Duration{"days": 24 * time.Hour, "hours": time.Hour, "minutes": time.Minute, "seconds": time.Second, "milliseconds": time.Millisecond} {
			if v, ok := x[k]; ok {
				f, err := strconv.ParseFloat(str(v), 64)
				if err != nil {
					return 0, fmt.Errorf("duration %s: %v", k, err)
				}
				d += time.Duration(f * float64(unit))
			}
		}
		return d, nil
	case string:
		s := strings.TrimSpace(x)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return time.Duration(f * float64(time.Second)), nil
		}
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		var total float64
		for _, p := range parts {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total = total*60 + f
		}
		if len(parts) == 2 {
			// "MM:SS" is minutes and seconds in HA.
			return time.Duration(total * float64(time.Second)), nil
		}
		return time.Duration(total * float64(time.Second)), nil
	default:
		f, err := strconv.ParseFloat(str(n), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %v", n)
		}
		return time.Duration(f * float64(time.Second)), nil
	}
}

// parseClock reads "HH:MM" or "HH:MM:SS".
func parseClock(s string) (h, m, sec int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("invalid time %q", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		nums[i], err = strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid time %q", s)
		}
	}
	if nums[0] > 23 || nums[1] > 59 || nums[2] > 59 {
		return 0, 0, 0, fmt.Errorf("invalid time %q", s)
	}
	return nums[0], nums[1], nums[2], nil
}
