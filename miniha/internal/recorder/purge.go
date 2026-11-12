/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"time"
)

const purgeBatchSize = 1000

// PurgeNow deletes states older than PurgeKeepDays. Statistics are kept.
// Work is done in small transactions, releasing the lock in between so
// flushes and queries are not stalled, and each batch hands a few free
// pages back to the filesystem; a full VACUUM would rewrite the whole
// file on the SD card and is never run.
func (r *Recorder) PurgeNow() error {
	cutoff := tsOf(r.core.Now().Add(-time.Duration(r.cfg.PurgeKeepDays) * 24 * time.Hour))
	for {
		n, err := r.purgeBatch(cutoff)
		if err != nil {
			return err
		}
		if n < purgeBatchSize {
			return nil
		}
	}
}

func (r *Recorder) purgeBatch(cutoff float64) (int, error) {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	tx, err := r.db.BeginTx(bg, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	const batch = `SELECT state_id FROM states WHERE last_updated_ts < ? ORDER BY last_updated_ts, state_id LIMIT ?`
	// The attribute sets of the doomed rows must be read before the
	// delete to find the ones left unreferenced.
	rows, err := tx.Query(`SELECT DISTINCT attributes_id FROM states WHERE attributes_id IS NOT NULL AND state_id IN (`+batch+`)`, cutoff, purgeBatchSize)
	if err != nil {
		return 0, err
	}
	var attrIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		attrIDs = append(attrIDs, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM states WHERE state_id IN (`+batch+`)`, cutoff, purgeBatchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	dropped := false
	for _, id := range attrIDs {
		res, err := tx.Exec(`DELETE FROM state_attributes WHERE attributes_id = ? AND NOT EXISTS (SELECT 1 FROM states WHERE attributes_id = ?)`, id, id)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if n > 0 {
			dropped = true
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	r.rows.Add(-deleted)
	if dropped {
		// A cached id may now point at a deleted attribute set.
		r.attrIDs = map[string]int64{}
	}
	if deleted > 0 {
		if _, err := r.db.Exec("PRAGMA incremental_vacuum(100)"); err != nil {
			return 0, err
		}
	}
	return int(deleted), nil
}
