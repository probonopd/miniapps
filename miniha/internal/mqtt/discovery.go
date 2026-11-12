/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// Will is a birth or last-will message.
type Will struct {
	Topic   string
	Payload string
	QoS     byte
	Retain  bool
}

type Options struct {
	Prefix   string
	Birth    *Will
	Will     *Will
	Tasmota  bool   // also consume Tasmota native discovery
	Platform string // registry namespace, "mqtt" by default; "mqtt/<name>" per extra broker
}

type component interface {
	stop()
}

type constructor func(e *entity) (component, error)

var components = map[string]constructor{}

// Discovery subscribes to the discovery prefix and turns config payloads
// into entities, following HA's rules for topic layout, removal and the
// device-based payload format.
type Discovery struct {
	core *core.Core
	tr   Transport
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	items    map[string]*item // discovery id -> entity
	unsubs   []func()
	started  bool
	kickMu   sync.Mutex
	kicked   map[string]time.Time // Tasmota state requests, rate limited
	kickSubs map[string]func()    // per-device RESULT subscriptions
	now      func() time.Time
}

type item struct {
	ent  *entity
	comp component
}

func NewDiscovery(c *core.Core, tr Transport, opts Options, log *slog.Logger) *Discovery {
	if opts.Prefix == "" {
		opts.Prefix = "homeassistant"
	}
	if opts.Platform == "" {
		opts.Platform = platform
	}
	return &Discovery{core: c, tr: tr, opts: opts, log: log, items: map[string]*item{}, kicked: map[string]time.Time{}, kickSubs: map[string]func(){}, now: time.Now}
}

func (d *Discovery) Start() error {
	for _, f := range []string{d.opts.Prefix + "/+/+/config", d.opts.Prefix + "/+/+/+/config"} {
		unsub, err := d.tr.Subscribe(f, d.onConfig)
		if err != nil {
			return err
		}
		d.unsubs = append(d.unsubs, unsub)
	}
	if d.opts.Tasmota {
		unsub, err := d.tr.Subscribe(tasmotaDiscoveryTopic, d.onTasmotaConfig)
		if err != nil {
			return err
		}
		d.unsubs = append(d.unsubs, unsub)
	}
	d.started = true
	// Devices resend their discovery payloads when they see the birth
	// message, so it goes out after the subscriptions are in place.
	return d.PublishBirth()
}

// PublishBirth sends the birth message; also used after a reconnect to
// an external broker.
func (d *Discovery) PublishBirth() error {
	if b := d.opts.Birth; b != nil && d.started {
		return d.tr.Publish(b.Topic, []byte(b.Payload), b.QoS, b.Retain)
	}
	return nil
}

func (d *Discovery) Stop() {
	if w := d.opts.Will; w != nil && d.started {
		d.tr.Publish(w.Topic, []byte(w.Payload), w.QoS, w.Retain)
	}
	for _, u := range d.unsubs {
		u()
	}
	d.kickMu.Lock()
	for _, u := range d.kickSubs {
		u()
	}
	d.kickSubs = map[string]func(){}
	d.kickMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, it := range d.items {
		it.comp.stop()
		it.ent.stop()
		delete(d.items, id)
	}
}

func (d *Discovery) onConfig(m Message) {
	parts := strings.Split(m.Topic, "/")
	var comp, node, object string
	switch len(parts) {
	case 4:
		comp, object = parts[1], parts[2]
	case 5:
		comp, node, object = parts[1], parts[2], parts[3]
	default:
		return
	}
	if comp == "device" {
		d.onDeviceConfig(node, object, m.Payload)
		return
	}
	id := comp + "/" + node + "/" + object
	if len(strings.TrimSpace(string(m.Payload))) == 0 {
		d.remove(id)
		return
	}
	cfg, err := parseConfig(m.Payload)
	if err != nil {
		d.log.Error("discovery: bad payload", "topic", m.Topic, "err", err)
		return
	}
	d.apply(comp, id, cfg)
}

// onDeviceConfig handles the device-based format: one payload describes
// a device and all its components, which share the top-level options.
func (d *Discovery) onDeviceConfig(node, object string, payload []byte) {
	prefix := "device/" + node + "/" + object + "/"
	if len(strings.TrimSpace(string(payload))) == 0 {
		d.mu.Lock()
		var ids []string
		for id := range d.items {
			if strings.HasPrefix(id, prefix) {
				ids = append(ids, id)
			}
		}
		d.mu.Unlock()
		for _, id := range ids {
			d.remove(id)
		}
		return
	}
	cfg, err := parseConfig(payload)
	if err != nil {
		d.log.Error("discovery: bad device payload", "node", node, "object", object, "err", err)
		return
	}
	comps, ok := cfg["components"].(map[string]any)
	if !ok {
		d.log.Error("discovery: device payload without components", "object", object)
		return
	}
	shared := Config{}
	for k, v := range cfg {
		if k != "components" {
			shared[k] = v
		}
	}
	for compID, raw := range comps {
		id := prefix + compID
		m, ok := raw.(map[string]any)
		if !ok || len(m) == 0 {
			d.remove(id)
			continue
		}
		ccfg := Config{}
		for k, v := range shared {
			ccfg[k] = v
		}
		for k, v := range expand(m) {
			ccfg[k] = v
		}
		platform := ccfg.Str("platform")
		if platform == "" {
			d.log.Error("discovery: component without platform", "id", id)
			continue
		}
		d.apply(platform, id, ccfg)
	}
}

func (d *Discovery) apply(comp, id string, cfg Config) {
	ctor, ok := components[comp]
	if !ok {
		d.log.Warn("discovery: unsupported component", "component", comp, "id", id)
		return
	}
	d.install(id, comp, cfg, ctor)
}

// install creates (or replaces) the entity behind a discovery id.
func (d *Discovery) install(id, comp string, cfg Config, ctor constructor) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var old *entity
	if it := d.items[id]; it != nil {
		// Reconfiguration: tear down and rebuild. The registry keeps the
		// entity_id (same unique_id) and the new entity inherits the state.
		it.comp.stop()
		it.ent.stop()
		delete(d.items, id)
		old = it.ent
	}
	ent, err := newEntity(d, id, comp, cfg)
	if err != nil {
		d.log.Error("discovery: invalid config", "id", id, "err", err)
		return
	}
	if old != nil {
		ent.inherit(old)
	}
	c, err := ctor(ent)
	if err != nil {
		ent.stop()
		d.log.Error("discovery: invalid config", "id", id, "err", err)
		return
	}
	d.items[id] = &item{ent, c}
	d.log.Info("discovery: entity configured", "entity_id", ent.entityID, "id", id)
	d.tasmotaKick(ent)
}

func (d *Discovery) remove(id string) {
	d.mu.Lock()
	it := d.items[id]
	delete(d.items, id)
	d.mu.Unlock()
	if it == nil {
		return
	}
	it.comp.stop()
	it.ent.remove()
	d.log.Info("discovery: entity removed", "entity_id", it.ent.entityID, "id", id)
}

// Count reports configured entities, for diagnostics.
func (d *Discovery) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

func errMissing(key string) error { return fmt.Errorf("required option %q is missing", key) }
