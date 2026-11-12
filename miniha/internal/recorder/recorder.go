/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package recorder stores state history in SQLite with as few writes as
// possible: state_changed events are buffered in RAM and written in one
// transaction per flush interval, so the SD card sees a handful of
// sequential writes instead of one per event.
package recorder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/probonopd/miniha/internal/core"
	_ "modernc.org/sqlite"
)

const (
	defaultFlushInterval = 5 * time.Minute
	defaultKeepDays      = 10

	// walAutoCheckpointPages is about 40 MB of WAL: far above what one
	// flush writes, so a checkpoint happens roughly once per flush
	// window instead of every 1000 pages.
	walAutoCheckpointPages = 10000

	// maxBuffered bounds RAM when the interval is long or flushes fail;
	// reaching it triggers an early (still batched) flush.
	maxBuffered = 20000

	// attrCacheLimit bounds the JSON -> attributes_id cache.
	attrCacheLimit = 4096
)

// purgeDelay is how long after start the first purge runs. A device that
// reboots more often than daily would otherwise never purge.
var purgeDelay = 10 * time.Minute

var bg = context.Background()

// Config configures the recorder. Include and Exclude entries are entity
// ids ("sensor.temp"), domains ("sensor") or globs ("sensor.living_*").
type Config struct {
	Path          string        `yaml:"path"`
	FlushInterval time.Duration `yaml:"flush_interval"`
	PurgeKeepDays int           `yaml:"purge_keep_days"`
	Include       []string      `yaml:"include"`
	Exclude       []string      `yaml:"exclude"`
}

// pending is one buffered state row. A nil state records that the entity
// was removed from the state machine: it is stored as a row with state
// NULL, which ends the series in History and shows as "was removed" in
// the logbook.
type pending struct {
	entityID  string
	state     *string
	attrs     string // canonical JSON (sorted keys); empty for removal
	changed   time.Time
	updated   time.Time
	reported  time.Time
	contextID string

	// Set only for a numeric sensor state with a valid state_class; they
	// register the entity for long-term statistics.
	stat *statMeta
}

type statMeta struct {
	class string
	unit  string
}

func (m statMeta) hasMean() bool { return m.class == "measurement" }

type Recorder struct {
	core *core.Core
	cfg  Config
	log  *slog.Logger
	db   *sql.DB
	flt  filter

	unsub    func()
	done     chan struct{}
	kick     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopErr  error

	// mu guards the buffer only, so the event handler never waits for
	// the database.
	mu        sync.Mutex
	buf       []pending
	lastFlush time.Time

	// dbMu serializes writers (flush, purge) and lets readers see the
	// database and the buffer as one consistent snapshot. It also guards
	// the caches below.
	dbMu          sync.RWMutex
	metaIDs       map[string]int64
	attrIDs       map[string]int64
	lastStateID   map[int64]int64
	statMetas     map[int64]statMeta
	lastStatsHour int64

	rows atomic.Int64
}

