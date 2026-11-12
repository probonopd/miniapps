/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

// haTime is HA's REST timestamp format: microseconds and "+00:00".
func haTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

// row is one stored (or still buffered) state. A nil state is a removal.
type row struct {
	state   *string
	attrs   string
	changed time.Time
	updated time.Time
}

func rowOf(p *pending) row {
	return row{state: p.state, attrs: p.attrs, changed: p.changed, updated: p.updated}
}

// snapshot is a consistent view of the database plus the not yet flushed
// events. Readers hold dbMu.RLock for its whole life, which keeps a
// flush from moving events from the buffer into the file in between.
type snapshot struct {
	r       *Recorder
	metas   map[string]int64 // flushed entities
	pending map[string][]pending
}

func (r *Recorder) snapshot() *snapshot {
	s := &snapshot{r: r, metas: make(map[string]int64, len(r.metaIDs)), pending: map[string][]pending{}}
	for k, v := range r.metaIDs {
		s.metas[k] = v
	}
	r.mu.Lock()
	buf := r.buf
	r.mu.Unlock()
	for i := range buf {
		s.pending[buf[i].entityID] = append(s.pending[buf[i].entityID], buf[i])
	}
	return s
}

// entities lists every entity with data, sorted.
func (s *snapshot) entities() []string {
	seen := make(map[string]bool, len(s.metas)+len(s.pending))
	var out []string
	for e := range s.metas {
		seen[e] = true
		out = append(out, e)
	}
	for e := range s.pending {
		if !seen[e] {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// prior is the last row of an entity before t, or at t when inclusive.
// History makes it inclusive so a row stamped exactly at the window start
// is the start item instead of appearing twice.
func (s *snapshot) prior(entityID string, t time.Time, inclusive bool) (*row, error) {
	var found *row
	cmp := "<"
	if inclusive {
		cmp = "<="
	}
	for _, p := range s.pending[entityID] {
		if p.updated.Before(t) || (inclusive && p.updated.Equal(t)) {
			rw := rowOf(&p)
			found = &rw
		}
	}
	if found != nil {
		return found, nil
	}
	mid, ok := s.metas[entityID]
	if !ok {
		return nil, nil
	}
	var state sql.NullString
	var attrs sql.NullString
	var changed, updated float64
	err := s.r.db.QueryRow(`SELECT s.state, a.shared_attrs, s.last_changed_ts, s.last_updated_ts
		FROM states s LEFT JOIN state_attributes a ON a.attributes_id = s.attributes_id
		WHERE s.metadata_id = ? AND s.last_updated_ts `+cmp+` ? ORDER BY s.last_updated_ts DESC, s.state_id DESC LIMIT 1`,
		mid, tsOf(t)).Scan(&state, &attrs, &changed, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row{state: nullStr(state), attrs: attrs.String, changed: timeOf(changed), updated: timeOf(updated)}, nil
}

// window returns the rows of one entity in [start, end), oldest first, or
// in (start, end) when exclusive.
func (s *snapshot) window(entityID string, start, end time.Time, exclusive bool) ([]row, error) {
	var out []row
	cmp := ">="
	if exclusive {
		cmp = ">"
	}
	if mid, ok := s.metas[entityID]; ok {
		rows, err := s.r.db.Query(`SELECT s.state, a.shared_attrs, s.last_changed_ts, s.last_updated_ts
			FROM states s LEFT JOIN state_attributes a ON a.attributes_id = s.attributes_id
			WHERE s.metadata_id = ? AND s.last_updated_ts `+cmp+` ? AND s.last_updated_ts < ? ORDER BY s.last_updated_ts, s.state_id`,
			mid, tsOf(start), tsOf(end))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var state, attrs sql.NullString
			var changed, updated float64
			if err := rows.Scan(&state, &attrs, &changed, &updated); err != nil {
				return nil, err
			}
			out = append(out, row{state: nullStr(state), attrs: attrs.String, changed: timeOf(changed), updated: timeOf(updated)})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, p := range s.pending[entityID] {
		if (p.updated.After(start) || (!exclusive && p.updated.Equal(start))) && p.updated.Before(end) {
			out = append(out, rowOf(&p))
		}
	}
	return out, nil
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func parseAttrs(s string) (map[string]any, error) {
	out := map[string]any{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// History implements api.HistorySource. The first item per entity is the
// state in effect at start (stamped with start, as HA does) so graphs
// begin with a value. Events not yet flushed are included.
func (r *Recorder) History(start, end time.Time, entityIDs []string, minimal, significantOnly bool) ([][]map[string]any, error) {
	r.dbMu.RLock()
	defer r.dbMu.RUnlock()
	snap := r.snapshot()
	ids := entityIDs
	if len(ids) == 0 {
		ids = snap.entities()
	}
	var out [][]map[string]any
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		list, err := snap.entityHistory(id, start, end, minimal, significantOnly)
		if err != nil {
			return nil, err
		}
		if len(list) > 0 {
			out = append(out, list)
		}
	}
	return out, nil
}

func (s *snapshot) entityHistory(entityID string, start, end time.Time, minimal, significantOnly bool) ([]map[string]any, error) {
	var rows []row
	prior, err := s.prior(entityID, start, true)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		prior.changed, prior.updated = start, start
		rows = append(rows, *prior)
	}
	win, err := s.window(entityID, start, end, true)
	if err != nil {
		return nil, err
	}
	rows = append(rows, win...)

	var out []map[string]any
	var prev *string
	for _, rw := range rows {
		if rw.state == nil {
			// Removal ends the series; the next state is a new start.
			prev = nil
			continue
		}
		if significantOnly && prev != nil && *prev == *rw.state {
			continue
		}
		prev = rw.state
		item := map[string]any{
			"state":        *rw.state,
			"last_changed": haTime(rw.changed),
			"last_updated": haTime(rw.updated),
		}
		if !minimal || len(out) == 0 {
			item["entity_id"] = entityID
		}
		if !minimal {
			attrs, err := parseAttrs(rw.attrs)
			if err != nil {
				return nil, err
			}
			item["attributes"] = attrs
		}
		out = append(out, item)
	}
	return out, nil
}

// Logbook implements api.HistorySource: one entry per state change in
// [start, end), oldest first. Attribute-only changes are skipped;
// unavailable and unknown are kept, as HA shows them.
func (r *Recorder) Logbook(start, end time.Time, entityID string) ([]map[string]any, error) {
	r.dbMu.RLock()
	defer r.dbMu.RUnlock()
	snap := r.snapshot()
	ids := []string{entityID}
	if entityID == "" {
		ids = snap.entities()
	}
	type entry struct {
		entity string
		row    row
	}
	var all []entry
	for _, id := range ids {
		rows, err := snap.window(id, start, end, false)
		if err != nil {
			return nil, err
		}
		for _, rw := range rows {
			all = append(all, entry{id, rw})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].row.updated.Before(all[j].row.updated) })

	type last struct {
		known bool
		state *string
		name  string
	}
	prev := map[string]*last{}
	out := []map[string]any{}
	for _, e := range all {
		l := prev[e.entity]
		if l == nil {
			l = &last{}
			prior, err := snap.prior(e.entity, start, false)
			if err != nil {
				return nil, err
			}
			if prior != nil {
				l.known, l.state = true, prior.state
				l.name = friendlyName(prior.attrs)
			}
			prev[e.entity] = l
		}
		name := friendlyName(e.row.attrs)
		if name == "" {
			name = l.name
		}
		if name == "" {
			name = e.entity
		}
		cur := e.row.state
		unchanged := l.known && ((cur == nil && l.state == nil) || (cur != nil && l.state != nil && *cur == *l.state))
		l.known, l.state = true, cur
		if friendly := friendlyName(e.row.attrs); friendly != "" {
			l.name = friendly
		}
		if unchanged {
			continue
		}
		item := map[string]any{
			"when":      haTime(e.row.updated),
			"name":      name,
			"entity_id": e.entity,
		}
		if cur == nil {
			item["state"] = ""
			item["message"] = "was removed"
		} else {
			item["state"] = *cur
			item["message"] = logMessage(*cur)
		}
		out = append(out, item)
	}
	return out, nil
}

func logMessage(state string) string {
	switch state {
	case "on":
		return "turned on"
	case "off":
		return "turned off"
	case "unavailable":
		return "became unavailable"
	case "unknown":
		return "became unknown"
	}
	return "changed to " + state
}

func friendlyName(attrs string) string {
	if attrs == "" {
		return ""
	}
	var a struct {
		Name any `json:"friendly_name"`
	}
	if json.Unmarshal([]byte(attrs), &a) != nil {
		return ""
	}
	s, _ := a.Name.(string)
	return s
}
