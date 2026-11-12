/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// RegistrySaver persists the entity, device and area registries. Writes
// are debounced so a burst of discovery messages costs one file write.
type RegistrySaver struct {
	path  string
	core  *core.Core
	delay time.Duration
	log   *slog.Logger

	mu    sync.Mutex
	timer *time.Timer
	dirty bool
}

func NewRegistrySaver(path string, c *core.Core, delay time.Duration, log *slog.Logger) *RegistrySaver {
	return &RegistrySaver{path: path, core: c, delay: delay, log: log}
}

// Load restores a saved registry into the core; a missing file is fine.
func (r *RegistrySaver) Load() error {
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snap core.RegistrySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("%s: %w", r.path, err)
	}
	return r.core.Restore(snap)
}

// Trigger schedules a write; it is what the core calls on every change.
func (r *RegistrySaver) Trigger() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirty = true
	if r.timer == nil {
		r.timer = time.AfterFunc(r.delay, func() {
			if err := r.Flush(); err != nil {
				r.log.Error("registry save failed", "err", err)
			}
		})
	}
}

// Flush writes now if anything changed.
func (r *RegistrySaver) Flush() error {
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	dirty := r.dirty
	r.dirty = false
	r.mu.Unlock()
	if !dirty {
		return nil
	}
	raw, err := json.MarshalIndent(r.core.Snapshot(), "", " ")
	if err != nil {
		return err
	}
	return WriteAtomic(r.path, raw, 0o644)
}
