/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type filterFn func(c *evalCtx, v any, args []any, kw map[string]any) (any, error)
type testFn func(v any, args []any) (bool, error)
type function func(c *evalCtx, args []any, kw map[string]any) (any, error)

// arg returns positional argument i, or the keyword name, or def.
func arg(args []any, kw map[string]any, i int, name string, def any) any {
	if i < len(args) {
		return args[i]
	}
	if v, ok := kw[name]; ok {
		return v
	}
	return def
}

var sentinel = &struct{}{}

// parseFloat follows HA's forgiving_float: numbers pass through, strings
// are parsed, anything else is an error.
func parseFloat(v any) (float64, error) {
	if f, ok := toFloat(v); ok {
		return f, nil
	}
	if s, ok := v.(string); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("could not convert %s to float", pyrepr(v))
}

// parseInt follows HA's forgiving_int: floats truncate, strings parse in
// the given base.
func parseInt(v any, base int) (any, error) {
	switch x := v.(type) {
	case int64, *big.Int:
		return x, nil
	case bool:
		return toBig(x).Int64(), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return nil, fmt.Errorf("cannot convert %s to integer", pyfloat(x))
		}
		b, _ := new(big.Float).SetFloat64(math.Trunc(x)).Int(nil)
		return normInt(b), nil
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(x, "_", ""))
		if b, ok := new(big.Int).SetString(s, base); ok {
			return normInt(b), nil
		}
	}
	return nil, fmt.Errorf("could not convert %s to int", pyrepr(v))
}

func haFloat(v any, def any) (any, error) {
	f, err := parseFloat(v)
	if err != nil {
		if def == sentinel {
			return nil, err
		}
		return def, nil
	}
	return f, nil
}

func haInt(v any, def any, base int) (any, error) {
	i, err := parseInt(v, base)
	if err != nil {
		if def == sentinel {
			return nil, err
		}
		return def, nil
	}
	return i, nil
}

// haRound mirrors HA's forgiving_round: float input, HA rounding methods,
// and an int result when precision is 0.
func haRound(v any, args []any, kw map[string]any) (any, error) {
	precision := arg(args, kw, 0, "precision", int64(0))
	method := pystr(arg(args, kw, 1, "method", "common"))
	def := arg(args, kw, 2, "default", sentinel)
	p, ok := precision.(int64)
	if !ok {
		return nil, fmt.Errorf("precision must be an integer")
	}
	f, err := parseFloat(v)
	if err != nil {
		if def == sentinel {
			return nil, err
		}
		return def, nil
	}
	mult := math.Pow(10, float64(p))
	switch method {
	case "ceil":
		f = math.Ceil(f*mult) / mult
	case "floor":
		f = math.Floor(f*mult) / mult
	case "half":
		f = math.RoundToEven(f*2) / 2
	default:
		// Python's round() rounds the exact binary value half-to-even,
		// which strconv does when formatting with a fixed precision.
		if p >= 0 {
			f, _ = strconv.ParseFloat(strconv.FormatFloat(f, 'f', int(p), 64), 64)
		} else {
			f = math.RoundToEven(f/(1/mult)) * (1 / mult)
		}
	}
	if p == 0 {
		return parseInt(f, 10)
	}
	return f, nil
}

func toList(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case Undefined:
		return nil, x
	}
	return iterate(v)
}

func minmax(v any, want int) (any, error) {
	items, err := toList(v)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("arg is an empty sequence")
	}
	best := items[0]
	for _, it := range items[1:] {
		c, err := pyCompare(it, best)
		if err != nil {
			return nil, err
		}
		if c == want {
			best = it
		}
	}
	return best, nil
}

var filters map[string]filterFn

