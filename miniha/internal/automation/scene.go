/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"context"
	"errors"
	"fmt"

	"github.com/probonopd/miniha/internal/core"
)

type scene struct {
	e        *Engine
	cfg      sceneConfig
	entityID string
}

func (e *Engine) applyScenes(cfgs []sceneConfig) {
	e.mu.Lock()
	old := e.scenes
	e.scenes = map[string]*scene{}
	e.mu.Unlock()
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		uniq := cfg.ID
		if uniq == "" {
			uniq = "name:" + cfg.Name
		}
		entry, err := e.core.RegisterEntity(core.EntityRegistration{Domain: "scene", Platform: "scene", UniqueID: uniq, OriginalName: cfg.Name})
		if err != nil {
			e.log.Error("scene registration failed", "name", cfg.Name, "err", err)
			continue
		}
		s := &scene{e: e, cfg: cfg, entityID: entry.EntityID}
		e.mu.Lock()
		e.scenes[entry.EntityID] = s
		e.mu.Unlock()
		seen[entry.EntityID] = true
		e.core.RegisterController(entry.EntityID, s)
		if e.core.GetState(entry.EntityID) == nil {
			e.core.SetState(entry.EntityID, core.StateUnknown, map[string]any{"entity_id": entityList(cfg)}, core.Context{})
		}
	}
	for id := range old {
		if !seen[id] {
			e.core.RemoveEntity(id)
		}
	}
}

func entityList(cfg sceneConfig) []string {
	out := make([]string, 0, len(cfg.Entities))
	for id := range cfg.Entities {
		out = append(out, id)
	}
	return out
}

// activate reproduces each entity's state the way HA's reproduce_state
// does: on/off through the domain's turn_on/turn_off with the
// attributes as service data, values through set_value and friends.
func (s *scene) activate(ctx context.Context, data map[string]any) error {
	var errs []error
	for id, spec := range s.cfg.Entities {
		domain, _ := core.SplitEntityID(id)
		state := ""
		attrs := map[string]any{}
		switch x := spec.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "state" {
					state = str(v)
				} else {
					attrs[k] = v
				}
			}
		default:
			state = str(spec)
		}
		call := map[string]any{"entity_id": id}
		if t, ok := data["transition"]; ok && (domain == "light") {
			call["transition"] = t
		}
		var svcDomain, svc string
		switch domain {
		case "light", "switch", "fan", "input_boolean", "automation", "humidifier", "siren":
			svcDomain = domain
			switch state {
			case "on":
				svc = "turn_on"
				for k, v := range attrs {
					call[k] = v
				}
			case "off":
				svc = "turn_off"
			default:
				errs = append(errs, fmt.Errorf("%s: scene state must be on or off", id))
				continue
			}
		case "number", "text", "input_number", "input_text":
			svcDomain, svc = domain, "set_value"
			call["value"] = parseResult(state)
		case "select", "input_select":
			svcDomain, svc = domain, "select_option"
			call["option"] = state
		case "cover":
			svcDomain = domain
			if pos, ok := attrs["current_position"]; ok {
				svc = "set_cover_position"
				call["position"] = pos
			} else if state == "open" {
				svc = "open_cover"
			} else {
				svc = "close_cover"
			}
		case "lock":
			svcDomain = domain
			svc = "lock"
			if state == "unlocked" {
				svc = "unlock"
			}
		default:
			svcDomain = "homeassistant"
			svc = "turn_on"
			if state == "off" {
				svc = "turn_off"
			}
		}
		if _, err := s.e.core.CallService(ctx, svcDomain, svc, call, core.ContextFrom(ctx)); err != nil {
			errs = append(errs, err)
		}
	}
	now := s.e.now()
	s.e.core.SetState(s.entityID, now.UTC().Format("2006-01-02T15:04:05.000000-07:00"), map[string]any{"entity_id": entityList(s.cfg)}, core.ContextFrom(ctx))
	return errors.Join(errs...)
}

func (s *scene) TurnOn(ctx context.Context, data map[string]any) error { return s.activate(ctx, data) }
func (s *scene) TurnOff(_ context.Context, _ map[string]any) error     { return nil }
