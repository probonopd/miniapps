/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"fmt"
	"time"
)

const (
	timerIdle   = "idle"
	timerActive = "active"
	timerPaused = "paused"
)

type timerDef struct {
	name, icon string
	duration   time.Duration
	restore    bool
}

type timer struct {
	base
	d timerDef

	state string
	// running is the duration attribute: the configured one unless
	// start was called with another.
	running   time.Duration
	remaining time.Duration // while paused
	end       time.Time     // while active
	// gen invalidates a pending deadline callback when the timer is
	// restarted, paused or cancelled after the callback was armed.
	gen uint64
	tm  *time.Timer
}

func parseTimer(h *Helpers, id string, raw any) (helper, error) {
	o, err := newOpts(domTimer, id, raw)
	if err != nil {
		return nil, err
	}
	var d timerDef
	if d.name, d.icon, err = o.nameIcon(); err != nil {
		return nil, err
	}
	if v, ok := o.get("duration"); ok {
		if d.duration, err = parseDuration(v); err != nil {
			return nil, o.errf("duration", "%v", err)
		}
	}
	restore, err := o.boolean("restore")
	if err != nil {
		return nil, err
	}
	if err := o.finish(); err != nil {
		return nil, err
	}
	if d.duration < 0 {
		return nil, o.errf("duration", "must not be negative")
	}
	d.restore = restore != nil && *restore
	return &timer{base: newBase(h, domTimer, id, d.name, d.icon), d: d}, nil
}

func (t *timer) def() any { return t.d }

func (t *timer) view() (string, map[string]any) {
	attrs := map[string]any{
		"duration": formatDuration(t.running),
		"editable": true,
		"restore":  t.d.restore,
	}
	switch t.state {
	case timerActive:
		attrs["remaining"] = formatDuration(t.end.Sub(t.h.c.Now()))
		attrs["finishes_at"] = formatTime(t.end)
	case timerPaused:
		attrs["remaining"] = formatDuration(t.remaining)
	}
	return t.state, attrs
}

// record keeps milliseconds because the display attributes are truncated
// to whole seconds and a short timer would lose its remaining time.
func (t *timer) record() record {
	r := record{State: t.state, Attributes: map[string]any{"duration_ms": float64(t.running.Milliseconds())}}
	switch t.state {
	case timerActive:
		r.Attributes["finishes_at"] = formatTime(t.end)
	case timerPaused:
		r.Attributes["remaining_ms"] = float64(t.remaining.Milliseconds())
	}
	return r
}

// restore brings back a running or paused timer only for restore: true
// (HA's default is false) or when a reload replaces the entity.
func (t *timer) restore(r *record, reload bool) {
	t.state = timerIdle
	t.running = t.d.duration
	if r == nil || !(reload || t.d.restore) {
		return
	}
	ms, ok := r.Attributes["duration_ms"].(float64)
	if !ok || ms < 0 {
		return
	}
	switch r.State {
	case timerActive:
		s, _ := r.Attributes["finishes_at"].(string)
		end, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return
		}
		t.state, t.end = timerActive, end
	case timerPaused:
		rem, ok := r.Attributes["remaining_ms"].(float64)
		if !ok || rem < 0 {
			return
		}
		t.state, t.remaining = timerPaused, time.Duration(rem)*time.Millisecond
	default:
		return
	}
	t.running = time.Duration(ms) * time.Millisecond
}

// resume arms the deadline of a restored timer, or finishes it right away
// when the deadline passed while the hub was down.
func (t *timer) resume() {
	_ = t.h.update(context.Background(), t, func() ([]event, error) {
		if t.state != timerActive {
			return nil, errSkip
		}
		if !t.end.After(t.h.c.Now()) {
			return t.finishLocked(), nil
		}
		t.schedule()
		return nil, errSkip
	})
}

func (t *timer) stop() { t.unschedule() }

func (t *timer) schedule() {
	t.unschedule()
	gen := t.gen
	delay := max(0, t.end.Sub(t.h.c.Now()))
	t.tm = time.AfterFunc(delay, func() { t.expired(gen) })
}

func (t *timer) unschedule() {
	t.gen++
	if t.tm != nil {
		t.tm.Stop()
		t.tm = nil
	}
}

func (t *timer) expired(gen uint64) {
	_ = t.h.update(context.Background(), t, func() ([]event, error) {
		if t.gen != gen || t.state != timerActive {
			return nil, errSkip
		}
		return t.finishLocked(), nil
	})
}

