/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package virtual

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"github.com/probonopd/miniha/internal/core"
)

const groupPlatform = "group"

// platformDomains lists the domains with a "platform: group" entity, in
// the order their entity ids are allocated.
var platformDomains = []string{"light", "switch", "binary_sensor", "sensor", "cover", "fan"}

// computeFn derives a group's state and attributes from its members'
// states; a nil entry is a member that has no state at all.
type computeFn func(states []*core.State) (state string, attrs map[string]any)

// Groups holds HA's old-style group: entities and the platform: group
// entities of light, switch, binary_sensor, sensor, cover and fan. Each
// one re-evaluates synchronously whenever a member's state changes.
type Groups struct {
	c   *core.Core
	log *slog.Logger

	ents     []*groupEntity
	byMember map[string][]*groupEntity
	unsub    func()

	// OnReload, when set by the owner, runs for group.reload (the owner
	// knows how to re-read the configuration). Without it the service
	// only re-evaluates every group.
	OnReload func(ctx context.Context) error
}

type groupEntity struct {
	g        *Groups
	entityID string
	members  []string
	base     map[string]any // attributes that do not depend on the members
	compute  computeFn
	reg      *core.EntityRegistration // nil for old-style groups: HA keeps them out of the registry
	ctl      any

	fl        flight
	mu        sync.Mutex
	written   bool
	lastState string
	lastAttrs map[string]any
}

// NewGroups validates the configuration and registers every group.
// Entries of platforms other than "group" are left to other packages.
func NewGroups(c *core.Core, old map[string]any, platforms map[string][]map[string]any, log *slog.Logger) (*Groups, error) {
	if log == nil {
		log = slog.Default()
	}
	g := &Groups{c: c, log: log, byMember: map[string][]*groupEntity{}}

	ids := make([]string, 0, len(old))
	for id := range old {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for order, id := range ids {
		e, err := g.newOld(id, old[id], order)
		if err != nil {
			return nil, fmt.Errorf("group.%s: %w", id, err)
		}
		g.ents = append(g.ents, e)
	}

	known := map[string]bool{}
	for _, d := range platformDomains {
		known[d] = true
	}
	var extra []string
	for d := range platforms {
		if !known[d] {
			extra = append(extra, d)
		}
	}
	sort.Strings(extra)
	for _, d := range append(append([]string{}, platformDomains...), extra...) {
		for i, m := range platforms[d] {
			if p, _ := m["platform"].(string); p != groupPlatform {
				continue
			}
			e, err := g.newPlatform(d, m)
			if err != nil {
				return nil, fmt.Errorf("%s[%d] (platform group): %w", d, i, err)
			}
			g.ents = append(g.ents, e)
		}
	}

	seen := map[string]bool{}
	for _, e := range g.ents {
		if e.reg != nil {
			if seen[e.reg.UniqueID] {
				return nil, fmt.Errorf("duplicate group unique_id %q", e.reg.UniqueID)
			}
			seen[e.reg.UniqueID] = true
			entry, err := c.RegisterEntity(*e.reg)
			if err != nil {
				return nil, err
			}
			e.entityID = entry.EntityID
		}
		for _, m := range e.members {
			if m == e.entityID {
				return nil, fmt.Errorf("%s contains itself", e.entityID)
			}
			g.byMember[m] = append(g.byMember[m], e)
		}
		if e.ctl != nil {
			c.RegisterController(e.entityID, e.ctl)
		}
	}

	c.RegisterOnOffDomain("group")
	c.RegisterService("group", "reload", g.reload, core.ServiceDesc{Name: "Reload", Description: "Reloads group configuration"})

	g.unsub = c.Subscribe("state_changed", g.onChange)
	for _, e := range g.ents {
		e.update(core.Context{})
	}
	return g, nil
}

// Stop unsubscribes from the core; the groups keep their last state.
func (g *Groups) Stop() {
	if g.unsub != nil {
		g.unsub()
		g.unsub = nil
	}
}

func (g *Groups) reload(ctx context.Context, _ core.ServiceCall) error {
	if g.OnReload != nil {
		return g.OnReload(ctx)
	}
	for _, e := range g.ents {
		e.update(core.ContextFrom(ctx))
	}
	return nil
}

func (g *Groups) onChange(ev core.Event) {
	id, _ := ev.Data["entity_id"].(string)
	for _, e := range g.byMember[id] {
		e.update(ev.Context)
	}
}

// update recomputes the group and writes it when it changed. The write
// happens outside the lock because state_changed subscribers run
// synchronously and may re-enter the group.
func (e *groupEntity) update(ctx core.Context) {
	e.fl.do(ctx, func(ctx core.Context) {
		states := make([]*core.State, len(e.members))
		for i, id := range e.members {
			states[i] = e.g.c.GetState(id)
		}
		state, attrs := e.compute(states)
		if attrs == nil {
			attrs = map[string]any{}
		}
		for k, v := range e.base {
			attrs[k] = v
		}
		attrs["entity_id"] = anyList(e.members)
		e.mu.Lock()
		if e.written && e.lastState == state && reflect.DeepEqual(e.lastAttrs, attrs) {
			e.mu.Unlock()
			return
		}
		e.lastState, e.lastAttrs, e.written = state, attrs, true
		e.mu.Unlock()
		if err := e.g.c.SetState(e.entityID, state, attrs, ctx); err != nil {
			e.g.log.Error("group state rejected", "entity_id", e.entityID, "err", err)
			e.mu.Lock()
			e.written = false
			e.mu.Unlock()
		}
	})
}

// forward sends a service call to every member in place of the group,
// keeping the caller's service data (brightness, ...) and context.
func (e *groupEntity) forward(ctx context.Context, domain, service string, data map[string]any) error {
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		switch k {
		case "entity_id", "target", "device_id", "area_id":
			continue
		}
		out[k] = v
	}
	out["entity_id"] = anyList(e.members)
	_, err := e.g.c.CallService(ctx, domain, service, out, core.ContextFrom(ctx))
	return err
}

