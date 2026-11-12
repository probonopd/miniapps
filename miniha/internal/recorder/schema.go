/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// schemaVersion is stored in schema_changes; a newer file is refused
// instead of being read with the wrong assumptions.
const schemaVersion = 1

// The layout follows HA's recorder (states_meta, state_attributes, states,
// statistics_meta, statistics) so the attribute and entity-id dedup behaves
// the same, but with the columns MiniHA needs only.
//
// Index choice is SD-card driven: states(last_updated_ts) is appended in
// time order (cheap) and makes the daily purge a range scan;
// states(metadata_id, last_updated_ts) serves history queries;
// states(attributes_id) lets the purge find orphaned attribute sets
// without scanning.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_changes (
	version    INTEGER PRIMARY KEY,
	changed_ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS states_meta (
	metadata_id INTEGER PRIMARY KEY,
	entity_id   TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS state_attributes (
	attributes_id INTEGER PRIMARY KEY,
	hash          INTEGER NOT NULL,
	shared_attrs  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_state_attributes_hash ON state_attributes (hash);
CREATE TABLE IF NOT EXISTS states (
	state_id         INTEGER PRIMARY KEY,
	metadata_id      INTEGER NOT NULL,
	state            TEXT,
	attributes_id    INTEGER,
	last_changed_ts  REAL NOT NULL,
	last_updated_ts  REAL NOT NULL,
	last_reported_ts REAL NOT NULL,
	old_state_id     INTEGER,
	context_id       TEXT
);
CREATE INDEX IF NOT EXISTS ix_states_last_updated_ts ON states (last_updated_ts);
CREATE INDEX IF NOT EXISTS ix_states_metadata_id_last_updated_ts ON states (metadata_id, last_updated_ts);
CREATE INDEX IF NOT EXISTS ix_states_attributes_id ON states (attributes_id);
CREATE TABLE IF NOT EXISTS statistics_meta (
	metadata_id         INTEGER PRIMARY KEY,
	statistic_id        TEXT NOT NULL UNIQUE,
	source              TEXT NOT NULL,
	unit_of_measurement TEXT,
	state_class         TEXT NOT NULL,
	has_mean            INTEGER NOT NULL,
	has_sum             INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS statistics (
	id          INTEGER PRIMARY KEY,
	metadata_id INTEGER NOT NULL,
	start_ts    REAL NOT NULL,
	mean        REAL,
	min         REAL,
	max         REAL,
	state       REAL,
	sum         REAL
);
CREATE UNIQUE INDEX IF NOT EXISTS ix_statistics_metadata_id_start_ts ON statistics (metadata_id, start_ts);
`

// openDB creates the directory, applies the persistent pragmas before the
// first table exists (page_size and auto_vacuum only take effect on an
// empty database) and returns the pool. Per-connection pragmas travel in
// the DSN so every pooled connection gets them.
func openDB(path string, walAutoCheckpointPages int) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", walAutoCheckpointPages))
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if err := initDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func initDB(db *sql.DB) error {
	// One connection for the setup so the pragma order is deterministic.
	conn, err := db.Conn(bg)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, p := range []string{
		"PRAGMA page_size = 4096",
		"PRAGMA auto_vacuum = INCREMENTAL",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := conn.ExecContext(bg, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	// An existing file made with another mode would silently never shrink
	// after a purge; refuse it rather than pretend.
	var av int
	if err := conn.QueryRowContext(bg, "PRAGMA auto_vacuum").Scan(&av); err != nil {
		return err
	}
	if av != 2 {
		return fmt.Errorf("history database is not in incremental auto_vacuum mode (%d)", av)
	}
	if _, err := conn.ExecContext(bg, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	var have sql.NullInt64
	if err := conn.QueryRowContext(bg, "SELECT max(version) FROM schema_changes").Scan(&have); err != nil {
		return err
	}
	if have.Valid && have.Int64 > schemaVersion {
		return fmt.Errorf("history database schema %d is newer than supported %d", have.Int64, schemaVersion)
	}
	if !have.Valid {
		if _, err := conn.ExecContext(bg, "INSERT INTO schema_changes (version, changed_ts) VALUES (?, ?)", schemaVersion, float64(time.Now().Unix())); err != nil {
			return err
		}
	}
	return nil
}
