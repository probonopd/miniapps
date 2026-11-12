/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	dateLayout     = "2006-01-02"
	clockLayout    = "15:04:05"
	datetimeLayout = "2006-01-02 15:04:05"
)

type inputDatetimeDef struct {
	name, icon       string
	hasDate, hasTime bool
	initial          string
	hasInitial       bool
}

type inputDatetime struct {
	base
	d   inputDatetimeDef
	loc *time.Location
	// cur holds the wall clock in loc. A date-only value sits at midnight,
	// a time-only value on 2000-01-01.
	cur time.Time
}

func parseInputDatetime(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domInputDatetime, id, raw)
	if err != nil {
		return nil, err
	}
	var d inputDatetimeDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	hasDate, err := o.boolean("has_date")
	if err != nil {
		return nil, err
	}
	hasTime, err := o.boolean("has_time")
	if err != nil {
		return nil, err
	}
	d.hasDate, d.hasTime = true, true
	if hasDate != nil {
		d.hasDate = *hasDate
	}
	if hasTime != nil {
		d.hasTime = *hasTime
	}
	if v, ok := o.get("initial"); ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, o.errf("initial", "must be a string, got %T", v)
		}
		d.initial, d.hasInitial = s, true
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	if !d.hasDate && !d.hasTime {
		return nil, o.errf("has_date", "at least one of has_date or has_time must be true")
	}
	e := &inputDatetime{base: newBase(h, domInputDatetime, id, d.name, d.icon), d: d, loc: h.loc}
	if d.hasInitial {
		if _, err := e.parseState(d.initial); err != nil {
			return nil, o.errf("initial", "%v", err)
		}
	}
	return e, nil
}

func (e *inputDatetime) def() any { return e.d }

func (e *inputDatetime) format(t time.Time) string {
	switch {
	case e.d.hasDate && e.d.hasTime:
		return t.Format(datetimeLayout)
	case e.d.hasDate:
		return t.Format(dateLayout)
	}
	return t.Format(clockLayout)
}

// parseState reads a value in the entity's own format; for the combined
// form HA also accepts an ISO "T" separator, an omitted seconds field and
// an explicit offset.
func (e *inputDatetime) parseState(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch {
	case e.d.hasDate && e.d.hasTime:
		return parseDatetime(s, e.loc)
	case e.d.hasDate:
		return time.ParseInLocation(dateLayout, s, e.loc)
	}
	return parseClock(s, e.loc)
}

