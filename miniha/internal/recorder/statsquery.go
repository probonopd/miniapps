/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// statsLocation decides where a day, week or month starts. Users read
// "today" on their wall clock, so buckets follow the local zone, not UTC.
// A variable so tests can pin it.
var statsLocation = time.Local

// bucketBounds returns the local-time bucket containing t for a period
// of day, week (starting Monday, as HA does) or month.
func bucketBounds(t time.Time, period string) (time.Time, time.Time) {
	t = t.In(statsLocation)
	y, m, d := t.Date()
	switch period {
	case "day":
		start := time.Date(y, m, d, 0, 0, 0, 0, statsLocation)
		return start, time.Date(y, m, d+1, 0, 0, 0, 0, statsLocation)
	case "week":
		back := (int(t.Weekday()) + 6) % 7
		start := time.Date(y, m, d-back, 0, 0, 0, 0, statsLocation)
		return start, time.Date(y, m, d-back+7, 0, 0, 0, 0, statsLocation)
	default:
		start := time.Date(y, m, 1, 0, 0, 0, 0, statsLocation)
		return start, time.Date(y, m+1, 1, 0, 0, 0, 0, statsLocation)
	}
}

// bucket accumulates the hourly rows of one day, week or month.
type bucket struct {
	start, end time.Time
	meanSum    float64
	meanN      int
	min, max   *float64
	state, sum *float64
}

func (b *bucket) add(mean, lo, hi, state, sum sql.NullFloat64) {
	if mean.Valid {
		b.meanSum += mean.Float64
		b.meanN++
	}
	if lo.Valid && (b.min == nil || lo.Float64 < *b.min) {
		v := lo.Float64
		b.min = &v
	}
	if hi.Valid && (b.max == nil || hi.Float64 > *b.max) {
		v := hi.Float64
		b.max = &v
	}
	// Rows arrive oldest first, so the last non-null value wins.
	if state.Valid {
		v := state.Float64
		b.state = &v
	}
	if sum.Valid {
		v := sum.Float64
		b.sum = &v
	}
}

func optFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func (b *bucket) item() map[string]any {
	var mean any
	if b.meanN > 0 {
		mean = b.meanSum / float64(b.meanN)
	}
	return map[string]any{
		"start": haTime(b.start), "end": haTime(b.end),
		"mean": mean, "min": optFloat(b.min), "max": optFloat(b.max),
		"state": optFloat(b.state), "sum": optFloat(b.sum),
	}
}

// Statistics implements api.HistorySource: the long-term statistics of
// the given statistic ids (all when empty) whose hour starts in
// [start, end); a zero end means no upper bound. Period "hour" returns
// the stored rows, "day", "week" and "month" aggregate them.
func (r *Recorder) Statistics(start, end time.Time, statisticIDs []string, period string) (map[string][]map[string]any, error) {
	switch period {
	case "hour", "day", "week", "month":
	default:
		return nil, fmt.Errorf("unknown statistics period %q (want hour, day, week or month)", period)
	}
	q := `SELECT m.statistic_id, s.start_ts, s.mean, s.min, s.max, s.state, s.sum
		FROM statistics s JOIN statistics_meta m USING (metadata_id) WHERE s.start_ts >= ?`
	args := []any{float64(start.Unix())}
	if !end.IsZero() {
		q += " AND s.start_ts < ?"
		args = append(args, float64(end.Unix()))
	}
	if len(statisticIDs) > 0 {
		q += " AND m.statistic_id IN (?" + strings.Repeat(",?", len(statisticIDs)-1) + ")"
		for _, id := range statisticIDs {
			args = append(args, id)
		}
	}
	q += " ORDER BY m.statistic_id, s.start_ts"

	r.dbMu.RLock()
	defer r.dbMu.RUnlock()
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]map[string]any{}
	var cur *bucket
	var curID string
	flush := func() {
		if cur != nil {
			out[curID] = append(out[curID], cur.item())
			cur = nil
		}
	}
	for rows.Next() {
		var id string
		var ts float64
		var mean, lo, hi, state, sum sql.NullFloat64
		if err := rows.Scan(&id, &ts, &mean, &lo, &hi, &state, &sum); err != nil {
			return nil, err
		}
		at := time.Unix(int64(ts), 0)
		if period == "hour" {
			b := &bucket{start: at, end: at.Add(time.Hour)}
			b.add(mean, lo, hi, state, sum)
			out[id] = append(out[id], b.item())
			continue
		}
		bs, be := bucketBounds(at, period)
		if cur == nil || id != curID || !cur.start.Equal(bs) {
			flush()
			cur, curID = &bucket{start: bs, end: be}, id
		}
		cur.add(mean, lo, hi, state, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// StatisticsMetadata implements api.HistorySource for the given
// statistic ids (all when empty), sorted by id. It reads the in-memory
// registration, which is what the statistics rows were written with.
func (r *Recorder) StatisticsMetadata(ids []string) []map[string]any {
	r.dbMu.RLock()
	defer r.dbMu.RUnlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []map[string]any{}
	for entity, mid := range r.metaIDs {
		m, ok := r.statMetas[mid]
		if !ok || (len(want) > 0 && !want[entity]) {
			continue
		}
		var unit any
		if m.unit != "" {
			unit = m.unit
		}
		out = append(out, map[string]any{
			"statistic_id": entity, "unit_of_measurement": unit,
			"statistics_unit_of_measurement": unit,
			"has_mean":                       m.hasMean(), "has_sum": !m.hasMean(),
			"source": "recorder", "name": nil,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["statistic_id"].(string) < out[j]["statistic_id"].(string) })
	return out
}
