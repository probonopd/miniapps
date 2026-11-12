/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Values are: nil, bool, int64, *big.Int, float64, string, []any, *Dict,
// Undefined, and method (a bound callable).

// Undefined mirrors Jinja2's Undefined: renders as "", is falsy, and errors
// when used in arithmetic or as an object.
type Undefined struct{ hint string }

func (u Undefined) Error() string { return "'" + u.hint + "' is undefined" }

type method func(args []any, kwargs map[string]any) (any, error)

// Dict is an insertion-ordered map so that dict output matches Python.
type Dict struct {
	keys []string
	m    map[string]any
}

func NewDict() *Dict { return &Dict{m: map[string]any{}} }

func (d *Dict) Set(k string, v any) {
	if _, ok := d.m[k]; !ok {
		d.keys = append(d.keys, k)
	}
	d.m[k] = v
}

func (d *Dict) Get(k string) (any, bool) {
	v, ok := d.m[k]
	return v, ok
}

func (d *Dict) Keys() []string { return d.keys }
func (d *Dict) Len() int       { return len(d.keys) }

// ToGo converts a Dict tree to plain Go maps for JSON encoding elsewhere.
func ToGo(v any) any {
	switch x := v.(type) {
	case *Dict:
		m := make(map[string]any, len(x.keys))
		for _, k := range x.keys {
			m[k] = ToGo(x.m[k])
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ToGo(e)
		}
		return out
	case *big.Int:
		return x.String()
	}
	return v
}

// FromGo converts plain Go values (as produced by encoding/json or yaml)
// into template values. Map key order is sorted for determinism.
func FromGo(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := NewDict()
		for _, k := range keys {
			d.Set(k, FromGo(x[k]))
		}
		return d
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = FromGo(e)
		}
		return out
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	case json.Number:
		return numberFromJSON(x)
	}
	return v
}

// DecodeJSON parses JSON preserving object key order and the int/float
// distinction, as Python's json.loads does.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			d := NewDict()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				d.Set(k, v)
			}
			_, err := dec.Token()
			return d, err
		case '[':
			l := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			_, err := dec.Token()
			return l, err
		}
		return nil, fmt.Errorf("unexpected delimiter %v", x)
	case json.Number:
		return numberFromJSON(x), nil
	}
	return t, nil
}

func numberFromJSON(n json.Number) any {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil, Undefined:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *Dict:
		return x.Len() > 0
	case Iterable:
		return len(x.TemplateItems()) > 0
	}
	return true
}

func isNumber(v any) bool {
	switch v.(type) {
	case int64, float64, *big.Int, bool:
		return true
	}
	return false
}

func isInt(v any) bool {
	switch v.(type) {
	case int64, *big.Int, bool:
		return true
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func toBig(v any) *big.Int {
	switch x := v.(type) {
	case int64:
		return big.NewInt(x)
	case *big.Int:
		return x
	case bool:
		if x {
			return big.NewInt(1)
		}
		return big.NewInt(0)
	}
	return nil
}

// normInt collapses a big.Int back to int64 when it fits, so that the
// common case stays cheap.
func normInt(b *big.Int) any {
	if b.IsInt64() {
		return b.Int64()
	}
	return b
}

// pystr renders a value the way Python's str() does.
func pystr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case Undefined:
		return ""
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case *big.Int:
		return x.String()
	case float64:
		return pyfloat(x)
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	}
	return pyrepr(v)
}

// pyfloat renders like Python's float repr: shortest round-trip digits,
// a trailing ".0" for integral values, exponent form outside [1e-4, 1e16).
func pyfloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	a := math.Abs(f)
	if a != 0 && (a >= 1e16 || a < 1e-4) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func pyrepr(v any) string {
	switch x := v.(type) {
	case string:
		if strings.Contains(x, "'") && !strings.Contains(x, "\"") {
			return "\"" + x + "\""
		}
		return "'" + strings.ReplaceAll(x, "'", "\\'") + "'"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyrepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Dict:
		parts := make([]string, 0, x.Len())
		for _, k := range x.keys {
			parts = append(parts, pyrepr(k)+": "+pyrepr(x.m[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return pystr(v)
}

// pyjson renders like Python's json.dumps with default separators.
func pyjson(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case Undefined:
		return "", fmt.Errorf("%s is undefined", x.hint)
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int64, *big.Int, float64:
		return pystr(v), nil
	case string:
		b, _ := json.Marshal(x)
		return string(b), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := pyjson(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case *Dict:
		parts := make([]string, 0, x.Len())
		for _, k := range x.keys {
			s, err := pyjson(x.m[k])
			if err != nil {
				return "", err
			}
			kb, _ := json.Marshal(k)
			parts = append(parts, string(kb)+": "+s)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	}
	return "", fmt.Errorf("cannot serialize %T to JSON", v)
}

func typeName(v any) string {
	switch v.(type) {
	case DateTime:
		return "datetime"
	case TimeDelta:
		return "timedelta"
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case *Dict:
		return "dict"
	case Undefined:
		return "Undefined"
	}
	return fmt.Sprintf("%T", v)
}
