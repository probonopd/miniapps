/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

const platform = "mqtt"

type availTopic struct {
	topic     string
	available string
	notAvail  string
	tpl       *template.Template
}

type availability struct {
	topics []availTopic
	mode   string // all, any, latest
	status map[string]bool
	latest bool
}

func (a *availability) available() bool {
	if a == nil {
		return true
	}
	switch a.mode {
	case "any":
		for _, ok := range a.status {
			if ok {
				return true
			}
		}
		return false
	case "latest":
		return a.latest
	}
	if len(a.status) < len(a.topics) {
		return false
	}
	for _, ok := range a.status {
		if !ok {
			return false
		}
	}
	return true
}

// entity is the part shared by every MQTT component: registry entries,
// availability, JSON attributes, templating and the state write.
type entity struct {
	d        *Discovery
	id       string
	domain   string
	cfg      Config
	entityID string
	uniqueID string
	deviceID string

	mu          sync.Mutex
	state       string // "" is unknown
	attrs       func() map[string]any
	jsonAttrs   map[string]any
	avail       *availability
	expired     bool
	expireTimer *time.Timer
	unsubs      []func()
	qos         byte
	retain      bool
	onAvailable []func() // run when availability turns to online
}

func newEntity(d *Discovery, id, domain string, cfg Config) (*entity, error) {
	e := &entity{d: d, id: id, domain: domain, cfg: cfg, qos: byte(cfg.Int("qos", 0)), retain: cfg.Bool("retain", false)}
	e.attrs = func() map[string]any { return nil }
	var devID string
	if dev := cfg.Map("device"); dev != nil {
		info, err := deviceInfo(dev, d.opts.Platform)
		if err != nil {
			return nil, err
		}
		reg, err := d.core.RegisterDevice(info)
		if err != nil {
			return nil, err
		}
		devID = reg.ID
	}
	e.uniqueID = cfg.Str("unique_id")
	if e.uniqueID == "" {
		// HA allows entities without unique_id but they stay out of the
		// registry; we key them by their discovery id instead.
		e.uniqueID = "discovery:" + id
	}
	name, hasName := cfg["name"]
	original := coerce(name)
	if !hasName {
		original = "MQTT " + strings.ReplaceAll(domain, "_", " ")
	}
	if hasName && name == nil {
		original = ""
	}
	if dev := cfg.Map("device"); dev != nil && original != "" {
		// HA strips a device-name prefix from the entity name (and treats
		// an entity named exactly like its device as unnamed).
		if dn := dev.Str("name"); dn != "" && strings.HasPrefix(original, dn) {
			original = strings.TrimLeft(original[len(dn):], " ")
		}
	}
	suggested := cfg.Str("object_id")
	if suggested == "" {
		suggested = cfg.Str("default_entity_id")
		if i := strings.Index(suggested, "."); i >= 0 {
			suggested = suggested[i+1:]
		}
	}
	entry, err := d.core.RegisterEntity(core.EntityRegistration{
		Domain: domain, Platform: d.opts.Platform, UniqueID: e.uniqueID, SuggestedObjectID: suggested,
		OriginalName: original, HasEntityName: true, DeviceID: devID, OriginalIcon: cfg.Str("icon"),
		EntityCategory: cfg.Str("entity_category"), DeviceClass: cfg.Str("device_class"),
		Unit: cfg.Str("unit_of_measurement"), DisabledByDefault: !cfg.Bool("enabled_by_default", true),
	})
	if err != nil {
		return nil, err
	}
	e.entityID = entry.EntityID
	e.deviceID = devID
	if err := e.setupAvailability(); err != nil {
		return nil, err
	}
	if err := e.setupJSONAttributes(); err != nil {
		return nil, err
	}
	return e, nil
}

