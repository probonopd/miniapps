/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

type script struct {
	e        *Engine
	cfg      scriptConfig
	entityID string
	runs     *runSet
	mu       sync.Mutex
	last     *time.Time
}

func (e *Engine) applyScripts(cfgs []scriptConfig) {
	e.mu.Lock()
	old := e.scripts
	e.scripts = map[string]*script{}
	e.mu.Unlock()
	for _, s := range old {
		s.runs.cancelAll()
	}
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		entry, err := e.core.RegisterEntity(core.EntityRegistration{Domain: "script", Platform: "script", UniqueID: cfg.ID, SuggestedObjectID: cfg.ID, OriginalName: cfg.Alias})
		if err != nil {
			e.log.Error("script registration failed", "id", cfg.ID, "err", err)
			continue
		}
		s := &script{e: e, cfg: cfg, entityID: entry.EntityID, runs: newRunSet()}
		if prev, ok := old[entry.EntityID]; ok {
			s.last = prev.last
		}
		e.mu.Lock()
		e.scripts[entry.EntityID] = s
		e.mu.Unlock()
		seen[entry.EntityID] = true
		e.core.RegisterController(entry.EntityID, s)
		// script.<id> as its own service, as in HA.
		id := cfg.ID
		e.core.RegisterService("script", id, func(ctx context.Context, call core.ServiceCall) error {
			vars := map[string]any{}
			for k, v := range call.Data {
				if k != "entity_id" && k != "target" {
					vars[k] = v
				}
			}
			s.start(vars)
			return nil
		}, core.ServiceDesc{Name: cfg.Alias, Description: "Runs the script " + cfg.Alias})
		s.writeState()
	}
	for id := range old {
		if !seen[id] {
			e.core.RemoveEntity(id)
		}
	}
}

func (s *script) writeState() {
	st := core.StateOff
	if s.runs.count() > 0 {
		st = core.StateOn
	}
	s.mu.Lock()
	var last any
	if s.last != nil {
		last = s.last.UTC().Format("2006-01-02T15:04:05.000000-07:00")
	}
	s.mu.Unlock()
	if err := s.e.core.SetState(s.entityID, st, map[string]any{"last_triggered": last, "mode": s.cfg.Mode, "current": s.runs.count()}, core.Context{}); err != nil {
		s.e.log.Error("script state write failed", "entity_id", s.entityID, "err", err)
	}
}

func (s *script) start(variables map[string]any) {
	vars := map[string]any{}
	for k, v := range s.cfg.Variables {
		r, err := s.e.renderValue(v, vars)
		if err != nil {
			s.e.log.Error("script variables failed", "entity_id", s.entityID, "err", err)
			return
		}
		vars[k] = r
	}
	for k, v := range variables {
		vars[k] = v
	}
	s.e.launch(s.entityID, s.cfg.Mode, s.cfg.Max, s.runs, func(ctx context.Context) {
		now := s.e.now()
		s.mu.Lock()
		s.last = &now
		s.mu.Unlock()
		s.writeState()
		rc := &runCtx{ctx: ctx, e: s.e, vars: vars, name: s.entityID, cctx: core.NewContext()}
		if err := s.e.runSequence(rc, s.cfg.Sequence); err != nil && !errors.Is(err, errStop) && !errors.Is(err, context.Canceled) {
			s.e.log.Error("script failed", "entity_id", s.entityID, "err", err)
		}
		s.writeState()
	})
}

func (s *script) TurnOn(_ context.Context, data map[string]any) error {
	vars, _ := data["variables"].(map[string]any)
	s.start(vars)
	return nil
}

func (s *script) TurnOff(_ context.Context, _ map[string]any) error {
	s.runs.cancelAll()
	s.writeState()
	return nil
}