func (t *timer) ev(typ string, withEnd bool, end time.Time) event {
	data := map[string]any{"entity_id": t.entityID}
	if withEnd {
		data["finishes_at"] = formatTime(end)
	}
	return event{typ, data}
}

// finishLocked ends the timer at its deadline (locked).
func (t *timer) finishLocked() []event {
	end := t.end
	if t.state != timerActive {
		end = t.h.c.Now()
	}
	t.reset()
	e := t.ev("timer.finished", true, end)
	e.data["finished_at"] = formatTime(end)
	return []event{e}
}

func (t *timer) reset() {
	t.unschedule()
	t.state = timerIdle
	t.running = t.d.duration
	t.remaining = 0
	t.end = time.Time{}
}

// Start begins the timer, or resumes a paused one when no duration is
// given. A start while active restarts with the full duration.
func (t *timer) Start(ctx context.Context, dur *time.Duration) error {
	if dur != nil && *dur < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	return t.h.update(ctx, t, func() ([]event, error) {
		typ := "timer.started"
		if t.state == timerPaused {
			typ = "timer.restarted"
		}
		span := t.running
		switch {
		case dur != nil:
			t.running, span = *dur, *dur
		case t.state == timerPaused:
			span = t.remaining
		case t.state == timerIdle:
			t.running, span = t.d.duration, t.d.duration
		}
		t.state = timerActive
		t.remaining = 0
		t.end = t.h.c.Now().Add(span)
		t.schedule()
		return []event{t.ev(typ, true, t.end)}, nil
	})
}

// Pause freezes an active timer; other states ignore it, as in HA.
func (t *timer) Pause(ctx context.Context) error {
	return t.h.update(ctx, t, func() ([]event, error) {
		if t.state != timerActive {
			return nil, errSkip
		}
		t.remaining = max(0, t.end.Sub(t.h.c.Now()))
		t.unschedule()
		t.state = timerPaused
		t.end = time.Time{}
		return []event{t.ev("timer.paused", false, time.Time{})}, nil
	})
}

func (t *timer) Cancel(ctx context.Context) error {
	return t.h.update(ctx, t, func() ([]event, error) {
		if t.state == timerIdle {
			return nil, errSkip
		}
		t.reset()
		return []event{t.ev("timer.cancelled", false, time.Time{})}, nil
	})
}

// Finish ends a running or paused timer ahead of schedule.
func (t *timer) Finish(ctx context.Context) error {
	return t.h.update(ctx, t, func() ([]event, error) {
		if t.state == timerIdle {
			return nil, errSkip
		}
		return t.finishLocked(), nil
	})
}

// Change shifts the deadline of an active timer by delta, which may be
// negative; a deadline moved into the past finishes the timer.
func (t *timer) Change(ctx context.Context, delta time.Duration) error {
	return t.h.update(ctx, t, func() ([]event, error) {
		if t.state != timerActive {
			return nil, errSkip
		}
		end := t.end.Add(delta)
		if !end.After(t.h.c.Now()) {
			return t.finishLocked(), nil
		}
		t.end = end
		t.schedule()
		return []event{t.ev("timer.changed", true, t.end)}, nil
	})
}

func (h *Helpers) registerTimerServices() {
	svc(h, domTimer, "start", "Start", map[string]any{"duration": fld("Duration the timer runs; the configured duration when omitted", false)}, func(ctx context.Context, e *timer, data map[string]any) error {
		var dur *time.Duration
		if v, ok := data["duration"]; ok && v != nil {
			d, err := parseDuration(v)
			if err != nil {
				return err
			}
			dur = &d
		}
		return e.Start(ctx, dur)
	})
	svc(h, domTimer, "pause", "Pause", nil, func(ctx context.Context, e *timer, _ map[string]any) error { return e.Pause(ctx) })
	svc(h, domTimer, "cancel", "Cancel", nil, func(ctx context.Context, e *timer, _ map[string]any) error { return e.Cancel(ctx) })
	svc(h, domTimer, "finish", "Finish", nil, func(ctx context.Context, e *timer, _ map[string]any) error { return e.Finish(ctx) })
	svc(h, domTimer, "change", "Change", map[string]any{"duration": fld("Duration to add to or, when negative, subtract from the running timer", true)}, func(ctx context.Context, e *timer, data map[string]any) error {
		v, ok := data["duration"]
		if !ok {
			return fmt.Errorf("duration is required")
		}
		d, err := parseDuration(v)
		if err != nil {
			return err
		}
		return e.Change(ctx, d)
	})
}
