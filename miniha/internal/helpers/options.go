/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// opts reads the options of one helper definition and, in finish, rejects
// every key nobody asked for: a typo in configuration.yaml must fail at
// load time instead of silently doing nothing.
type opts struct {
	domain string
	id     string
	m      map[string]any
	used   map[string]bool
}

func newOpts(domain, id string, raw any) (*opts, error) {
	if !core.ValidEntityID(domain + "." + id) {
		return nil, fmt.Errorf("%s: %q is not a valid object id (lowercase letters, digits and underscores)", domain, id)
	}
	m, err := asMap(raw)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", domain, id, err)
	}
	return &opts{domain: domain, id: id, m: m, used: map[string]bool{}}, nil
}

// asMap accepts the map shapes the YAML decoders produce. A nil value is
// an empty mapping because HA allows "my_helper:" without any options.
func asMap(v any) (map[string]any, error) {
	switch x := v.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return x, nil
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", k)
			}
			out[ks] = val
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected a mapping, got %T", v)
}

func (o *opts) errf(key, format string, args ...any) error {
	return fmt.Errorf("%s.%s: %s: %s", o.domain, o.id, key, fmt.Sprintf(format, args...))
}

// get returns the raw value; a null value counts as absent.
func (o *opts) get(key string) (any, bool) {
	o.used[key] = true
	v, ok := o.m[key]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

func (o *opts) str(key string) (string, error) {
	v, ok := o.get(key)
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", o.errf(key, "must be a string, got %T", v)
	}
	return s, nil
}

func (o *opts) boolean(key string) (*bool, error) {
	v, ok := o.get(key)
	if !ok {
		return nil, nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil, o.errf(key, "must be true or false, got %T", v)
	}
	return &b, nil
}

func (o *opts) number(key string) (*float64, error) {
	v, ok := o.get(key)
	if !ok {
		return nil, nil
	}
	f, err := toFloat(v)
	if err != nil {
		return nil, o.errf(key, "%v", err)
	}
	return &f, nil
}

func (o *opts) integer(key string) (*int, error) {
	v, ok := o.get(key)
	if !ok {
		return nil, nil
	}
	n, err := toInt(v)
	if err != nil {
		return nil, o.errf(key, "%v", err)
	}
	return &n, nil
}

// nameIcon reads the two options every helper has; the object id is the
// name when none is given, as in HA.
func (o *opts) nameIcon() (name, icon string, err error) {
	if name, err = o.str("name"); err != nil {
		return
	}
	if name == "" {
		name = o.id
	}
	icon, err = o.str("icon")
	return
}

func (o *opts) finish() error {
	var unknown []string
	for k := range o.m {
		if !o.used[k] {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	valid := make([]string, 0, len(o.used))
	for k := range o.used {
		valid = append(valid, k)
	}
	sort.Strings(valid)
	return fmt.Errorf("%s.%s: unknown option %s (valid options: %s)", o.domain, o.id, strings.Join(unknown, ", "), strings.Join(valid, ", "))
}

// toFloat accepts the numeric types a YAML or JSON decoder yields, plus
// numeric strings because service callers often send values as text.
func toFloat(v any) (float64, error) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
	case int8:
		f = float64(x)
	case int16:
		f = float64(x)
	case int32:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint:
		f = float64(x)
	case uint8:
		f = float64(x)
	case uint16:
		f = float64(x)
	case uint32:
		f = float64(x)
	case uint64:
		f = float64(x)
	case string:
		p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", x)
		}
		f = p
	default:
		return 0, fmt.Errorf("must be a number, got %T", v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("must be a finite number")
	}
	return f, nil
}

func toInt(v any) (int, error) {
	f, err := toFloat(v)
	if err != nil {
		return 0, err
	}
	if f != math.Trunc(f) {
		return 0, fmt.Errorf("must be a whole number, got %v", f)
	}
	return int(f), nil
}

// toBool is for service data, where booleans may arrive as strings.
func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "on", "yes", "1":
			return true, nil
		case "false", "off", "no", "0":
			return false, nil
		}
	}
	return false, fmt.Errorf("must be true or false, got %v", v)
}

// parseDuration reads what HA's time period validator accepts: seconds as
// a number or numeric string, "H:MM[:SS[.f]]" with an optional leading
// minus, or a mapping with days, hours, minutes, seconds, milliseconds.
func parseDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if strings.Contains(s, ":") {
			return parseClockDuration(s)
		}
	case map[string]any, map[any]any:
		m, err := asMap(x)
		if err != nil {
			return 0, err
		}
		units := map[string]time.Duration{
			"days": 24 * time.Hour, "hours": time.Hour, "minutes": time.Minute,
			"seconds": time.Second, "milliseconds": time.Millisecond,
		}
		var total time.Duration
		for k, val := range m {
			unit, ok := units[k]
			if !ok {
				return 0, fmt.Errorf("unknown duration key %q (valid: days, hours, minutes, seconds, milliseconds)", k)
			}
			f, err := toFloat(val)
			if err != nil {
				return 0, fmt.Errorf("duration %s: %w", k, err)
			}
			total += time.Duration(f * float64(unit))
		}
		return total, nil
	}
	secs, err := toFloat(v)
	if err != nil {
		return 0, fmt.Errorf("duration must be seconds, \"H:MM:SS\" or a mapping: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func parseClockDuration(s string) (time.Duration, error) {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("duration %q must look like H:MM or H:MM:SS", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 {
		return 0, fmt.Errorf("duration %q has an invalid hour", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("duration %q has an invalid minute", s)
	}
	var sec float64
	if len(parts) == 3 {
		sec, err = strconv.ParseFloat(parts[2], 64)
		if err != nil || sec < 0 || sec >= 60 {
			return 0, fmt.Errorf("duration %q has an invalid second", s)
		}
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second))
	if neg {
		d = -d
	}
	return d, nil
}

// formatDuration renders HA's "H:MM:SS", truncating to whole seconds.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64(d / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs%3600/60, secs%60)
}

// formatTime is Python's isoformat for an aware datetime, which is how HA
// prints finishes_at.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.999999-07:00")
}

// parseOptions reads an input_select option list: non-empty, unique,
// elements coerced to strings the way HA's cv.string does for numbers.
func parseOptions(v any) ([]string, error) {
	var items []any
	switch x := v.(type) {
	case []any:
		items = x
	case []string:
		for _, s := range x {
			items = append(items, s)
		}
	default:
		return nil, fmt.Errorf("must be a list of strings, got %T", v)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("must not be empty")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		var s string
		switch x := it.(type) {
		case string:
			s = x
		case int, int64, uint64, float64:
			s = fmt.Sprint(x)
		default:
			return nil, fmt.Errorf("option %v must be a string", it)
		}
		if seen[s] {
			return nil, fmt.Errorf("option %q is listed twice", s)
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}
