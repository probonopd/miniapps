/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package location

import (
	"fmt"
	"strings"

	"github.com/probonopd/miniha/internal/core"
)

// Persons owns the person.* entities.
type Persons struct {
	c       *core.Core
	persons []*person
	unsub   func()
}

type person struct {
	entityID string
	id       string
	name     string
	picture  string
	trackers []string
}

// NewPersons creates one person entity per config entry and keeps its state
// in step with its device trackers.
func NewPersons(c *core.Core, cfg []map[string]any) (*Persons, error) {
	p := &Persons{c: c}
	seen := map[string]bool{}
	byTracker := map[string]bool{}
	for i, entry := range cfg {
		pe, err := parsePerson(entry)
		if err != nil {
			return nil, fmt.Errorf("person entry %d: %w", i+1, err)
		}
		if seen[pe.entityID] {
			return nil, fmt.Errorf("person entry %d: %s is defined twice", i+1, pe.entityID)
		}
		seen[pe.entityID] = true
		for _, t := range pe.trackers {
			byTracker[t] = true
		}
		p.persons = append(p.persons, pe)
	}
	p.unsub = c.Subscribe("state_changed", func(ev core.Event) {
		id, _ := ev.Data["entity_id"].(string)
		if !byTracker[id] {
			return
		}
		for _, pe := range p.persons {
			for _, t := range pe.trackers {
				if t == id {
					p.update(pe)
					break
				}
			}
		}
	})
	for _, pe := range p.persons {
		p.update(pe)
	}
	return p, nil
}

func parsePerson(entry map[string]any) (*person, error) {
	name, _ := entry["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	slug := core.Slugify(name)
	if slug == "" {
		return nil, fmt.Errorf("name %q has no usable characters for an entity id", name)
	}
	pe := &person{entityID: "person." + slug, id: slug, name: name}
	if v, ok := entry["id"]; ok {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s: id must be a non-empty string", name)
		}
		pe.id = s
	}
	if v, ok := entry["picture"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: picture must be a string", name)
		}
		pe.picture = s
	}
	if v, ok := entry["device_trackers"]; ok {
		switch l := v.(type) {
		case []any:
			for _, item := range l {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("%s: device_trackers entries must be strings", name)
				}
				pe.trackers = append(pe.trackers, s)
			}
		case []string:
			pe.trackers = append(pe.trackers, l...)
		case string:
			pe.trackers = append(pe.trackers, l)
		default:
			return nil, fmt.Errorf("%s: device_trackers must be a list of entity ids", name)
		}
	}
	for _, t := range pe.trackers {
		if !core.ValidEntityID(t) || !strings.HasPrefix(t, "device_tracker.") {
			return nil, fmt.Errorf("%s: %q is not a device_tracker entity id", name, t)
		}
	}
	return pe, nil
}

// update applies HA's source selection. Unknown and unavailable trackers
// are ignored. A GPS tracker is preferred because its coordinates carry
// the zone and accuracy; without one, a router/bluetooth tracker that says
// home wins, else the most recent remaining tracker decides.
func (p *Persons) update(pe *person) {
	var gps, nonGPSHome, other *core.State
	for _, id := range pe.trackers {
		s := p.c.GetState(id)
		if s == nil || s.State == core.StateUnknown || s.State == core.StateUnavailable {
			continue
		}
		switch {
		case s.Attributes["source_type"] == "gps":
			gps = newer(gps, s)
		case s.State == "home":
			nonGPSHome = newer(nonGPSHome, s)
		default:
			other = newer(other, s)
		}
	}
	var src *core.State
	switch {
	// HA: a router or Bluetooth tracker that sees the person at home wins
	// over GPS, which may lag or drift.
	case nonGPSHome != nil:
		src = nonGPSHome
	case gps != nil:
		src = gps
	default:
		src = other
	}
	state := core.StateUnknown
	attrs := map[string]any{
		"editable":        false,
		"id":              pe.id,
		"device_trackers": append([]string{}, pe.trackers...),
		"user_id":         nil,
		"friendly_name":   pe.name,
	}
	if pe.picture != "" {
		attrs["entity_picture"] = pe.picture
	}
	if src != nil {
		state = src.State
		attrs["source"] = src.EntityID
		for _, k := range []string{"latitude", "longitude", "gps_accuracy"} {
			if v, ok := src.Attributes[k]; ok {
				attrs[k] = v
			}
		}
	}
	if err := p.c.SetState(pe.entityID, state, attrs, core.Context{}); err != nil {
		panic(fmt.Sprintf("location: writing %s: %v", pe.entityID, err))
	}
}

func newer(a, b *core.State) *core.State {
	if a == nil || b.LastUpdated.After(a.LastUpdated) {
		return b
	}
	return a
}

// Stop detaches the persons from the event bus; the entities stay.
func (p *Persons) Stop() {
	p.unsub()
}
