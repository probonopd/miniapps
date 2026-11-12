/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package location

import (
	"math"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/astro"
	"github.com/probonopd/miniha/internal/core"
)

const (
	// civilElevation is the sun elevation of civil dawn and dusk.
	civilElevation = -6.0
	// horizonElevation is HA's threshold between above_horizon and
	// below_horizon.
	horizonElevation = -0.833

	sunUpdateInterval = time.Minute
	timeLayout        = "2006-01-02T15:04:05.000000-07:00"
)

// Sun owns the sun.sun entity.
type Sun struct {
	c        *core.Core
	lat, lon float64
	loc      *time.Location

	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewSun creates sun.sun and refreshes it every minute until Stop. loc
// defines the civil day that astro uses for sunrise and sunset.
func NewSun(c *core.Core, lat, lon float64, loc *time.Location) *Sun {
	s := &Sun{c: c, lat: lat, lon: lon, loc: loc, done: make(chan struct{})}
	s.Update()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(sunUpdateInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.Update()
			case <-s.done:
				return
			}
		}
	}()
	return s
}

// Stop ends the periodic refresh and waits for it to finish.
func (s *Sun) Stop() {
	s.stopOnce.Do(func() { close(s.done) })
	s.wg.Wait()
}

// Update recomputes the entity for the core's current time.
func (s *Sun) Update() {
	now := s.c.Now().In(s.loc)
	elev, az := astro.Position(now, s.lat, s.lon)
	state := "below_horizon"
	if elev > horizonElevation {
		state = "above_horizon"
	}
	rising, _ := astro.NextSunEvent(now, s.lat, s.lon, "sunrise", 0)
	setting, _ := astro.NextSunEvent(now, s.lat, s.lon, "sunset", 0)
	dawn, _ := s.nextCrossing(now, civilElevation, true)
	dusk, _ := s.nextCrossing(now, civilElevation, false)
	noon := s.nextExtremum(now, true)
	midnight := s.nextExtremum(now, false)
	attrs := map[string]any{
		"next_dawn":     formatTime(dawn),
		"next_dusk":     formatTime(dusk),
		"next_midnight": formatTime(midnight),
		"next_noon":     formatTime(noon),
		"next_rising":   formatTime(rising),
		"next_setting":  formatTime(setting),
		"elevation":     round2(elev),
		"azimuth":       round2(az),
		// The sun is rising while the next solar noon comes before the
		// next solar midnight, i.e. between midnight and noon.
		"rising":        noon.Before(midnight),
		"friendly_name": "Sun",
	}
	if err := s.c.SetState("sun.sun", state, attrs, core.Context{}); err != nil {
		panic("location: writing sun.sun: " + err.Error())
	}
}

// formatTime renders UTC with an explicit offset like HA; the zero time
// (no such event, e.g. polar day) becomes nil in the attribute.
func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// nextCrossing finds the next time after t that the sun's elevation passes
// the given value, rising or falling. astro exports no custom-zenith
// calculation, so this scans coarsely and bisects, which is accurate to
// about the same minute as the rest of astro. It looks 36 hours ahead,
// enough for any event of a day in the next one; polar nights without a
// crossing report false.
func (s *Sun) nextCrossing(t time.Time, elevation float64, rising bool) (time.Time, bool) {
	const step = 5 * time.Minute
	prev := t
	prevElev, _ := astro.Position(prev, s.lat, s.lon)
	for end := t.Add(36 * time.Hour); prev.Before(end); {
		next := prev.Add(step)
		nextElev, _ := astro.Position(next, s.lat, s.lon)
		crossed := rising && prevElev < elevation && nextElev >= elevation ||
			!rising && prevElev >= elevation && nextElev < elevation
		if crossed {
			lo, hi := prev, next
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				e, _ := astro.Position(mid, s.lat, s.lon)
				if (e >= elevation) == rising {
					hi = mid
				} else {
					lo = mid
				}
			}
			return hi, true
		}
		prev, prevElev = next, nextElev
	}
	return time.Time{}, false
}

// nextExtremum finds the next solar noon (highest sun) or solar midnight
// (lowest sun) after t. Noon is within about 17 minutes of 12:00 local
// mean time, so a ternary search over a few hours around each candidate
// day's estimate suffices; the elevation is unimodal there.
func (s *Sun) nextExtremum(t time.Time, noon bool) time.Time {
	for day := -1; day <= 2; day++ {
		base := time.Date(t.UTC().Year(), t.UTC().Month(), t.UTC().Day()+day, 0, 0, 0, 0, time.UTC)
		est := base.Add(12*time.Hour - time.Duration(s.lon*4*float64(time.Minute)))
		if !noon {
			est = est.Add(12 * time.Hour)
		}
		lo, hi := est.Add(-3*time.Hour), est.Add(3*time.Hour)
		for hi.Sub(lo) > time.Second {
			a := lo.Add(hi.Sub(lo) / 3)
			b := hi.Add(-hi.Sub(lo) / 3)
			ea, _ := astro.Position(a, s.lat, s.lon)
			eb, _ := astro.Position(b, s.lat, s.lon)
			if (ea < eb) == noon {
				lo = a
			} else {
				hi = b
			}
		}
		at := lo.Add(hi.Sub(lo) / 2)
		if at.After(t) {
			return at
		}
	}
	return time.Time{}
}
