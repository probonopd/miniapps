/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Host types plug into the engine through these interfaces, so that the
// automation engine can expose "states", datetimes and functions without
// the template package knowing about the core.

// Object exposes attributes (and items) to templates.
type Object interface {
	TemplateAttr(name string) (any, bool)
}

// Callable can be called from templates.
type Callable interface {
	TemplateCall(args []any, kw map[string]any) (any, error)
}

// Iterable can be looped over and filtered.
type Iterable interface {
	TemplateItems() []any
}

// Comparable orders host values (datetimes).
type Comparable interface {
	CompareTo(other any) (int, bool)
}

// Arith lets host values take part in + and -; reversed means the host
// value was the right operand.
type Arith interface {
	BinaryOp(op string, other any, reversed bool) (any, bool, error)
}

// Func is a host function usable as a global.
type Func func(args []any, kw map[string]any) (any, error)

// DateTime mirrors the Python datetime the HA templates work with.
type DateTime struct{ T time.Time }

func (d DateTime) String() string {
	// Python str(datetime): "2026-11-05 13:00:00.123456+01:00", no
	// fraction when it is zero.
	s := d.T.Format("2006-01-02 15:04:05")
	if d.T.Nanosecond() != 0 {
		s += fmt.Sprintf(".%06d", d.T.Nanosecond()/1000)
	}
	return s + d.T.Format("-07:00")
}

func (d DateTime) isoformat() string {
	s := d.T.Format("2006-01-02T15:04:05")
	if d.T.Nanosecond() != 0 {
		s += fmt.Sprintf(".%06d", d.T.Nanosecond()/1000)
	}
	return s + d.T.Format("-07:00")
}

func (d DateTime) TemplateAttr(name string) (any, bool) {
	switch name {
	case "year":
		return int64(d.T.Year()), true
	case "month":
		return int64(d.T.Month()), true
	case "day":
		return int64(d.T.Day()), true
	case "hour":
		return int64(d.T.Hour()), true
	case "minute":
		return int64(d.T.Minute()), true
	case "second":
		return int64(d.T.Second()), true
	case "microsecond":
		return int64(d.T.Nanosecond() / 1000), true
	case "weekday":
		return method(func(_ []any, _ map[string]any) (any, error) { return int64((int(d.T.Weekday()) + 6) % 7), nil }), true
	case "isoweekday":
		return method(func(_ []any, _ map[string]any) (any, error) { return int64((int(d.T.Weekday())+6)%7 + 1), nil }), true
	case "timestamp":
		return method(func(_ []any, _ map[string]any) (any, error) { return float64(d.T.UnixNano()) / 1e9, nil }), true
	case "isoformat":
		return method(func(_ []any, _ map[string]any) (any, error) { return d.isoformat(), nil }), true
	case "strftime":
		return method(func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("strftime() takes one argument")
			}
			return Strftime(d.T, pystr(args[0])), nil
		}), true
	case "date":
		return method(func(_ []any, _ map[string]any) (any, error) {
			y, m, dd := d.T.Date()
			return DateTime{time.Date(y, m, dd, 0, 0, 0, 0, d.T.Location())}, nil
		}), true
	case "replace":
		return method(func(_ []any, kw map[string]any) (any, error) {
			t := d.T
			get := func(k string, def int) int {
				if v, ok := kw[k].(int64); ok {
					return int(v)
				}
				return def
			}
			return DateTime{time.Date(get("year", t.Year()), time.Month(get("month", int(t.Month()))), get("day", t.Day()), get("hour", t.Hour()), get("minute", t.Minute()), get("second", t.Second()), get("microsecond", t.Nanosecond()/1000)*1000, t.Location())}, nil
		}), true
	case "astimezone":
		return method(func(_ []any, _ map[string]any) (any, error) { return DateTime{d.T.Local()}, nil }), true
	}
	return nil, false
}

func (d DateTime) CompareTo(other any) (int, bool) {
	o, ok := other.(DateTime)
	if !ok {
		return 0, false
	}
	switch {
	case d.T.Before(o.T):
		return -1, true
	case d.T.After(o.T):
		return 1, true
	}
	return 0, true
}

