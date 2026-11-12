/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	c   *core.Core
	r   *Recorder
	now time.Time
	cfg Config
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	e := &env{t: t, c: core.New(), now: t0, cfg: cfg}
	e.c.Now = func() time.Time { return e.now }
	if cfg.Path == "" {
		e.cfg.Path = filepath.Join(t.TempDir(), "sub", "history.db")
	}
	e.open()
	t.Cleanup(func() { e.r.Stop() })
	return e
}

func (e *env) open() {
	e.t.Helper()
	r, err := New(e.c, e.cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	e.r = r
}

func (e *env) set(at time.Time, id, state string, attrs map[string]any) {
	e.t.Helper()
	e.now = at
	if err := e.c.SetState(id, state, attrs, core.Context{}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) flush() {
	e.t.Helper()
	if err := e.r.Flush(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.r.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestPragmasAndSchema(t *testing.T) {
	e := newEnv(t, Config{})
	checks := map[string]string{"journal_mode": "wal", "auto_vacuum": "2", "page_size": "4096", "synchronous": "1", "wal_autocheckpoint": "10000"}
	for pragma, want := range checks {
		var got string
		if err := e.r.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
	if e.cfg.Path == "" || e.r.cfg.FlushInterval != 5*time.Minute || e.r.cfg.PurgeKeepDays != 10 {
		t.Errorf("defaults not applied: %+v", e.r.cfg)
	}
	for _, table := range []string{"states_meta", "state_attributes", "states", "statistics_meta", "statistics", "schema_changes"} {
		if e.count("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table) != 1 {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestFlushWritesOneBatch(t *testing.T) {
	e := newEnv(t, Config{})
	ids := []string{"sensor.a", "sensor.b", "light.c"}
	n := 0
	for i := 0; i < 100; i++ {
		for _, id := range ids {
			e.set(t0.Add(time.Duration(n)*time.Second), id, fmt.Sprint(i), nil)
			n++
		}
	}
	if got := e.count("SELECT count(*) FROM states"); got != 0 {
		t.Fatalf("rows before flush = %d, want 0 (never write per event)", got)
	}
	if got := e.r.Stats()["buffered_events"]; got != 300 {
		t.Fatalf("buffered = %v, want 300", got)
	}
	e.flush()
	if got := e.count("SELECT count(*) FROM states"); got != 300 {
		t.Fatalf("rows after flush = %d, want 300", got)
	}
	st := e.r.Stats()
	if st["rows"] != int64(300) || st["buffered_events"] != 0 || st["last_flush"] == nil || st["db_bytes"].(int64) <= 0 {
		t.Errorf("stats = %v", st)
	}
	// An empty flush must not touch the file.
	e.flush()
	if got := e.count("SELECT count(*) FROM states"); got != 300 {
		t.Fatalf("rows after empty flush = %d", got)
	}
	// old_state_id chains the rows of one entity.
	if got := e.count("SELECT count(*) FROM states WHERE old_state_id IS NULL"); got != 3 {
		t.Errorf("rows without predecessor = %d, want 3", got)
	}
}

func TestOnlyRealChangesRecorded(t *testing.T) {
	e := newEnv(t, Config{})
	e.set(t0, "sensor.x", "1", map[string]any{"a": 1})
	e.set(t0.Add(time.Minute), "sensor.x", "1", map[string]any{"a": 1}) // last_reported only
	if err := e.c.SetStateForce("sensor.x", "1", map[string]any{"a": 1}, core.Context{}); err != nil {
		t.Fatal(err)
	}
	e.set(t0.Add(2*time.Minute), "sensor.x", "1", map[string]any{"a": 2}) // attribute change
	e.flush()
	if got := e.count("SELECT count(*) FROM states"); got != 2 {
		t.Errorf("rows = %d, want 2", got)
	}
}

func TestAttributeDedup(t *testing.T) {
	e := newEnv(t, Config{})
	a := map[string]any{"unit_of_measurement": "C", "friendly_name": "T", "nested": map[string]any{"z": 1, "a": 2}}
	e.set(t0, "sensor.t", "1", a)
	e.set(t0.Add(time.Minute), "sensor.t", "2", a)
	e.set(t0.Add(2*time.Minute), "sensor.u", "2", a)
	e.set(t0.Add(3*time.Minute), "sensor.t", "3", map[string]any{"unit_of_measurement": "F"})
	e.flush()
	if got := e.count("SELECT count(DISTINCT attributes_id) FROM states"); got != 2 {
		t.Errorf("distinct attributes_id = %d, want 2", got)
	}
	if got := e.count("SELECT count(*) FROM state_attributes"); got != 2 {
		t.Errorf("state_attributes rows = %d, want 2", got)
	}
	// Dedup also holds across flushes and after the cache is gone.
	e.set(t0.Add(4*time.Minute), "sensor.t", "4", a)
	e.r.attrIDs = map[string]int64{}
	e.flush()
	if got := e.count("SELECT count(*) FROM state_attributes"); got != 2 {
		t.Errorf("state_attributes rows after second flush = %d, want 2", got)
	}
}

func TestHistory(t *testing.T) {
	e := newEnv(t, Config{})
	h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	e.set(h(10), "light.a", "off", map[string]any{"brightness": 1})
	e.set(h(11), "light.a", "on", map[string]any{"brightness": 1})
	e.set(h(12), "light.a", "on", map[string]any{"brightness": 2})
	e.set(h(13), "light.a", "off", map[string]any{"brightness": 2})
	e.set(h(11), "light.b", "on", nil)
	e.flush()

	start, end := h(11).Add(30*time.Minute), h(13).Add(30*time.Minute)
	out, err := e.r.History(start, end, []string{"light.a"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0]) != 3 {
		t.Fatalf("got %v", out)
	}
	first := out[0][0]
	if first["state"] != "on" || first["entity_id"] != "light.a" ||
		first["last_changed"] != "2026-01-01T11:30:00.000000+00:00" || first["last_updated"] != "2026-01-01T11:30:00.000000+00:00" {
		t.Errorf("prior-state item = %v", first)
	}
	if out[0][1]["last_updated"] != "2026-01-01T12:00:00.000000+00:00" || out[0][1]["attributes"].(map[string]any)["brightness"] != float64(2) {
		t.Errorf("second item = %v", out[0][1])
	}
	if out[0][2]["state"] != "off" {
		t.Errorf("third item = %v", out[0][2])
	}

	// Window selection: nothing in the window still yields the start state.
	out, _ = e.r.History(h(20), h(21), []string{"light.a"}, false, false)
	if len(out) != 1 || len(out[0]) != 1 || out[0][0]["state"] != "off" {
		t.Errorf("quiet window = %v", out)
	}
	// Before the first record there is nothing.
	out, _ = e.r.History(h(1), h(2), []string{"light.a"}, false, false)
	if len(out) != 0 {
		t.Errorf("early window = %v", out)
	}
	// A row stamped exactly at start is the start item, not a duplicate.
	out, _ = e.r.History(h(12), h(14), []string{"light.a"}, false, false)
	if len(out[0]) != 2 || out[0][0]["state"] != "on" || out[0][1]["state"] != "off" {
		t.Errorf("boundary window = %v", out)
	}

	// Minimal: no attributes, entity_id only on the first item.
	out, _ = e.r.History(start, end, []string{"light.a"}, true, false)
	if _, ok := out[0][0]["entity_id"]; !ok {
		t.Error("minimal first item lacks entity_id")
	}
	for i, it := range out[0] {
		if _, ok := it["attributes"]; ok {
			t.Errorf("minimal item %d has attributes", i)
		}
		if _, ok := it["entity_id"]; ok && i > 0 {
			t.Errorf("minimal item %d has entity_id", i)
		}
		if it["state"] == nil || it["last_changed"] == nil || it["last_updated"] == nil {
			t.Errorf("minimal item %d incomplete: %v", i, it)
		}
	}

	// Significant only: the attribute-only row at 12:00 disappears.
	out, _ = e.r.History(start, end, []string{"light.a"}, false, true)
	if len(out[0]) != 2 || out[0][0]["state"] != "on" || out[0][1]["state"] != "off" {
		t.Errorf("significant only = %v", out)
	}

	// No ids: every recorded entity, sorted.
	out, _ = e.r.History(h(0), h(24), nil, true, false)
	if len(out) != 2 || out[0][0]["entity_id"] != "light.a" || out[1][0]["entity_id"] != "light.b" {
		t.Errorf("all entities = %v", out)
	}
	// Unknown entity yields nothing.
	out, _ = e.r.History(h(0), h(24), []string{"light.nope"}, false, false)
	if len(out) != 0 {
		t.Errorf("unknown entity = %v", out)
	}
}

func TestHistoryIncludesUnflushedEvents(t *testing.T) {
	e := newEnv(t, Config{})
	e.set(t0, "sensor.p", "1", nil)
	e.flush()
	e.set(t0.Add(time.Hour), "sensor.p", "2", nil)
	e.set(t0.Add(time.Hour), "sensor.q", "9", nil) // never flushed, no metadata row yet
	out, err := e.r.History(t0.Add(30*time.Minute), t0.Add(2*time.Hour), nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || len(out[0]) != 2 || out[0][1]["state"] != "2" || out[1][0]["state"] != "9" {
		t.Errorf("history = %v", out)
	}
}

func TestLogbook(t *testing.T) {
	e := newEnv(t, Config{})
	h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	e.set(h(1), "light.a", "off", map[string]any{"friendly_name": "Kitchen"})
	e.set(h(2), "light.a", "on", map[string]any{"friendly_name": "Kitchen"})
	e.set(h(3), "light.a", "on", map[string]any{"friendly_name": "Kitchen", "brightness": 3}) // attribute only
	e.set(h(4), "light.a", "unavailable", nil)
	e.set(h(5), "sensor.t", "21.5", nil)
	e.c.Now = func() time.Time { return h(6) }
	e.c.RemoveState("sensor.t", core.Context{})
	e.flush()

	out, err := e.r.Logbook(h(2), h(24), "")
	if err != nil {
		t.Fatal(err)
	}
	// The first row of light.a in the window (on, 02:00) differs from
	// the prior (off), so it is listed.
	var got []string
	for _, it := range out {
		got = append(got, fmt.Sprintf("%v|%v|%v|%v|%v", it["when"], it["name"], it["entity_id"], it["state"], it["message"]))
	}
	want := []string{
		"2026-01-01T02:00:00.000000+00:00|Kitchen|light.a|on|turned on",
		"2026-01-01T04:00:00.000000+00:00|Kitchen|light.a|unavailable|became unavailable",
		"2026-01-01T05:00:00.000000+00:00|sensor.t|sensor.t|21.5|changed to 21.5",
		"2026-01-01T06:00:00.000000+00:00|sensor.t|sensor.t||was removed",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("logbook:\n got %q\nwant %q", got, want)
	}
	out, _ = e.r.Logbook(h(0), h(24), "sensor.t")
	if len(out) != 2 {
		t.Errorf("entity filter = %v", out)
	}
	// An attribute-only change as the first row of the window is not an entry.
	out, _ = e.r.Logbook(h(3), h(3).Add(time.Minute), "light.a")
	if len(out) != 0 {
		t.Errorf("attribute-only window = %v", out)
	}
}

func TestRemovalEndsSeries(t *testing.T) {
	e := newEnv(t, Config{})
	e.set(t0, "sensor.r", "1", nil)
	e.now = t0.Add(time.Hour)
	e.c.RemoveState("sensor.r", core.Context{})
	e.flush()
	if got := e.count("SELECT count(*) FROM states WHERE state IS NULL"); got != 1 {
		t.Fatalf("removal rows = %d, want 1", got)
	}
	out, _ := e.r.History(t0.Add(2*time.Hour), t0.Add(3*time.Hour), nil, false, false)
	if len(out) != 0 {
		t.Errorf("history after removal = %v", out)
	}
}

func TestIncludeExclude(t *testing.T) {
	cases := []struct {
		include, exclude []string
		allowed, denied  []string
	}{
		{nil, nil, []string{"sensor.a", "light.b"}, nil},
		{[]string{"sensor"}, nil, []string{"sensor.a"}, []string{"light.b", "sensorx.a"}},
		{[]string{"light.kitchen", "sensor.living_*"}, nil, []string{"light.kitchen", "sensor.living_temp"}, []string{"light.bath", "sensor.bed_temp"}},
		{nil, []string{"sensor.noisy", "update", "switch.tmp_*"}, []string{"sensor.ok", "light.a"}, []string{"sensor.noisy", "update.x", "switch.tmp_1"}},
		{[]string{"sensor"}, []string{"sensor.noisy_*"}, []string{"sensor.ok"}, []string{"sensor.noisy_1", "light.a"}},
	}
	for i, tc := range cases {
		f, err := newFilter(tc.include, tc.exclude)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range tc.allowed {
			if !f.allows(id) {
				t.Errorf("case %d: %s should be allowed", i, id)
			}
		}
		for _, id := range tc.denied {
			if f.allows(id) {
				t.Errorf("case %d: %s should be denied", i, id)
			}
		}
	}
	if _, err := newFilter([]string{"sensor.[a"}, nil); err == nil {
		t.Error("malformed glob accepted")
	}
	if _, err := newFilter(nil, []string{" "}); err == nil {
		t.Error("empty entry accepted")
	}

	e := newEnv(t, Config{Include: []string{"sensor"}, Exclude: []string{"sensor.noisy_*"}})
	e.set(t0, "sensor.ok", "1", nil)
	e.set(t0, "sensor.noisy_1", "1", nil)
	e.set(t0, "light.a", "on", nil)
	e.flush()
	if got := e.count("SELECT count(*) FROM states"); got != 1 {
		t.Errorf("recorded rows = %d, want 1", got)
	}
}

func TestStatistics(t *testing.T) {
	e := newEnv(t, Config{})
	h := func(n int, min int) time.Time {
		return t0.Add(time.Duration(n)*time.Hour + time.Duration(min)*time.Minute)
	}
	temp := map[string]any{"state_class": "measurement", "unit_of_measurement": "C"}
	meter := map[string]any{"state_class": "total_increasing", "unit_of_measurement": "kWh"}
	e.set(h(0, 0), "sensor.temp", "10", temp)
	e.set(h(0, 30), "sensor.temp", "20", temp)
	e.set(h(2, 15), "sensor.temp", "30", temp)
	e.set(h(0, 10), "sensor.meter", "100", meter)
	e.set(h(1, 10), "sensor.meter", "110", meter)
	e.set(h(2, 10), "sensor.meter", "5", meter) // counter reset
	e.set(h(1, 0), "sensor.plain", "5", nil)    // no state_class
	e.set(h(1, 5), "sensor.cls", "x", temp)     // not numeric
	e.set(h(3, 2), "sensor.temp", "31", temp)   // flush time, hour 3 still open
	e.flush()

	type stat struct{ start, mean, min, max, state, sum float64 }
	load := func(entity string) []stat {
		rows, err := e.r.db.Query(`SELECT s.start_ts, COALESCE(s.mean, -1), COALESCE(s.min, -1), COALESCE(s.max, -1), COALESCE(s.state, -1), COALESCE(s.sum, -1)
			FROM statistics s JOIN statistics_meta m USING (metadata_id) WHERE m.statistic_id = ? ORDER BY s.start_ts`, entity)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []stat
		for rows.Next() {
			var s stat
			if err := rows.Scan(&s.start, &s.mean, &s.min, &s.max, &s.state, &s.sum); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	hs := func(n int) float64 { return float64(t0.Add(time.Duration(n) * time.Hour).Unix()) }
	want := []stat{
		{hs(0), 15, 10, 20, -1, -1},
		{hs(1), 20, 20, 20, -1, -1},
		{hs(2), 27.5, 20, 30, -1, -1},
	}
	got := load("sensor.temp")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("temp statistics:\n got %v\nwant %v", got, want)
	}
	wantMeter := []stat{
		{hs(0), -1, -1, -1, 100, 0},
		{hs(1), -1, -1, -1, 110, 10},
		{hs(2), -1, -1, -1, 5, 15},
	}
	if got := load("sensor.meter"); fmt.Sprint(got) != fmt.Sprint(wantMeter) {
		t.Errorf("meter statistics:\n got %v\nwant %v", got, wantMeter)
	}
	if got := e.count("SELECT count(*) FROM statistics_meta"); got != 2 {
		t.Errorf("statistics_meta rows = %d, want 2 (plain and non-numeric excluded)", got)
	}

	// A second flush in the same hour adds nothing; the next hour adds one row per entity.
	e.flush()
	if got := e.count("SELECT count(*) FROM statistics"); got != 6 {
		t.Errorf("statistics rows = %d, want 6", got)
	}
	e.now = h(4, 5)
	e.flush()
	if got := e.count("SELECT count(*) FROM statistics"); got != 8 {
		t.Errorf("statistics rows after next hour = %d, want 8", got)
	}
	last := load("sensor.temp")[3]
	if last.start != hs(3) || math.Abs(last.mean-(30*2+31*58)/60.0) > 1e-9 {
		t.Errorf("hour 3 = %v", last)
	}
}

func TestPurgeKeepsStatistics(t *testing.T) {
	e := newEnv(t, Config{PurgeKeepDays: 10})
	temp := func(i int) map[string]any {
		return map[string]any{"state_class": "measurement", "n": i % 7}
	}
	for i := 0; i < 720; i++ { // every 30 min for 15 days
		e.set(t0.Add(time.Duration(i)*30*time.Minute), "sensor.temp", fmt.Sprint(i), temp(i))
	}
	for i := 0; i < 2500; i++ { // more than two purge batches, unique attributes
		e.set(t0.Add(time.Duration(i)*time.Minute), "sensor.bulk", fmt.Sprint(i), map[string]any{"seq": i})
	}
	e.now = t0.Add(15 * 24 * time.Hour)
	e.flush()
	statsBefore := e.count("SELECT count(*) FROM statistics")
	if statsBefore < 300 {
		t.Fatalf("statistics rows = %d, want a full 15 days", statsBefore)
	}
	cutoff := e.now.Add(-10 * 24 * time.Hour)
	wantLeft := 0
	for i := 0; i < 720; i++ {
		if !t0.Add(time.Duration(i) * 30 * time.Minute).Before(cutoff) {
			wantLeft++
		}
	}
	if err := e.r.PurgeNow(); err != nil {
		t.Fatal(err)
	}
	if got := e.count("SELECT count(*) FROM states"); got != wantLeft {
		t.Errorf("states left = %d, want %d", got, wantLeft)
	}
	if got := e.count("SELECT count(*) FROM states WHERE last_updated_ts < ?", tsOf(cutoff)); got != 0 {
		t.Errorf("%d old rows survived", got)
	}
	if got := e.r.Stats()["rows"]; got != int64(wantLeft) {
		t.Errorf("Stats rows = %v, want %d", got, wantLeft)
	}
	if got := e.count("SELECT count(*) FROM statistics"); got != statsBefore {
		t.Errorf("statistics rows %d -> %d, want kept", statsBefore, got)
	}
	if got := e.count("SELECT count(*) FROM state_attributes WHERE shared_attrs LIKE '%seq%'"); got != 0 {
		t.Errorf("%d orphaned bulk attribute sets survived", got)
	}
	if got := e.count("SELECT count(*) FROM state_attributes WHERE attributes_id NOT IN (SELECT attributes_id FROM states WHERE attributes_id IS NOT NULL)"); got != 0 {
		t.Errorf("%d unreferenced attribute sets", got)
	}
	// Recording continues correctly after a purge dropped attribute sets.
	e.set(e.now.Add(time.Minute), "sensor.bulk", "again", map[string]any{"seq": 1})
	e.flush()
	if got := e.count("SELECT count(*) FROM states s JOIN state_attributes a USING (attributes_id)"); got != wantLeft+1 {
		t.Errorf("rows with attributes = %d, want %d", got, wantLeft+1)
	}
}

func TestStopFlushesAndReopens(t *testing.T) {
	e := newEnv(t, Config{})
	e.set(t0, "sensor.s", "1", map[string]any{"k": "v"})
	e.set(t0.Add(time.Hour), "sensor.s", "2", map[string]any{"k": "v"})
	if err := e.r.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	e.open()
	if got := e.r.Stats()["rows"]; got != int64(2) {
		t.Errorf("rows after reopen = %v", got)
	}
	e.set(t0.Add(2*time.Hour), "sensor.s", "3", map[string]any{"k": "v"})
	e.flush()
	if got := e.count("SELECT count(*) FROM state_attributes"); got != 1 {
		t.Errorf("attribute sets after reopen = %d, want 1", got)
	}
	if got := e.count("SELECT count(*) FROM states_meta"); got != 1 {
		t.Errorf("states_meta rows = %d, want 1", got)
	}
	out, _ := e.r.History(t0, t0.Add(24*time.Hour), []string{"sensor.s"}, true, false)
	if len(out) != 1 || len(out[0]) != 3 {
		t.Errorf("history after reopen = %v", out)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	c := core.New()
	if _, err := New(c, Config{}, nil); err == nil {
		t.Error("empty path accepted")
	}
	if _, err := New(c, Config{Path: filepath.Join(t.TempDir(), "h.db"), Exclude: []string{"sensor.[x"}}, nil); err == nil {
		t.Error("bad glob accepted")
	}
	if _, err := New(c, Config{Path: filepath.Join(t.TempDir(), "h.db"), PurgeKeepDays: -1}, nil); err == nil {
		t.Error("negative keep days accepted")
	}
}