// New opens (creating if needed) the database and starts the flush and
// purge goroutines. Call Stop to flush and close.
func New(c *core.Core, cfg Config, log *slog.Logger) (*Recorder, error) {
	if cfg.Path == "" {
		return nil, errors.New("recorder: path is required")
	}
	if cfg.FlushInterval < 0 || cfg.PurgeKeepDays < 0 {
		return nil, errors.New("recorder: negative flush interval or purge_keep_days")
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.PurgeKeepDays == 0 {
		cfg.PurgeKeepDays = defaultKeepDays
	}
	flt, err := newFilter(cfg.Include, cfg.Exclude)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	db, err := openDB(cfg.Path, walAutoCheckpointPages)
	if err != nil {
		return nil, fmt.Errorf("recorder: open %s: %w", cfg.Path, err)
	}
	r := &Recorder{
		core: c, cfg: cfg, log: log, db: db, flt: flt,
		done:        make(chan struct{}),
		kick:        make(chan struct{}, 1),
		metaIDs:     map[string]int64{},
		attrIDs:     map[string]int64{},
		lastStateID: map[int64]int64{},
		statMetas:   map[int64]statMeta{},
	}
	if err := r.loadCaches(); err != nil {
		db.Close()
		return nil, fmt.Errorf("recorder: %w", err)
	}
	r.unsub = c.Subscribe("state_changed", r.onStateChanged)
	r.wg.Add(2)
	go r.flushLoop()
	go r.purgeLoop()
	return r, nil
}

func (r *Recorder) loadCaches() error {
	rows, err := r.db.Query("SELECT metadata_id, entity_id FROM states_meta")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var e string
		if err := rows.Scan(&id, &e); err != nil {
			rows.Close()
			return err
		}
		r.metaIDs[e] = id
	}
	if err := rows.Close(); err != nil {
		return err
	}
	srows, err := r.db.Query("SELECT metadata_id, state_class, COALESCE(unit_of_measurement, '') FROM statistics_meta")
	if err != nil {
		return err
	}
	for srows.Next() {
		var id int64
		var m statMeta
		if err := srows.Scan(&id, &m.class, &m.unit); err != nil {
			srows.Close()
			return err
		}
		r.statMetas[id] = m
	}
	if err := srows.Close(); err != nil {
		return err
	}
	// Counted once; flush and purge keep it current so the status page
	// never scans the table.
	var n int64
	if err := r.db.QueryRow("SELECT count(*) FROM states").Scan(&n); err != nil {
		return err
	}
	r.rows.Store(n)
	return nil
}

func micro(t time.Time) time.Time { return time.UnixMicro(t.UnixMicro()).UTC() }

func tsOf(t time.Time) float64 { return float64(t.UnixMicro()) / 1e6 }

func timeOf(ts float64) time.Time { return time.UnixMicro(int64(math.Round(ts * 1e6))).UTC() }

func attrsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !reflect.DeepEqual(av, bv) {
			return false
		}
	}
	return true
}

// canonicalAttrs relies on encoding/json sorting map keys, so equal
// attribute sets always serialize to the same string.
func canonicalAttrs(a map[string]any) (string, error) {
	if a == nil {
		return "{}", nil
	}
	b, err := json.Marshal(a)
	return string(b), err
}

var statClasses = map[string]bool{"measurement": true, "total": true, "total_increasing": true}

func statMetaFor(s *core.State) *statMeta {
	if s.Domain() != "sensor" {
		return nil
	}
	class, _ := s.Attributes["state_class"].(string)
	if !statClasses[class] {
		return nil
	}
	v, err := strconv.ParseFloat(s.State, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	unit, _ := s.Attributes["unit_of_measurement"].(string)
	return &statMeta{class: class, unit: unit}
}

// onStateChanged runs on the firing goroutine, so it only filters and
// appends to the buffer.
func (r *Recorder) onStateChanged(ev core.Event) {
	entityID, _ := ev.Data["entity_id"].(string)
	if entityID == "" || !r.flt.allows(entityID) {
		return
	}
	// A failed assertion means a foreign event of the same type with
	// another payload shape (fired through the API); a removal always
	// carries a typed nil pointer.
	ns, ok := ev.Data["new_state"].(*core.State)
	if !ok {
		return
	}
	oldS, ok := ev.Data["old_state"].(*core.State)
	if !ok {
		return
	}
	var p pending
	if ns == nil {
		if oldS == nil {
			return
		}
		t := micro(ev.TimeFired)
		p = pending{entityID: entityID, changed: t, updated: t, reported: t, contextID: ev.Context.ID}
	} else {
		if oldS != nil && oldS.State == ns.State && attrsEqual(oldS.Attributes, ns.Attributes) {
			return
		}
		attrs, err := canonicalAttrs(ns.Attributes)
		if err != nil {
			r.log.Error("recorder: attributes not serializable, state not recorded", "entity_id", entityID, "err", err)
			return
		}
		st := ns.State
		p = pending{
			entityID: entityID, state: &st, attrs: attrs,
			changed: micro(ns.LastChanged), updated: micro(ns.LastUpdated), reported: micro(ns.LastReported),
			contextID: ns.Context.ID, stat: statMetaFor(ns),
		}
	}
	r.mu.Lock()
	r.buf = append(r.buf, p)
	n := len(r.buf)
	r.mu.Unlock()
	if n >= maxBuffered {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

func (r *Recorder) flushLoop() {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-r.kick:
		case <-r.done:
			return
		}
		if err := r.Flush(); err != nil {
			r.log.Error("recorder: flush failed, events stay buffered", "err", err)
		}
	}
}

func (r *Recorder) purgeLoop() {
	defer r.wg.Done()
	t := time.NewTimer(purgeDelay)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-r.done:
			return
		}
		if err := r.PurgeNow(); err != nil {
			r.log.Error("recorder: purge failed", "err", err)
		}
		t.Reset(24 * time.Hour)
	}
}