var tests = map[string]testFn{
	"defined":   func(v any, _ []any) (bool, error) { _, u := v.(Undefined); return !u, nil },
	"undefined": func(v any, _ []any) (bool, error) { _, u := v.(Undefined); return u, nil },
	"none":      func(v any, _ []any) (bool, error) { return v == nil, nil },
	"number":    func(v any, _ []any) (bool, error) { return isNumber(v), nil },
	"string":    func(v any, _ []any) (bool, error) { _, ok := v.(string); return ok, nil },
	"boolean":   func(v any, _ []any) (bool, error) { _, ok := v.(bool); return ok, nil },
	"true":      func(v any, _ []any) (bool, error) { b, ok := v.(bool); return ok && b, nil },
	"false":     func(v any, _ []any) (bool, error) { b, ok := v.(bool); return ok && !b, nil },
	"integer":   func(v any, _ []any) (bool, error) { _, b := v.(bool); return isInt(v) && !b, nil },
	"float":     func(v any, _ []any) (bool, error) { _, ok := v.(float64); return ok, nil },
	"list":      func(v any, _ []any) (bool, error) { _, ok := v.([]any); return ok, nil },
	"sequence": func(v any, _ []any) (bool, error) {
		switch v.(type) {
		case []any, string, *Dict:
			return true, nil
		}
		return false, nil
	},
	"iterable": func(v any, _ []any) (bool, error) {
		switch v.(type) {
		case []any, string, *Dict:
			return true, nil
		}
		return false, nil
	},
	"mapping": func(v any, _ []any) (bool, error) { _, ok := v.(*Dict); return ok, nil },
	"even": func(v any, _ []any) (bool, error) {
		if !isInt(v) {
			return false, fmt.Errorf("even: not an integer")
		}
		return toBig(v).Bit(0) == 0, nil
	},
	"odd": func(v any, _ []any) (bool, error) {
		if !isInt(v) {
			return false, fmt.Errorf("odd: not an integer")
		}
		return toBig(v).Bit(0) == 1, nil
	},
	"divisibleby": func(v any, args []any) (bool, error) {
		if len(args) != 1 || !isInt(v) || !isInt(args[0]) {
			return false, fmt.Errorf("divisibleby needs integers")
		}
		return new(big.Int).Mod(toBig(v), toBig(args[0])).Sign() == 0, nil
	},
	"eq": func(v any, args []any) (bool, error) { return len(args) == 1 && pyEqual(v, args[0]), nil },
	"ne": func(v any, args []any) (bool, error) { return len(args) == 1 && !pyEqual(v, args[0]), nil },
	"lower": func(v any, _ []any) (bool, error) {
		s, ok := v.(string)
		return ok && s == strings.ToLower(s), nil
	},
	"upper": func(v any, _ []any) (bool, error) {
		s, ok := v.(string)
		return ok && s == strings.ToUpper(s), nil
	},
}

var functions = map[string]function{
	"float": func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("float() needs an argument")
		}
		return haFloat(args[0], arg(args, kw, 1, "default", sentinel))
	},
	"int": func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("int() needs an argument")
		}
		return haInt(args[0], arg(args, kw, 1, "default", sentinel), 10)
	},
	"is_number": func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("is_number() takes one argument")
		}
		f, err := parseFloat(args[0])
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f), nil
	},
	"iif": func(_ *evalCtx, args []any, kw map[string]any) (any, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("iif() needs a value")
		}
		return iif(args[0], args[1:], kw)
	},
	"min": func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		if len(args) == 1 {
			return minmax(args[0], -1)
		}
		return minmax(args, -1)
	},
	"max": func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		if len(args) == 1 {
			return minmax(args[0], 1)
		}
		return minmax(args, 1)
	},
	"abs": func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("abs() takes one argument")
		}
		return pyabs(args[0])
	},
	"range": func(_ *evalCtx, args []any, _ map[string]any) (any, error) {
		var start, stop, step int64 = 0, 0, 1
		var ok bool
		switch len(args) {
		case 1:
			stop, ok = args[0].(int64)
		case 2:
			start, ok = args[0].(int64)
			if ok {
				stop, ok = args[1].(int64)
			}
		case 3:
			start, ok = args[0].(int64)
			if ok {
				stop, ok = args[1].(int64)
			}
			if ok {
				step, ok = args[2].(int64)
			}
		}
		if !ok || step == 0 {
			return nil, fmt.Errorf("range() needs integer arguments")
		}
		var out []any
		for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
			out = append(out, i)
		}
		return out, nil
	},
}

func iif(v any, args []any, kw map[string]any) (any, error) {
	ifTrue := arg(args, kw, 0, "if_true", true)
	ifFalse := arg(args, kw, 1, "if_false", false)
	ifNone := arg(args, kw, 2, "if_none", sentinel)
	if v == nil && ifNone != sentinel {
		return ifNone, nil
	}
	if truthy(v) {
		return ifTrue, nil
	}
	return ifFalse, nil
}