// --- old-style groups ---

type groupDomain struct {
	on      map[string]bool
	onName  string
	offName string
}

func stateSet(ss ...string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

var defaultGroupDomain = groupDomain{on: stateSet(core.StateOn), onName: core.StateOn, offName: core.StateOff}

// groupDomains holds the per-domain meaning of "on" that HA's domain
// group registrations define.
var groupDomains = map[string]groupDomain{
	"device_tracker": {stateSet("home"), "home", "not_home"},
	"person":         {stateSet("home"), "home", "not_home"},
	"cover":          {stateSet("open", "opening", "closing"), "open", "closed"},
	"lock":           {stateSet("unlocked", "unlocking", "open", "opening", "jammed"), "unlocked", "locked"},
	"media_player":   {stateSet("on", "playing", "paused", "buffering", "idle"), core.StateOn, core.StateOff},
	"plant":          {stateSet("problem"), "problem", "ok"},
}

func groupDomainOf(entityID string) groupDomain {
	d, _ := core.SplitEntityID(entityID)
	if gd, ok := groupDomains[d]; ok {
		return gd
	}
	return defaultGroupDomain
}

var oldGroupKeys = []string{"name", "entities", "icon", "all"}

func (g *Groups) newOld(id string, raw any, order int) (*groupEntity, error) {
	entityID := "group." + id
	if !core.ValidEntityID(entityID) {
		return nil, fmt.Errorf("invalid group id")
	}
	var m map[string]any
	switch x := raw.(type) {
	case map[string]any, map[any]any:
		m, _ = asMap(x)
	case []any, string:
		// Shorthand: the value is just the member list.
		m = map[string]any{"entities": x}
	default:
		return nil, fmt.Errorf("must be a mapping or an entity list")
	}
	if err := checkKeys(m, oldGroupKeys...); err != nil {
		return nil, err
	}
	members, err := entityList(m["entities"])
	if err != nil {
		return nil, err
	}
	name, err := strOpt(m, "name")
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = id
	}
	icon, err := strOpt(m, "icon")
	if err != nil {
		return nil, err
	}
	all, err := boolOpt(m, "all")
	if err != nil {
		return nil, err
	}
	base := map[string]any{"friendly_name": name, "order": order}
	if icon != "" {
		base["icon"] = icon
	}
	if all {
		base["all"] = true
	}
	e := &groupEntity{g: g, entityID: entityID, members: members, base: base}
	e.ctl = onOffCtl{e, "homeassistant"}
	e.compute = func(states []*core.State) (string, map[string]any) {
		on, special := verdict(states, all, func(s *core.State) bool {
			return groupDomainOf(s.EntityID).on[s.State]
		})
		if special != "" {
			return special, nil
		}
		pair := defaultGroupDomain
		if uniformDomain(members) {
			pair = groupDomainOf(members[0])
		}
		if on {
			return pair.onName, nil
		}
		return pair.offName, nil
	}
	return e, nil
}

