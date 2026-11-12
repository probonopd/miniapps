/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"database/sql"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"
)

// statsGrace keeps an hour open for a minute after it ends: an event
// stamped just before the boundary can reach the buffer after the batch
// was taken, and its hour must not be finalized without it.
const statsGrace = time.Minute

// statsHorizon is the start of the newest hour that is safe to finalize.
func statsHorizon(now time.Time) int64 {
	return now.Add(-statsGrace).Unix() / 3600 * 3600
}

type sample struct {
	ts  float64
	val float64
	ok  bool // numeric; unavailable, unknown or removed are gaps
}

func parseSample(ts float64, state sql.NullString) sample {
	if !state.Valid {
		return sample{ts: ts}
	}
	v, err := strconv.ParseFloat(state.String, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return sample{ts: ts}
	}
	return sample{ts: ts, val: v, ok: true}
}

// computeStats finalizes every completed hour that has no statistics row
// yet, for each registered entity, inside the flush transaction. The
// raw rows are the source, so a gap in reports (a constant sensor) still
// yields rows: the last value stays in effect until the next change.
func (r *Recorder) computeStats(tx *sql.Tx, st *staged, horizon int64) error {
	metas := make(map[int64]statMeta, len(r.statMetas)+len(st.stat))
	for k, v := range r.statMetas {
		metas[k] = v
	}
	for k, v := range st.stat {
		metas[k] = v
	}
	ids := make([]int64, 0, len(metas))
	for id := range metas {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	ins, err := tx.Prepare("INSERT INTO statistics (metadata_id, start_ts, mean, min, max, state, sum) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, mid := range ids {
		if err := r.statsFor(tx, ins, mid, metas[mid], horizon); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) statsFor(tx *sql.Tx, ins *sql.Stmt, mid int64, m statMeta, horizon int64) error {
	var lastStart float64
	var prevState, prevSum sql.NullFloat64
	err := tx.QueryRow("SELECT start_ts, state, sum FROM statistics WHERE metadata_id = ? ORDER BY start_ts DESC LIMIT 1", mid).
		Scan(&lastStart, &prevState, &prevSum)
	var from int64
	switch {
	case err == nil:
		from = int64(lastStart) + 3600
	case errors.Is(err, sql.ErrNoRows):
		var first sql.NullFloat64
		if err := tx.QueryRow("SELECT min(last_updated_ts) FROM states WHERE metadata_id = ?", mid).Scan(&first); err != nil {
			return err
		}
		if !first.Valid {
			return nil
		}
		from = int64(first.Float64) / 3600 * 3600
	default:
		return err
	}
	if from >= horizon {
		return nil
	}

	cur := sample{}
	var priorState sql.NullString
	var priorTS float64
	switch err := tx.QueryRow("SELECT state, last_updated_ts FROM states WHERE metadata_id = ? AND last_updated_ts < ? ORDER BY last_updated_ts DESC, state_id DESC LIMIT 1",
		mid, float64(from)).Scan(&priorState, &priorTS); {
	case err == nil:
		cur = parseSample(priorTS, priorState)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	rows, err := tx.Query("SELECT state, last_updated_ts FROM states WHERE metadata_id = ? AND last_updated_ts >= ? AND last_updated_ts < ? ORDER BY last_updated_ts, state_id",
		mid, float64(from), float64(horizon))
	if err != nil {
		return err
	}
	var samples []sample
	for rows.Next() {
		var s sql.NullString
		var ts float64
		if err := rows.Scan(&s, &ts); err != nil {
			rows.Close()
			return err
		}
		samples = append(samples, parseSample(ts, s))
	}
	if err := rows.Close(); err != nil {
		return err
	}

	idx := 0
	for hs := from; hs < horizon; hs += 3600 {
		he := float64(hs + 3600)
		seg := float64(hs)
		var weighted, covered, lo, hi, last float64
		seen := false
		observe := func(v float64) {
			if !seen || v < lo {
				lo = v
			}
			if !seen || v > hi {
				hi = v
			}
			seen = true
			last = v
		}
		// The value in effect until the next sample counts toward the
		// time-weighted mean, so a sensor reporting rarely is not
		// dominated by a burst of readings.
		advance := func(until float64) {
			if cur.ok && until > seg {
				weighted += cur.val * (until - seg)
				covered += until - seg
			}
			seg = until
		}
		if cur.ok {
			observe(cur.val)
		}
		for idx < len(samples) && samples[idx].ts < he {
			advance(samples[idx].ts)
			cur = samples[idx]
			if cur.ok {
				observe(cur.val)
			}
			idx++
		}
		advance(he)
		if !seen || covered == 0 {
			continue
		}
		if m.hasMean() {
			if _, err := ins.Exec(mid, float64(hs), weighted/covered, lo, hi, nil, nil); err != nil {
				return err
			}
			continue
		}
		// total / total_increasing: sum accumulates the change of the
		// meter; a drop of a total_increasing meter is a reset, so the
		// new reading counts as the increase since zero.
		sum := 0.0
		if prevState.Valid {
			delta := last - prevState.Float64
			if m.class == "total_increasing" && delta < 0 {
				delta = last
			}
			sum = prevSum.Float64 + delta
		}
		if _, err := ins.Exec(mid, float64(hs), nil, nil, nil, last, sum); err != nil {
			return err
		}
		prevState = sql.NullFloat64{Float64: last, Valid: true}
		prevSum = sql.NullFloat64{Float64: sum, Valid: true}
	}
	return nil
}