func deviceInfo(dev Config, platform string) (core.DeviceInfo, error) {
	info := core.DeviceInfo{
		Manufacturer: dev.Str("manufacturer"), Model: dev.Str("model"), ModelID: dev.Str("model_id"),
		Name: dev.Str("name"), SWVersion: dev.Str("sw_version"), HWVersion: dev.Str("hw_version"),
		SerialNumber: dev.Str("serial_number"), SuggestedArea: dev.Str("suggested_area"),
		ConfigurationURL: dev.Str("configuration_url"),
	}
	for _, id := range dev.Strings("identifiers") {
		info.Identifiers = append(info.Identifiers, [2]string{platform, id})
	}
	if conns, ok := dev["connections"].([]any); ok {
		for _, c := range conns {
			pair, ok := c.([]any)
			if !ok || len(pair) != 2 {
				return info, fmt.Errorf("device connections must be [type, id] pairs")
			}
			info.Connections = append(info.Connections, [2]string{coerce(pair[0]), coerce(pair[1])})
		}
	}
	if via := dev.Str("via_device"); via != "" {
		info.ViaDevice = [2]string{platform, via}
	}
	if len(info.Identifiers) == 0 && len(info.Connections) == 0 {
		return info, fmt.Errorf("device needs identifiers or connections")
	}
	return info, nil
}

func (e *entity) setupAvailability() error {
	cfg := e.cfg
	var topics []availTopic
	if cfg.Has("availability_topic") && cfg.Has("availability") {
		return fmt.Errorf("availability_topic and availability are mutually exclusive")
	}
	if cfg.Has("availability_topic") {
		tpl, err := cfg.Template("availability_template")
		if err != nil {
			return err
		}
		topics = append(topics, availTopic{cfg.Str("availability_topic"), cfg.StrDef("payload_available", "online"), cfg.StrDef("payload_not_available", "offline"), tpl})
	}
	if list, ok := cfg["availability"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("availability entries must be objects")
			}
			ac := Config(m)
			if !ac.Has("topic") {
				return fmt.Errorf("availability entry without topic")
			}
			tpl, err := ac.Template("value_template")
			if err != nil {
				return err
			}
			topics = append(topics, availTopic{ac.Str("topic"), ac.StrDef("payload_available", "online"), ac.StrDef("payload_not_available", "offline"), tpl})
		}
	}
	if len(topics) == 0 {
		return nil
	}
	mode := cfg.StrDef("availability_mode", "latest")
	if mode != "all" && mode != "any" && mode != "latest" {
		return fmt.Errorf("invalid availability_mode %q", mode)
	}
	e.avail = &availability{topics: topics, mode: mode, status: map[string]bool{}}
	for _, at := range topics {
		at := at
		e.subscribe(at.topic, func(m Message) {
			payload := string(m.Payload)
			if at.tpl != nil {
				var err error
				payload, err = e.render(at.tpl, m.Payload)
				if err != nil {
					e.d.log.Warn("availability template failed", "entity_id", e.entityID, "err", err)
					return
				}
			}
			e.mu.Lock()
			was := e.avail.available()
			switch payload {
			case at.available:
				e.avail.status[at.topic] = true
				e.avail.latest = true
			case at.notAvail:
				e.avail.status[at.topic] = false
				e.avail.latest = false
			default:
				e.mu.Unlock()
				e.d.log.Warn("unexpected availability payload", "entity_id", e.entityID, "payload", payload)
				return
			}
			now := e.avail.available()
			hooks := e.onAvailable
			e.mu.Unlock()
			e.write(false)
			if now && !was {
				for _, h := range hooks {
					h()
				}
			}
		})
	}
	return nil
}

func (e *entity) setupJSONAttributes() error {
	if !e.cfg.Has("json_attributes_topic") {
		return nil
	}
	tpl, err := e.cfg.Template("json_attributes_template")
	if err != nil {
		return err
	}
	e.subscribe(e.cfg.Str("json_attributes_topic"), func(m Message) {
		payload := m.Payload
		if tpl != nil {
			s, err := e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("json_attributes_template failed", "entity_id", e.entityID, "err", err)
				return
			}
			payload = []byte(s)
		}
		var attrs map[string]any
		if err := json.Unmarshal(payload, &attrs); err != nil {
			e.d.log.Warn("JSON attributes payload is not an object", "entity_id", e.entityID)
			return
		}
		e.mu.Lock()
		e.jsonAttrs = attrs
		e.mu.Unlock()
		e.write(false)
	})
	return nil
}