func pyabs(v any) (any, error) {
	switch x := v.(type) {
	case int64:
		if x < 0 {
			return -x, nil
		}
		return x, nil
	case *big.Int:
		return normInt(new(big.Int).Abs(x)), nil
	case float64:
		return math.Abs(x), nil
	case bool:
		return toBig(x).Int64(), nil
	}
	return nil, fmt.Errorf("bad operand type for abs(): '%s'", typeName(v))
}

func init() {
	filters = map[string]filterFn{
		"float": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			return haFloat(v, arg(args, kw, 0, "default", sentinel))
		},
		"int": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			base := arg(args, kw, 1, "base", int64(10))
			b, ok := base.(int64)
			if !ok {
				return nil, fmt.Errorf("base must be an integer")
			}
			return haInt(v, arg(args, kw, 0, "default", sentinel), int(b))
		},
		"round": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			return haRound(v, args, kw)
		},
		"multiply": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			amount, ok := toFloat(arg(args, kw, 0, "amount", nil))
			if !ok {
				return nil, fmt.Errorf("multiply needs a numeric amount")
			}
			f, err := parseFloat(v)
			if err != nil {
				def := arg(args, kw, 1, "default", sentinel)
				if def == sentinel {
					return nil, err
				}
				return def, nil
			}
			return f * amount, nil
		},
		"default": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			def := arg(args, kw, 0, "default_value", "")
			boolean := truthy(arg(args, kw, 1, "boolean", false))
			if _, u := v.(Undefined); u || (boolean && !truthy(v)) {
				return def, nil
			}
			return v, nil
		},
		"is_defined": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			if u, ok := v.(Undefined); ok {
				return nil, u
			}
			return v, nil
		},
		"iif": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			return iif(v, args, kw)
		},
		"abs":    func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return pyabs(v) },
		"string": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return pystr(v), nil },
		"lower": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			return strings.ToLower(pystr(v)), nil
		},
		"upper": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			return strings.ToUpper(pystr(v)), nil
		},
		"title": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			return pytitle(pystr(v)), nil
		},
		"capitalize": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			s := strings.ToLower(pystr(v))
			if s == "" {
				return s, nil
			}
			r := []rune(s)
			r[0] = unicode.ToUpper(r[0])
			return string(r), nil
		},
		"trim": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			chars := arg(args, kw, 0, "chars", nil)
			if chars == nil {
				return strings.TrimSpace(pystr(v)), nil
			}
			return strings.Trim(pystr(v), pystr(chars)), nil
		},
		"replace": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			old := arg(args, kw, 0, "old", nil)
			nw := arg(args, kw, 1, "new", nil)
			if old == nil || nw == nil {
				return nil, fmt.Errorf("replace needs old and new")
			}
			n := int64(-1)
			if c, ok := arg(args, kw, 2, "count", nil).(int64); ok {
				n = c
			}
			return strings.Replace(pystr(v), pystr(old), pystr(nw), int(n)), nil
		},
		"length": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			switch x := v.(type) {
			case string:
				return int64(len([]rune(x))), nil
			case []any:
				return int64(len(x)), nil
			case *Dict:
				return int64(x.Len()), nil
			}
			return nil, fmt.Errorf("object of type '%s' has no len()", typeName(v))
		},
		"join": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			items, err := toList(v)
			if err != nil {
				return nil, err
			}
			sep := pystr(arg(args, kw, 0, "d", ""))
			parts := make([]string, len(items))
			for i, it := range items {
				parts[i] = pystr(it)
			}
			return strings.Join(parts, sep), nil
		},
		"first": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			items, err := toList(v)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return Undefined{"first"}, nil
			}
			return items[0], nil
		},
		"last": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			items, err := toList(v)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return Undefined{"last"}, nil
			}
			return items[len(items)-1], nil
		},
		"list": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return toList(v) },
		"min":  func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return minmax(v, -1) },
		"max":  func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return minmax(v, 1) },
		"sum": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			items, err := toList(v)
			if err != nil {
				return nil, err
			}
			var acc any = arg(args, kw, 1, "start", int64(0))
			for _, it := range items {
				acc, err = arith("+", acc, it)
				if err != nil {
					return nil, err
				}
			}
			return acc, nil
		},
		"to_json": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) { return pyjson(v) },
		// Jinja2's built-in: sorted keys and HTML-safe escaping.
		"tojson": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			out, err := pyjson(sortedKeys(v))
			if err != nil {
				return nil, err
			}
			r := strings.NewReplacer("<", "\\u003c", ">", "\\u003e", "&", "\\u0026", "'", "\\u0027")
			return r.Replace(out), nil
		},
		"from_json": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("from_json needs a string")
			}
			return DecodeJSON([]byte(s))
		},
		"is_number": func(_ *evalCtx, v any, _ []any, _ map[string]any) (any, error) {
			f, err := parseFloat(v)
			return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f), nil
		},
		"bool": func(_ *evalCtx, v any, args []any, kw map[string]any) (any, error) {
			// HA's bool filter: on/off, true/false, yes/no, 1/0 strings.
			switch x := v.(type) {
			case bool:
				return x, nil
			case string:
				switch strings.ToLower(strings.TrimSpace(x)) {
				case "true", "yes", "on", "enable", "1":
					return true, nil
				case "false", "no", "off", "disable", "0":
					return false, nil
				}
			default:
				if f, ok := toFloat(v); ok {
					return f != 0, nil
				}
			}
			def := arg(args, kw, 0, "default", sentinel)
			if def == sentinel {
				return nil, fmt.Errorf("could not convert %s to bool", pyrepr(v))
			}
			return def, nil
		},
	}
}