// Flush writes everything buffered, and any newly completed hour of
// statistics, in a single transaction. On error the events stay buffered.
func (r *Recorder) Flush() error {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	r.mu.Lock()
	batch := r.buf
	r.buf = nil
	r.mu.Unlock()
	now := r.core.Now()
	if err := r.write(batch, now); err != nil {
		r.mu.Lock()
		r.buf = append(batch, r.buf...)
		r.mu.Unlock()
		return err
	}
	if len(batch) > 0 {
		r.mu.Lock()
		r.lastFlush = now
		r.mu.Unlock()
	}
	return nil
}

// staged holds cache additions made inside a transaction; they are
// merged only after commit so a rollback cannot leave ids that do not
// exist in the file.
type staged struct {
	meta  map[string]int64
	attrs map[string]int64
	last  map[int64]int64
	stat  map[int64]statMeta
}

func (r *Recorder) write(batch []pending, now time.Time) error {
	horizon := statsHorizon(now)
	statsDue := horizon > r.lastStatsHour && (len(r.statMetas) > 0 || hasStat(batch))
	if len(batch) == 0 && !statsDue {
		return nil
	}
	if len(r.attrIDs) > attrCacheLimit {
		r.attrIDs = map[string]int64{}
	}
	tx, err := r.db.BeginTx(bg, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st := &staged{meta: map[string]int64{}, attrs: map[string]int64{}, last: map[int64]int64{}, stat: map[int64]statMeta{}}
	if err := r.insertBatch(tx, st, batch); err != nil {
		return err
	}
	if statsDue {
		if err := r.computeStats(tx, st, horizon); err != nil {
			return fmt.Errorf("statistics: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for k, v := range st.meta {
		r.metaIDs[k] = v
	}
	for k, v := range st.attrs {
		r.attrIDs[k] = v
	}
	for k, v := range st.last {
		r.lastStateID[k] = v
	}
	for k, v := range st.stat {
		r.statMetas[k] = v
	}
	if statsDue {
		r.lastStatsHour = horizon
	}
	r.rows.Add(int64(len(batch)))
	return nil
}

func hasStat(batch []pending) bool {
	for i := range batch {
		if batch[i].stat != nil {
			return true
		}
	}
	return false
}

func (r *Recorder) insertBatch(tx *sql.Tx, st *staged, batch []pending) error {
	if len(batch) == 0 {
		return nil
	}
	insState, err := tx.Prepare(`INSERT INTO states (metadata_id, state, attributes_id, last_changed_ts, last_updated_ts, last_reported_ts, old_state_id, context_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insState.Close()
	for i := range batch {
		p := &batch[i]
		mid, err := r.metaID(tx, st, p.entityID)
		if err != nil {
			return err
		}
		var aid sql.NullInt64
		if p.state != nil {
			id, err := r.attrID(tx, st, p.attrs)
			if err != nil {
				return err
			}
			aid = sql.NullInt64{Int64: id, Valid: true}
		}
		old, err := r.previousStateID(tx, st, mid)
		if err != nil {
			return err
		}
		var state sql.NullString
		if p.state != nil {
			state = sql.NullString{String: *p.state, Valid: true}
		}
		res, err := insState.Exec(mid, state, aid, tsOf(p.changed), tsOf(p.updated), tsOf(p.reported), old, p.contextID)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		st.last[mid] = id
		if p.stat != nil {
			if err := r.registerStat(tx, st, mid, p.entityID, *p.stat); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Recorder) metaID(tx *sql.Tx, st *staged, entityID string) (int64, error) {
	if id, ok := st.meta[entityID]; ok {
		return id, nil
	}
	if id, ok := r.metaIDs[entityID]; ok {
		return id, nil
	}
	res, err := tx.Exec("INSERT INTO states_meta (entity_id) VALUES (?)", entityID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	st.meta[entityID] = id
	return id, nil
}

func attrHash(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64())
}

// attrID returns the id of the stored attribute set, inserting it on first
// use. The hash only narrows the lookup; the text is compared so a
// collision cannot merge two different sets.
func (r *Recorder) attrID(tx *sql.Tx, st *staged, attrs string) (int64, error) {
	if id, ok := st.attrs[attrs]; ok {
		return id, nil
	}
	if id, ok := r.attrIDs[attrs]; ok {
		return id, nil
	}
	h := attrHash(attrs)
	rows, err := tx.Query("SELECT attributes_id, shared_attrs FROM state_attributes WHERE hash = ?", h)
	if err != nil {
		return 0, err
	}
	found := int64(-1)
	for rows.Next() {
		var id int64
		var s string
		if err := rows.Scan(&id, &s); err != nil {
			rows.Close()
			return 0, err
		}
		if s == attrs {
			found = id
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if found < 0 {
		res, err := tx.Exec("INSERT INTO state_attributes (hash, shared_attrs) VALUES (?, ?)", h, attrs)
		if err != nil {
			return 0, err
		}
		if found, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	}
	st.attrs[attrs] = found
	return found, nil
}

func (r *Recorder) previousStateID(tx *sql.Tx, st *staged, mid int64) (sql.NullInt64, error) {
	if id, ok := st.last[mid]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	if id, ok := r.lastStateID[mid]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	var id int64
	err := tx.QueryRow("SELECT state_id FROM states WHERE metadata_id = ? ORDER BY last_updated_ts DESC, state_id DESC LIMIT 1", mid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, nil
	}
	return sql.NullInt64{Int64: id, Valid: err == nil}, err
}

func (r *Recorder) registerStat(tx *sql.Tx, st *staged, mid int64, entityID string, m statMeta) error {
	cur, ok := st.stat[mid]
	if !ok {
		cur, ok = r.statMetas[mid]
	}
	if ok && cur == m {
		return nil
	}
	hasMean, hasSum := 0, 1
	if m.hasMean() {
		hasMean, hasSum = 1, 0
	}
	_, err := tx.Exec(`INSERT INTO statistics_meta (metadata_id, statistic_id, source, unit_of_measurement, state_class, has_mean, has_sum)
		VALUES (?, ?, 'recorder', ?, ?, ?, ?)
		ON CONFLICT(metadata_id) DO UPDATE SET unit_of_measurement = excluded.unit_of_measurement,
			state_class = excluded.state_class, has_mean = excluded.has_mean, has_sum = excluded.has_sum`,
		mid, entityID, m.unit, m.class, hasMean, hasSum)
	if err != nil {
		return err
	}
	st.stat[mid] = m
	return nil
}

// Stop stops the goroutines, flushes, checkpoints the WAL and closes the
// database. It is safe to call more than once.
func (r *Recorder) Stop() error {
	r.stopOnce.Do(func() {
		r.unsub()
		close(r.done)
		r.wg.Wait()
		ferr := r.Flush()
		r.dbMu.Lock()
		_, cerr := r.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		clerr := r.db.Close()
		r.dbMu.Unlock()
		r.stopErr = errors.Join(ferr, cerr, clerr)
	})
	return r.stopErr
}

// Stats feeds the status page.
func (r *Recorder) Stats() map[string]any {
	var size int64
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(r.cfg.Path + suffix); err == nil {
			size += fi.Size()
		}
	}
	r.mu.Lock()
	buffered := len(r.buf)
	last := r.lastFlush
	r.mu.Unlock()
	out := map[string]any{
		"rows":                   r.rows.Load(),
		"db_bytes":               size,
		"buffered_events":        buffered,
		"flush_interval_seconds": int(r.cfg.FlushInterval / time.Second),
		"last_flush":             nil,
	}
	if !last.IsZero() {
		out["last_flush"] = last.UTC().Format(time.RFC3339)
	}
	return out
}
