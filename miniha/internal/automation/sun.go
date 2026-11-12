/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"time"

	"github.com/probonopd/miniha/internal/astro"
)

// Thin wrappers so the engine keeps its names; the math lives in astro.
func sunTimes(t time.Time, lat, lon float64) (rise, set time.Time, ok bool) {
	return astro.SunTimes(t, lat, lon)
}

func nextSunEvent(t time.Time, lat, lon float64, event string, offset time.Duration) (time.Time, bool) {
	return astro.NextSunEvent(t, lat, lon, event, offset)
}