func uniformDomain(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	d0, _ := core.SplitEntityID(ids[0])
	for _, id := range ids[1:] {
		if d, _ := core.SplitEntityID(id); d != d0 {
			return false
		}
	}
	return true
}

// verdict classifies the members: special is unknown or unavailable when
// no member has a usable state, otherwise on says whether the group is on.
// Unknown and unavailable members do not vote, as in HA.
func verdict(states []*core.State, all bool, isOn func(*core.State) bool) (on bool, special string) {
	var known []*core.State
	unavailable := 0
	for _, s := range states {
		switch {
		case s == nil || s.State == core.StateUnavailable:
			unavailable++
		case s.State == core.StateUnknown:
		default:
			known = append(known, s)
		}
	}
	if len(known) == 0 {
		if len(states) > 0 && unavailable == len(states) {
			return false, core.StateUnavailable
		}
		return false, core.StateUnknown
	}
	for _, s := range known {
		if isOn(s) != all {
			return !all, ""
		}
	}
	return all, ""
}

// --- controllers ---

// onOffCtl turns the group on or off by forwarding to its members.
type onOffCtl struct {
	e      *groupEntity
	domain string
}

func (c onOffCtl) TurnOn(ctx context.Context, data map[string]any) error {
	return c.e.forward(ctx, c.domain, "turn_on", data)
}

func (c onOffCtl) TurnOff(ctx context.Context, data map[string]any) error {
	return c.e.forward(ctx, c.domain, "turn_off", data)
}

type coverCtl struct{ e *groupEntity }

func (c coverCtl) OpenCover(ctx context.Context) error {
	return c.e.forward(ctx, "cover", "open_cover", nil)
}

func (c coverCtl) CloseCover(ctx context.Context) error {
	return c.e.forward(ctx, "cover", "close_cover", nil)
}

func (c coverCtl) StopCover(ctx context.Context) error {
	return c.e.forward(ctx, "cover", "stop_cover", nil)
}

func (c coverCtl) SetCoverPosition(ctx context.Context, position int) error {
	return c.e.forward(ctx, "cover", "set_cover_position", map[string]any{"position": position})
}

// CoverState lets cover.toggle decide between opening and closing.
func (c coverCtl) CoverState() string {
	if st := c.e.g.c.GetState(c.e.entityID); st != nil {
		return st.State
	}
	return core.StateUnknown
}

type fanCtl struct {
	onOffCtl
}

func (c fanCtl) SetPercentage(ctx context.Context, percentage int) error {
	return c.e.forward(ctx, "fan", "set_percentage", map[string]any{"percentage": percentage})
}

// Percentage lets fan.increase_speed and decrease_speed start from the
// group's current speed.
func (c fanCtl) Percentage() int {
	if st := c.e.g.c.GetState(c.e.entityID); st != nil {
		if p, ok := toInt(st.Attributes["percentage"]); ok {
			return p
		}
	}
	return 0
}
