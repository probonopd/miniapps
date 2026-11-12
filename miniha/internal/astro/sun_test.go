/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package astro

import (
	"math"
	"testing"
	"time"
)

func TestBerlinSolstice(t *testing.T) {
	loc := time.FixedZone("CEST", 7200)
	day := time.Date(2026, 6, 21, 12, 0, 0, 0, loc)
	rise, set, ok := SunTimes(day, 52.52, 13.405)
	if !ok {
		t.Fatal("no sun")
	}
	for _, c := range []struct {
		got, want time.Time
	}{{rise, time.Date(2026, 6, 21, 4, 43, 0, 0, loc)}, {set, time.Date(2026, 6, 21, 21, 33, 0, 0, loc)}} {
		if d := c.got.Sub(c.want); d < -3*time.Minute || d > 3*time.Minute {
			t.Errorf("got %v want about %v", c.got, c.want)
		}
	}
	// Solar noon in Berlin on the solstice: elevation about 61 degrees, azimuth near 180.
	el, az := Position(time.Date(2026, 6, 21, 13, 8, 0, 0, loc), 52.52, 13.405)
	if math.Abs(el-61) > 1 || math.Abs(az-180) > 3 {
		t.Errorf("noon position %.1f %.1f", el, az)
	}
	el, _ = Position(time.Date(2026, 6, 21, 1, 0, 0, 0, loc), 52.52, 13.405)
	if el > -5 {
		t.Errorf("night elevation %.1f", el)
	}
	if _, ok := NextSunEvent(day, 52.52, 13.405, "sunset", 0); !ok {
		t.Error("no next sunset")
	}
	if _, _, ok := SunTimes(day, 80, 0); ok {
		t.Error("polar day must report no sunrise")
	}
}