func parseDatetime(s string, loc *time.Location) (time.Time, error) {
	for _, l := range []string{datetimeLayout, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	for _, l := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.In(loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date and time like 2026-01-31 08:30:00", s)
}

func parseClock(s string, loc *time.Location) (time.Time, error) {
	for _, l := range []string{clockLayout, "15:04"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return time.Date(2000, 1, 1, t.Hour(), t.Minute(), t.Second(), 0, loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time like 08:30:00", s)
}

func (e *inputDatetime) view() (string, map[string]any) {
	attrs := map[string]any{
		"has_date": e.d.hasDate,
		"has_time": e.d.hasTime,
		"editable": true,
	}
	if e.d.hasDate {
		attrs["year"] = e.cur.Year()
		attrs["month"] = int(e.cur.Month())
		attrs["day"] = e.cur.Day()
	}
	if e.d.hasTime {
		attrs["hour"] = e.cur.Hour()
		attrs["minute"] = e.cur.Minute()
		attrs["second"] = e.cur.Second()
	}
	if e.d.hasDate {
		attrs["timestamp"] = float64(e.cur.Unix())
	} else {
		attrs["timestamp"] = float64(e.cur.Hour()*3600 + e.cur.Minute()*60 + e.cur.Second())
	}
	return e.format(e.cur), attrs
}

func (e *inputDatetime) record() record {
	st, _ := e.view()
	return record{State: st}
}

func (e *inputDatetime) restore(r *record, reload bool) {
	// HA's default is the epoch date and midnight.
	e.cur = time.Date(1970, 1, 1, 0, 0, 0, 0, e.loc)
	if !e.d.hasDate {
		e.cur = time.Date(2000, 1, 1, 0, 0, 0, 0, e.loc)
	}
	if e.d.hasInitial {
		e.cur, _ = e.parseState(e.d.initial)
		e.cur = e.normalize(e.cur)
		if !reload {
			return
		}
	}
	if r != nil {
		if t, err := e.parseState(r.State); err == nil {
			e.cur = e.normalize(t)
		}
	}
}

// normalize drops the parts the entity does not have.
func (e *inputDatetime) normalize(t time.Time) time.Time {
	switch {
	case e.d.hasDate && e.d.hasTime:
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, e.loc)
	case e.d.hasDate:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, e.loc)
	}
	return time.Date(2000, 1, 1, t.Hour(), t.Minute(), t.Second(), 0, e.loc)
}

func (e *inputDatetime) resume() {}
func (e *inputDatetime) stop()   {}

// setDatetime applies HA's set_datetime rules: date and time need a
// matching has_date and has_time, and a missing half of a combined value
// is taken from the current one.
func (e *inputDatetime) setDatetime(ctx context.Context, data map[string]any) error {
	date, hasDate := data["date"]
	clock, hasClock := data["time"]
	dt, hasDT := data["datetime"]
	ts, hasTS := data["timestamp"]
	groups := 0
	for _, g := range []bool{hasDate || hasClock, hasDT, hasTS} {
		if g {
			groups++
		}
	}
	if groups != 1 {
		return fmt.Errorf("set_datetime needs exactly one of date and/or time, datetime or timestamp")
	}
	var (
		newDate, newClock time.Time
		haveDate, haveClk bool
		err               error
	)
	switch {
	case hasDate || hasClock:
		if hasDate {
			if !e.d.hasDate {
				return fmt.Errorf("cannot set a date, the entity has no date")
			}
			s, ok := date.(string)
			if !ok {
				return fmt.Errorf("date must be a string like 2026-01-31")
			}
			if newDate, err = time.ParseInLocation(dateLayout, strings.TrimSpace(s), e.loc); err != nil {
				return fmt.Errorf("date %q is not like 2026-01-31", s)
			}
			haveDate = true
		}
		if hasClock {
			if !e.d.hasTime {
				return fmt.Errorf("cannot set a time, the entity has no time")
			}
			s, ok := clock.(string)
			if !ok {
				return fmt.Errorf("time must be a string like 08:30:00")
			}
			if newClock, err = parseClock(strings.TrimSpace(s), e.loc); err != nil {
				return err
			}
			haveClk = true
		}
	case hasDT:
		s, ok := dt.(string)
		if !ok {
			return fmt.Errorf("datetime must be a string like 2026-01-31 08:30:00")
		}
		t, err := parseDatetime(strings.TrimSpace(s), e.loc)
		if err != nil {
			return err
		}
		newDate, newClock, haveDate, haveClk = t, t, e.d.hasDate, e.d.hasTime
	default:
		f, err := toFloat(ts)
		if err != nil {
			return fmt.Errorf("timestamp %w", err)
		}
		sec := int64(f)
		t := time.Unix(sec, int64((f-float64(sec))*1e9)).In(e.loc)
		newDate, newClock, haveDate, haveClk = t, t, e.d.hasDate, e.d.hasTime
	}
	return e.h.update(ctx, e, func() ([]event, error) {
		cur := e.cur
		y, mo, d := cur.Date()
		if haveDate {
			y, mo, d = newDate.Date()
		}
		hh, mm, ss := cur.Clock()
		if haveClk {
			hh, mm, ss = newClock.Clock()
		}
		e.cur = e.normalize(time.Date(y, mo, d, hh, mm, ss, 0, e.loc))
		return nil, nil
	})
}

func (h *Helpers) registerInputDatetimeServices() {
	fields := map[string]any{
		"date":      fld("The target date", false),
		"time":      fld("The target time", false),
		"datetime":  fld("The target date and time", false),
		"timestamp": fld("The target date and time as seconds since the epoch", false),
	}
	svc(h, domInputDatetime, "set_datetime", "Set", fields, func(ctx context.Context, e *inputDatetime, data map[string]any) error {
		return e.setDatetime(ctx, data)
	})
}