func (d DateTime) BinaryOp(op string, other any, reversed bool) (any, bool, error) {
	switch o := other.(type) {
	case DateTime:
		if op == "-" && !reversed {
			return TimeDelta{d.T.Sub(o.T)}, true, nil
		}
	case TimeDelta:
		switch op {
		case "+":
			return DateTime{d.T.Add(o.D)}, true, nil
		case "-":
			if !reversed {
				return DateTime{d.T.Add(-o.D)}, true, nil
			}
		}
	}
	return nil, false, nil
}

// TimeDelta mirrors Python's timedelta.
type TimeDelta struct{ D time.Duration }

func (t TimeDelta) String() string {
	d := t.D
	neg := d < 0
	if neg {
		d = -d
	}
	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	s := fmt.Sprintf("%d:%02d:%02d", int64(d/time.Hour), int64(d/time.Minute)%60, int64(d/time.Second)%60)
	if us := (d % time.Second) / time.Microsecond; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	if days != 0 {
		unit := "days"
		if days == 1 {
			unit = "day"
		}
		s = fmt.Sprintf("%d %s, %s", days, unit, s)
	}
	if neg {
		s = "-" + s
	}
	return s
}

func (t TimeDelta) TemplateAttr(name string) (any, bool) {
	switch name {
	case "total_seconds":
		return method(func(_ []any, _ map[string]any) (any, error) { return t.D.Seconds(), nil }), true
	case "days":
		return int64(math.Floor(t.D.Hours() / 24)), true
	case "seconds":
		rem := t.D - time.Duration(math.Floor(t.D.Hours()/24))*24*time.Hour
		return int64(rem / time.Second), true
	}
	return nil, false
}

func (t TimeDelta) CompareTo(other any) (int, bool) {
	o, ok := other.(TimeDelta)
	if !ok {
		return 0, false
	}
	switch {
	case t.D < o.D:
		return -1, true
	case t.D > o.D:
		return 1, true
	}
	return 0, true
}

func (t TimeDelta) BinaryOp(op string, other any, reversed bool) (any, bool, error) {
	switch o := other.(type) {
	case TimeDelta:
		switch op {
		case "+":
			return TimeDelta{t.D + o.D}, true, nil
		case "-":
			if reversed {
				return TimeDelta{o.D - t.D}, true, nil
			}
			return TimeDelta{t.D - o.D}, true, nil
		}
	case DateTime:
		if op == "+" {
			return DateTime{o.T.Add(t.D)}, true, nil
		}
	default:
		if f, ok := toFloat(other); ok && op == "*" {
			return TimeDelta{time.Duration(float64(t.D) * f)}, true, nil
		}
	}
	return nil, false, nil
}

// Strftime converts the Python directives HA templates use.
func Strftime(t time.Time, format string) string {
	var sb strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			sb.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			sb.WriteString(t.Format("2006"))
		case 'y':
			sb.WriteString(t.Format("06"))
		case 'm':
			sb.WriteString(t.Format("01"))
		case 'd':
			sb.WriteString(t.Format("02"))
		case 'H':
			sb.WriteString(t.Format("15"))
		case 'I':
			sb.WriteString(t.Format("03"))
		case 'M':
			sb.WriteString(t.Format("04"))
		case 'S':
			sb.WriteString(t.Format("05"))
		case 'f':
			sb.WriteString(fmt.Sprintf("%06d", t.Nanosecond()/1000))
		case 'p':
			sb.WriteString(t.Format("PM"))
		case 'a':
			sb.WriteString(t.Format("Mon"))
		case 'A':
			sb.WriteString(t.Format("Monday"))
		case 'b':
			sb.WriteString(t.Format("Jan"))
		case 'B':
			sb.WriteString(t.Format("January"))
		case 'j':
			sb.WriteString(fmt.Sprintf("%03d", t.YearDay()))
		case 'z':
			sb.WriteString(t.Format("-0700"))
		case 'Z':
			sb.WriteString(t.Format("MST"))
		case 'w':
			sb.WriteString(fmt.Sprintf("%d", int(t.Weekday())))
		case 'e':
			sb.WriteString(fmt.Sprintf("%2d", t.Day()))
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(format[i])
		}
	}
	return sb.String()
}

// ParseDateTime accepts the ISO forms HA produces and consumes.
func ParseDateTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999-07:00", "2006-01-02 15:04:05.999999-07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
