/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"sort"
	"time"
)

// Clock is what now() uses; automations swap it in tests.
var Clock = func() time.Time { return time.Now() }

func asTimestamp(v any) (float64, error) {
	switch x := v.(type) {
	case DateTime:
		return float64(x.T.UnixNano()) / 1e9, nil
	case string:
		if t, ok := ParseDateTime(x); ok {
			return float64(t.UnixNano()) / 1e9, nil
		}
	default:
		if f, ok := toFloat(v); ok {
			return f, nil
		}
	}
	return 0, fmt.Errorf("as_timestamp: cannot convert %s", pyrepr(v))
}

func asDatetime(v any) (DateTime, error) {
	switch x := v.(type) {
	case DateTime:
		return x, nil
	case string:
		if t, ok := ParseDateTime(x); ok {
			return DateTime{t}, nil
		}
	default:
		if f, ok := toFloat(v); ok {
			sec := int64(f)
			return DateTime{time.Unix(sec, int64((f-float64(sec))*1e9)).UTC()}, nil
		}
	}
	return DateTime{}, fmt.Errorf("as_datetime: cannot convert %s", pyrepr(v))
}

func withDefault(err error, args []any, kw map[string]any, idx int) (any, error) {
	def := arg(args, kw, idx, "default", sentinel)
	if def == sentinel {
		return nil, err
	}
	return def, nil
}

// attrOf reads an attribute path like "attributes.brightness".
func attrOf(v any, path string) (any, error) {
	cur := v
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			var err error
			cur, err = getAttr(cur, path[start:i])
			if err != nil {
				return nil, err
			}
			start = i + 1
		}
	}
	return cur, nil
}

func runTest(name string, v any, args []any) (bool, error) {
	t, ok := tests[name]
	if !ok {
		switch name {
		case "==", "equalto":
			t = tests["eq"]
		case "!=":
			t = tests["ne"]
		case "in":
			return func() (bool, error) {
				if len(args) != 1 {
					return false, fmt.Errorf("in needs one argument")
				}
				r, err := binop("in", v, args[0])
				return r == true, err
			}()
		case ">", "<", ">=", "<=":
			if len(args) != 1 {
				return false, fmt.Errorf("%s needs one argument", name)
			}
			r, err := binop(name, v, args[0])
			return r == true, err
		default:
			return false, fmt.Errorf("unknown test %q", name)
		}
	}
	return t(v, args)
}

