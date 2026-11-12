/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"math"
	"testing"
	"time"
)

// statsQueryEnv records two numeric sensors over three finished hours.
func statsQueryEnv(t *testing.T) *env {
	t.Helper()
	old := statsLocation
	statsLocation = time.FixedZone("UTC+2", 2*3600)
	t.Cleanup(func() { statsLocation = old })
	e := newEnv(t, Config{})
	h := func(n, min int) time.Time {
		return t0.Add(time.Duration(n)*time.Hour + time.Duration(min)*time.Minute)
	}
	temp := map[string]any{"state_class": "measurement", "unit_of_measurement": "C"}
	meter := map[string]any{"state_class": "total_increasing", "unit_of_measurement": "kWh"}
	e.set(h(0, 0), "sensor.temp", "10", temp)
	e.set(h(0, 30), "sensor.temp", "20", temp)
	e.set(h(2, 15), "sensor.temp", "30", temp)
	e.set(h(0, 10), "sensor.meter", "100", meter)
	e.set(h(1, 10), "sensor.meter", "110", meter)
	e.set(h(2, 10), "sensor.meter", "5", meter)
	e.now = h(3, 5)
	e.flush()
	return e
}

func near(v any, want float64) bool {
	f, ok := v.(float64)
	return ok && math.Abs(f-want) < 1e-9
}

func TestStatisticsHourly(t *testing.T) {
	e := statsQueryEnv(t)
	got, err := e.r.Statistics(t0, time.Time{}, []string{"sensor.temp"}, "hour")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got["sensor.temp"]) != 3 {
		t.Fatalf("got %v", got)
	}
	first := got["sensor.temp"][0]
	if first["start"] != "2026-01-01T00:00:00.000000+00:00" || first["end"] != "2026-01-01T01:00:00.000000+00:00" {
		t.Errorf("bounds: %v", first)
	}
	if !near(first["mean"], 15) || !near(first["min"], 10) || !near(first["max"], 20) || first["state"] != nil || first["sum"] != nil {
		t.Errorf("fields: %v", first)
	}
	// A start after the first hour drops it; an end before the last hour drops that.
	got, _ = e.r.Statistics(t0.Add(time.Hour), t0.Add(2*time.Hour), nil, "hour")
	if len(got["sensor.temp"]) != 1 || len(got["sensor.meter"]) != 1 {
		t.Errorf("window filter: %v", got)
	}
}

func TestStatisticsAggregatesInLocalTime(t *testing.T) {
	e := statsQueryEnv(t)
	day, err := e.r.Statistics(t0, time.Time{}, nil, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(day["sensor.temp"]) != 1 {
		t.Fatalf("day buckets: %v", day)
	}
	d := day["sensor.temp"][0]
	// 00:00 UTC+2 on 2026-01-01 is 22:00 UTC the evening before.
	if d["start"] != "2025-12-31T22:00:00.000000+00:00" || d["end"] != "2026-01-01T22:00:00.000000+00:00" {
		t.Errorf("day bounds: %v", d)
	}
	if !near(d["mean"], (15+20+27.5)/3) || !near(d["min"], 10) || !near(d["max"], 30) {
		t.Errorf("day temp: %v", d)
	}
	m := day["sensor.meter"][0]
	if !near(m["state"], 5) || !near(m["sum"], 15) || m["mean"] != nil {
		t.Errorf("day meter: %v", m)
	}
	week, _ := e.r.Statistics(t0, time.Time{}, []string{"sensor.temp"}, "week")
	// 2026-01-01 is a Thursday; the week starts Monday 2025-12-29 local.
	if w := week["sensor.temp"]; len(w) != 1 || w[0]["start"] != "2025-12-28T22:00:00.000000+00:00" || w[0]["end"] != "2026-01-04T22:00:00.000000+00:00" {
		t.Errorf("week: %v", week)
	}
	month, _ := e.r.Statistics(t0, time.Time{}, []string{"sensor.temp"}, "month")
	if mo := month["sensor.temp"]; len(mo) != 1 || mo[0]["start"] != "2025-12-31T22:00:00.000000+00:00" || mo[0]["end"] != "2026-01-31T22:00:00.000000+00:00" {
		t.Errorf("month: %v", month)
	}
}

func TestStatisticsRejectsUnknownPeriod(t *testing.T) {
	e := statsQueryEnv(t)
	if _, err := e.r.Statistics(t0, time.Time{}, nil, "year"); err == nil {
		t.Fatal("unknown period accepted")
	}
}

func TestStatisticsMetadata(t *testing.T) {
	e := statsQueryEnv(t)
	all := e.r.StatisticsMetadata(nil)
	if len(all) != 2 || all[0]["statistic_id"] != "sensor.meter" || all[1]["statistic_id"] != "sensor.temp" {
		t.Fatalf("all: %v", all)
	}
	if all[0]["has_sum"] != true || all[0]["has_mean"] != false || all[0]["unit_of_measurement"] != "kWh" || all[0]["source"] != "recorder" || all[0]["name"] != nil {
		t.Errorf("meter: %v", all[0])
	}
	one := e.r.StatisticsMetadata([]string{"sensor.temp"})
	if len(one) != 1 || one[0]["has_mean"] != true || one[0]["has_sum"] != false {
		t.Errorf("temp: %v", one)
	}
	if got := e.r.StatisticsMetadata([]string{"sensor.nope"}); len(got) != 0 {
		t.Errorf("unknown id: %v", got)
	}
}