// sortedKeys returns a copy with dict keys sorted, recursively.
func sortedKeys(v any) any {
	switch x := v.(type) {
	case *Dict:
		keys := append([]string{}, x.keys...)
		sort.Strings(keys)
		d := NewDict()
		for _, k := range keys {
			d.Set(k, sortedKeys(x.m[k]))
		}
		return d
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sortedKeys(e)
		}
		return out
	}
	return v
}

func pytitle(s string) string {
	r := []rune(s)
	prevLetter := false
	for i, c := range r {
		if unicode.IsLetter(c) {
			if prevLetter {
				r[i] = unicode.ToLower(c)
			} else {
				r[i] = unicode.ToUpper(c)
			}
			prevLetter = true
		} else {
			prevLetter = false
		}
	}
	return string(r)
}

func dictMethod(d *Dict, name string) method {
	switch name {
	case "items":
		return func(_ []any, _ map[string]any) (any, error) {
			out := make([]any, 0, d.Len())
			for _, k := range d.keys {
				out = append(out, []any{k, d.m[k]})
			}
			return out, nil
		}
	case "keys":
		return func(_ []any, _ map[string]any) (any, error) {
			out := make([]any, 0, d.Len())
			for _, k := range d.keys {
				out = append(out, k)
			}
			return out, nil
		}
	case "values":
		return func(_ []any, _ map[string]any) (any, error) {
			out := make([]any, 0, d.Len())
			for _, k := range d.keys {
				out = append(out, d.m[k])
			}
			return out, nil
		}
	case "get":
		return func(args []any, kw map[string]any) (any, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("get() needs a key")
			}
			if v, ok := d.Get(pystr(args[0])); ok {
				return v, nil
			}
			return arg(args, kw, 1, "default", nil), nil
		}
	}
	return nil
}

func stringMethod(s string, name string) method {
	switch name {
	case "upper":
		return func(_ []any, _ map[string]any) (any, error) { return strings.ToUpper(s), nil }
	case "lower":
		return func(_ []any, _ map[string]any) (any, error) { return strings.ToLower(s), nil }
	case "strip":
		return func(_ []any, _ map[string]any) (any, error) { return strings.TrimSpace(s), nil }
	case "title":
		return func(_ []any, _ map[string]any) (any, error) { return pytitle(s), nil }
	case "split":
		return func(args []any, kw map[string]any) (any, error) {
			sep := arg(args, kw, 0, "sep", nil)
			var parts []string
			if sep == nil {
				parts = strings.Fields(s)
			} else {
				parts = strings.Split(s, pystr(sep))
			}
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = p
			}
			return out, nil
		}
	case "startswith":
		return func(args []any, _ map[string]any) (any, error) {
			return len(args) == 1 && strings.HasPrefix(s, pystr(args[0])), nil
		}
	case "endswith":
		return func(args []any, _ map[string]any) (any, error) {
			return len(args) == 1 && strings.HasSuffix(s, pystr(args[0])), nil
		}
	case "replace":
		return func(args []any, _ map[string]any) (any, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("replace() needs two arguments")
			}
			return strings.ReplaceAll(s, pystr(args[0]), pystr(args[1])), nil
		}
	}
	return nil
}