func init() {
	functions["now"] = func(_ *evalCtx, _ []any, _ map[string]any) (any, error) { return DateTime{Clock()}, nil }
	functions["utcnow"] = func(_ *evalCtx, _ []any, _ map[string]any) (any, error) { return DateTime{Clock().UTC()}, nil }
	functions["timedelta"] = func(_ *evalCtx, _ []any, kw map[string]any) (any, error) {
		var d time.Duration
		for k, unit := range map[string]time.Duration{"days": 24 * time.Hour, "hours": time.Hour, "minutes": time.Minute, "seconds": time.Second, "milliseconds": time.Millisecond, "weeks": 7 * 24 * time.Hour} {
			if v, ok := kw[k]; ok {
				f, ok := toFloat(v)
				if !ok {
					return nil, fmt.Errorf("timedelta: %s must be a number", k)
				}
				d += time.Duration(f * float64(unit))
			}
		}
		return TimeDelta{d}, nil
	}
	functions["as_timestamp"] = func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("as_timestamp needs a value")
		}
		f, err := asTimestamp(args[0])
		if err != nil {
			return withDefault(err, args, kw, 1)
		}
		return f, nil
	}
	functions["as_datetime"] = func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("as_datetime needs a value")
		}
		d, err := asDatetime(args[0])
		if err != nil {
			return withDefault(err, args, kw, 1)
		}
		return d, nil
	}
	functions["as_local"] = func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("as_local needs a value")
		}
		d, err := asDatetime(args[0])
		if err != nil {
			return nil, err
		}
		return DateTime{d.T.In(Clock().Location())}, nil
	}
	functions["strptime"] = func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("strptime needs a string and a format")
		}
		t, ok := ParseDateTime(pystr(args[0]))
		if !ok {
			return withDefault(fmt.Errorf("strptime: cannot parse %s", pyrepr(args[0])), args, kw, 2)
		}
		return DateTime{t}, nil
	}

	filters["as_timestamp"] = func(c *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		return functions["as_timestamp"](c, append([]any{v}, args...), kw)
	}
	filters["as_datetime"] = func(c *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		return functions["as_datetime"](c, append([]any{v}, args...), kw)
	}
	filters["as_local"] = func(c *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		return functions["as_local"](c, []any{v}, kw)
	}
	filters["timestamp_custom"] = func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		f, err := asTimestamp(v)
		if err != nil {
			return withDefault(err, args, kw, 2)
		}
		format := pystr(arg(args, kw, 0, "format_string", "%Y-%m-%dT%H:%M:%S"))
		local := truthy(arg(args, kw, 1, "local", true))
		t := time.Unix(int64(f), int64((f-float64(int64(f)))*1e9))
		if local {
			t = t.In(Clock().Location())
		} else {
			t = t.UTC()
		}
		return Strftime(t, format), nil
	}
	filters["timestamp_local"] = func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		f, err := asTimestamp(v)
		if err != nil {
			return withDefault(err, args, kw, 0)
		}
		return DateTime{time.Unix(int64(f), 0).In(Clock().Location())}.isoformat(), nil
	}
	filters["timestamp_utc"] = func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		f, err := asTimestamp(v)
		if err != nil {
			return withDefault(err, args, kw, 0)
		}
		return DateTime{time.Unix(int64(f), 0).UTC()}.isoformat(), nil
	}
	filters["count"] = filters["length"]
	// Jinja2's short alias, used in HA's MQTT light template examples.
	filters["d"] = filters["default"]
	filters["unique"] = func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
		items, err := toList(v)
		if err != nil {
			return nil, err
		}
		var out []any
		for _, it := range items {
			dup := false
			for _, o := range out {
				if pyEqual(it, o) {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, it)
			}
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	}
	filters["sort"] = func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		items, err := toList(v)
		if err != nil {
			return nil, err
		}
		out := append([]any{}, items...)
		reverse := truthy(arg(args, kw, 0, "reverse", false))
		attr := pystr(arg(args, kw, 2, "attribute", ""))
		key := func(x any) (any, error) {
			if attr == "" {
				return x, nil
			}
			return attrOf(x, attr)
		}
		var sortErr error
		sort.SliceStable(out, func(i, j int) bool {
			a, err1 := key(out[i])
			b, err2 := key(out[j])
			if err1 != nil || err2 != nil {
				sortErr = fmt.Errorf("sort: %v", err1)
				return false
			}
			cmp, err := pyCompare(a, b)
			if err != nil {
				sortErr = err
				return false
			}
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
		return out, sortErr
	}
	filters["map"] = func(c *evalCtx, v any, args []any, kw map[string]any) (any, error) {
		items, err := toList(v)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(items))
		if attr, ok := kw["attribute"]; ok {
			def, hasDef := kw["default"]
			for _, it := range items {
				r, err := attrOf(it, pystr(attr))
				if err != nil {
					return nil, err
				}
				if _, undef := r.(Undefined); undef && hasDef {
					r = def
				}
				out = append(out, r)
			}
			return out, nil
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("map needs a filter name or attribute=")
		}
		f, ok := filters[pystr(args[0])]
		if !ok {
			return nil, fmt.Errorf("map: unknown filter %q", pystr(args[0]))
		}
		for _, it := range items {
			r, err := f(c, it, args[1:], nil)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	}
	selectBy := func(keep bool, byAttr bool) filterFn {
		return func(_ *evalCtx, v any, args []any, _ map[string]any) (any, error) {
			items, err := toList(v)
			if err != nil {
				return nil, err
			}
			var attr string
			if byAttr {
				if len(args) == 0 {
					return nil, fmt.Errorf("selectattr needs an attribute")
				}
				attr = pystr(args[0])
				args = args[1:]
			}
			out := []any{}
			for _, it := range items {
				val := it
				if byAttr {
					val, err = attrOf(it, attr)
					if err != nil {
						return nil, err
					}
				}
				var ok bool
				if len(args) == 0 {
					ok = truthy(val)
				} else {
					ok, err = runTest(pystr(args[0]), val, args[1:])
					if err != nil {
						return nil, err
					}
				}
				if ok == keep {
					out = append(out, it)
				}
			}
			return out, nil
		}
	}
	filters["select"] = selectBy(true, false)
	filters["reject"] = selectBy(false, false)
	filters["selectattr"] = selectBy(true, true)
	filters["rejectattr"] = selectBy(false, true)
	filters["attr"] = func(_ *evalCtx, v any, args []any, _ map[string]any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("attr needs a name")
		}
		return getAttr(v, pystr(args[0]))
	}
	filters["items"] = func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
		d, ok := v.(*Dict)
		if !ok {
			return nil, fmt.Errorf("items: not a dict")
		}
		return dictMethod(d, "items")(nil, nil)
	}
	filters["reverse"] = func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
		items, err := toList(v)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, it := range items {
			out[len(items)-1-i] = it
		}
		return out, nil
	}
	filters["slice"] = func(_ *evalCtx, v any, args []any, _ map[string]any) (any, error) {
		items, err := toList(v)
		if err != nil {
			return nil, err
		}
		n, _ := arg(args, nil, 0, "", int64(1)).(int64)
		if n <= 0 {
			return nil, fmt.Errorf("slice needs a positive count")
		}
		out := []any{}
		per := (len(items) + int(n) - 1) / int(n)
		for i := 0; i < len(items); i += per {
			out = append(out, items[i:min(i+per, len(items))])
		}
		return out, nil
	}
}