func (e *entity) subscribe(topic string, cb func(Message)) {
	unsub, err := e.d.tr.Subscribe(topic, cb)
	if err != nil {
		e.d.log.Error("subscribe failed", "topic", topic, "err", err)
		return
	}
	e.unsubs = append(e.unsubs, unsub)
}

func (e *entity) publish(topic string, payload string) error {
	return e.d.tr.Publish(topic, []byte(payload), e.qos, e.retain)
}

// render evaluates a template with HA's MQTT variables.
func (e *entity) render(tpl *template.Template, payload []byte) (string, error) {
	return tpl.RenderWithPossibleJSON(string(payload), payload, map[string]any{"entity_id": e.entityID, "name": e.d.core.FriendlyName(e.entityID)})
}

// renderCommand evaluates a command_template with "value" set.
func (e *entity) renderCommand(tpl *template.Template, value any) (string, error) {
	return tpl.Render(map[string]any{"value": value, "entity_id": e.entityID, "name": e.d.core.FriendlyName(e.entityID)}, nil)
}

// startExpire arms the expire_after timer that marks the entity
// unavailable when no update arrives in time.
func (e *entity) startExpire() {
	secs := e.cfg.Float("expire_after", 0)
	if secs <= 0 {
		return
	}
	e.mu.Lock()
	if e.expireTimer != nil {
		e.expireTimer.Stop()
	}
	e.expired = false
	e.expireTimer = time.AfterFunc(time.Duration(secs*float64(time.Second)), func() {
		e.mu.Lock()
		e.expired = true
		e.mu.Unlock()
		e.write(false)
	})
	e.mu.Unlock()
}

// setState records a new state and writes it; called by components.
func (e *entity) setState(state string, ctx context.Context) {
	e.mu.Lock()
	e.state = state
	e.mu.Unlock()
	e.write(false, ctx)
}

// write pushes the entity's view to the core: unavailable wins over
// everything, "" becomes unknown, attributes are JSON attributes plus the
// component's own.
func (e *entity) write(force bool, ctxs ...context.Context) {
	e.mu.Lock()
	state := e.state
	if state == "" {
		state = core.StateUnknown
	}
	if !e.avail.available() || e.expired {
		state = core.StateUnavailable
	}
	attrs := map[string]any{}
	for k, v := range e.jsonAttrs {
		attrs[k] = v
	}
	for k, v := range e.attrs() {
		attrs[k] = v
	}
	if p := e.cfg.Str("entity_picture"); p != "" {
		attrs["entity_picture"] = p
	}
	e.mu.Unlock()
	cctx := core.Context{}
	if len(ctxs) > 0 {
		cctx = core.ContextFrom(ctxs[0])
	}
	var err error
	if force {
		err = e.d.core.SetStateForce(e.entityID, state, attrs, cctx)
	} else {
		err = e.d.core.SetState(e.entityID, state, attrs, cctx)
	}
	if err != nil {
		e.d.log.Error("state write failed", "entity_id", e.entityID, "err", err)
	}
}

// inherit carries the runtime state of a replaced entity over to its
// reconfigured successor, so a config update does not flap the state.
func (e *entity) inherit(old *entity) {
	old.mu.Lock()
	defer old.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = old.state
	e.jsonAttrs = old.jsonAttrs
	e.expired = old.expired
	if e.avail != nil && old.avail != nil {
		for _, t := range e.avail.topics {
			if v, ok := old.avail.status[t.topic]; ok {
				e.avail.status[t.topic] = v
			}
		}
		e.avail.latest = old.avail.latest
	}
}

func (e *entity) stop() {
	e.mu.Lock()
	if e.expireTimer != nil {
		e.expireTimer.Stop()
	}
	unsubs := e.unsubs
	e.unsubs = nil
	e.mu.Unlock()
	for _, u := range unsubs {
		u()
	}
	e.d.core.UnregisterController(e.entityID)
}

// remove is stop plus deletion from the registry, for discovery removal.
func (e *entity) remove() {
	e.stop()
	e.d.core.RemoveEntity(e.entityID)
	if e.deviceID != "" {
		e.d.core.RemoveDeviceIfOrphaned(e.deviceID)
	}
}
