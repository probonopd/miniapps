/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

// The Exporter is the mirror image of Discovery: instead of turning MQTT
// discovery payloads into core entities it announces core entities as MQTT
// discovery payloads, so another Home Assistant (or MiniHA) subscribed to
// the same broker sees this hub as one device that carries all of its
// switches, lights, sensors and so on, and can control them.
//
// Topic scheme (<ns> is the export namespace, default "miniha/<hub id>"):
//
//	<prefix>/<component>/<hub id>_<entity slug>/config  retained discovery config
//	<ns>/status                                         online / offline, retained
//	<ns>/state/<entity_id>                              state, retained
//	<ns>/attr/<entity_id>                               attributes as JSON, retained
//	<ns>/cmd/<entity_id>                                commands (ON, OFF, PRESS, JSON for lights, ...)
//	<ns>/pos/<entity_id>, <ns>/pct/<entity_id>          cover position, fan percentage commands
//	<ns>/mode/<entity_id>, <ns>/temp/<entity_id>        climate mode and target temperature commands
//	<ns>/{pos_state,pct_state,temp_state,curtemp}/<entity_id>  the matching state topics
//
// Commands are only honored when they are not retained: a retained command
// is replayed by the broker to every new subscriber, so acting on it would
// re-run an old "unlock" after every restart. MQTT 5 message expiry is not
// visible through Transport, so the retained flag is the staleness rule;
// commands that sat in the work queue for longer than CommandMaxAge (a
// service call that blocks for a long time) are dropped as well.
//
// The hub's last will (status "offline") belongs to the broker connection
// and must be configured by whoever creates the Transport; Stop publishes
// "offline" explicitly for a clean shutdown.
//
// Loop hazard: when the export broker is also a broker whose discovery
// prefix MiniHA consumes, the exported configs come back as MQTT entities.
// Use a different DiscoveryPrefix or Exclude the platform's entities then.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// Version is announced as the software version of the exported device and
// of the discovery origin. The main package sets it at startup.
var Version = "dev"

const (
	defaultExportPrefix  = "homeassistant"
	defaultCommandMaxAge = 30 * time.Second

	// A burst of registry updates (every entity registration fires one)
	// is folded into a single resync.
	registryResyncDelay = 200 * time.Millisecond

	// Bounds a service call so that one stuck device cannot freeze the
	// command queue forever.
	commandTimeout = 30 * time.Second

	exportQoS = 1
)

// ExportOptions configures an Exporter. Zero values select the defaults.
type ExportOptions struct {
	Namespace       string
	DiscoveryPrefix string
	Name            string
	Include         []string
	Exclude         []string
	CommandMaxAge   time.Duration
}

// exportComponent maps a core domain to the HA MQTT component that can
// represent it; the input_* helpers are plain switch/number/... on the wire.
var exportComponent = map[string]string{
	"switch":        "switch",
	"input_boolean": "switch",
	"light":         "light",
	"number":        "number",
	"input_number":  "number",
	"select":        "select",
	"input_select":  "select",
	"text":          "text",
	"input_text":    "text",
	"button":        "button",
	"input_button":  "button",
	"binary_sensor": "binary_sensor",
	"sensor":        "sensor",
	"cover":         "cover",
	"lock":          "lock",
	"fan":           "fan",
	"climate":       "climate",
}

// notExported lists domains that make no sense on another hub (they only
// exist to drive local logic or have no MQTT component), so they are
// skipped without a log line.
var notExported = map[string]bool{
	"automation": true, "script": true, "scene": true, "zone": true,
	"person": true, "sun": true, "tag": true, "device_automation": true,
	"camera": true, "image": true, "update": true, "event": true,
}

var hubIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// exportMatcher is one include/exclude entry: an entity id, a domain or a
// glob, the same notation the recorder uses.
type exportMatcher struct {
	kind int // 0 entity id, 1 domain, 2 glob
	pat  string
}

func parseExportMatchers(list []string) ([]exportMatcher, error) {
	var out []exportMatcher
	for _, e := range list {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
			return nil, fmt.Errorf("empty entry")
		case strings.ContainsAny(e, "*?["):
			if _, err := path.Match(e, ""); err != nil {
				return nil, fmt.Errorf("glob %q: %w", e, err)
			}
			out = append(out, exportMatcher{2, e})
		case strings.Contains(e, "."):
			out = append(out, exportMatcher{0, e})
		default:
			out = append(out, exportMatcher{1, e})
		}
	}
	return out, nil
}

func (m exportMatcher) match(entityID string) bool {
	switch m.kind {
	case 0:
		return entityID == m.pat
	case 1:
		return strings.HasPrefix(entityID, m.pat+".")
	}
	ok, _ := path.Match(m.pat, entityID)
	return ok
}

// exportedEntity remembers what was published for one entity, so that its
// retained topics can be cleared when it disappears.
type exportedEntity struct {
	configTopic string
	topics      map[string]bool
}

type queuedCommand struct {
	at  time.Time
	run func(ctx context.Context)
}

// Exporter publishes the entities of a core as one HA device on a broker.
type Exporter struct {
	c      *core.Core
	tr     Transport
	opts   ExportOptions
	hubID  string
	log    *slog.Logger
	device map[string]any
	origin map[string]any

	include, exclude []exportMatcher
	ns               string
	statusTopic      string

	// mu guards the queues; the publishing state below belongs to the
	// output worker alone (and to Start before the worker runs).
	mu       sync.Mutex
	outWake  *sync.Cond
	cmdWake  *sync.Cond
	dirty    []string
	dirtySet map[string]bool
	resync   bool
	outBusy  bool
	cmds     []queuedCommand
	cmdBusy  bool
	running  bool
	debounce *time.Timer
	wg       sync.WaitGroup
	unsubs   []func()

	debounceDelay time.Duration

	exported map[string]*exportedEntity
	last     map[string]string // topic -> last published payload

	skipLogged map[string]bool // guarded by mu
}

// NewExporter prepares an exporter; nothing is published before Start.
func NewExporter(c *core.Core, tr Transport, cfg ExportOptions, hubID string, log *slog.Logger) *Exporter {
	x := &Exporter{
		c: c, tr: tr, opts: cfg, hubID: hubID, log: log,
		dirtySet: map[string]bool{}, debounceDelay: registryResyncDelay,
		exported: map[string]*exportedEntity{}, last: map[string]string{}, skipLogged: map[string]bool{},
	}
	x.outWake = sync.NewCond(&x.mu)
	x.cmdWake = sync.NewCond(&x.mu)
	if x.opts.DiscoveryPrefix == "" {
		x.opts.DiscoveryPrefix = defaultExportPrefix
	}
	if x.opts.Namespace == "" {
		x.opts.Namespace = "miniha/" + hubID
	}
	if x.opts.CommandMaxAge <= 0 {
		x.opts.CommandMaxAge = defaultCommandMaxAge
	}
	x.ns = x.opts.Namespace
	x.statusTopic = x.ns + "/status"
	// The identifiers are plain strings, which is what HA's MQTT device
	// schema accepts.
	x.device = map[string]any{
		"identifiers":  []string{"miniha_" + hubID},
		"name":         x.opts.Name,
		"manufacturer": "MiniHA",
		"model":        "hub",
		"sw_version":   Version,
	}
	x.origin = map[string]any{"name": "MiniHA", "sw": Version}
	return x
}

func (x *Exporter) validate() error {
	if !hubIDPattern.MatchString(x.hubID) {
		return fmt.Errorf("export: hub id %q must be non-empty and consist of letters, digits, _ and -", x.hubID)
	}
	if x.opts.Name == "" {
		return fmt.Errorf("export: device name must be set")
	}
	if strings.ContainsAny(x.ns, "+#") || strings.HasPrefix(x.ns, "/") || strings.HasSuffix(x.ns, "/") {
		return fmt.Errorf("export: namespace %q must not contain wildcards or start or end with a slash", x.ns)
	}
	if strings.ContainsAny(x.opts.DiscoveryPrefix, "+#") || strings.HasSuffix(x.opts.DiscoveryPrefix, "/") {
		return fmt.Errorf("export: discovery prefix %q must not contain wildcards or end with a slash", x.opts.DiscoveryPrefix)
	}
	var err error
	if x.include, err = parseExportMatchers(x.opts.Include); err != nil {
		return fmt.Errorf("export include: %w", err)
	}
	if x.exclude, err = parseExportMatchers(x.opts.Exclude); err != nil {
		return fmt.Errorf("export exclude: %w", err)
	}
	return nil
}

// Start announces the hub, publishes a full snapshot and begins to follow
// the core and the command topics.
func (x *Exporter) Start() error {
	x.mu.Lock()
	if x.running {
		x.mu.Unlock()
		return fmt.Errorf("export: already started")
	}
	x.mu.Unlock()
	if err := x.validate(); err != nil {
		return err
	}
	fail := func(err error) error {
		x.unsubscribeAll()
		return err
	}
	for _, kind := range []string{"cmd", "pos", "pct", "mode", "temp"} {
		kind := kind
		unsub, err := x.tr.Subscribe(x.ns+"/"+kind+"/#", func(m Message) { x.onCommand(kind, m) })
		if err != nil {
			return fail(fmt.Errorf("export: subscribing to %s/%s/#: %w", x.ns, kind, err))
		}
		x.addUnsub(unsub)
	}
	// Subscribe before the snapshot so that a change in between is marked
	// dirty and published once the worker runs.
	x.addUnsub(x.c.Subscribe("state_changed", x.onStateChanged))
	x.addUnsub(x.c.Subscribe("registry_updated", x.onRegistryUpdated))
	if err := x.tr.Publish(x.statusTopic, []byte("online"), exportQoS, true); err != nil {
		return fail(fmt.Errorf("export: publishing %s: %w", x.statusTopic, err))
	}
	if err := x.syncAll(); err != nil {
		return fail(err)
	}
	x.mu.Lock()
	x.running = true
	x.wg.Add(2)
	x.mu.Unlock()
	go x.outLoop()
	go x.cmdLoop()
	return nil
}

// Stop ends the exporter and marks the hub offline. The retained configs
// stay on the broker so the entities reappear when the hub comes back.
func (x *Exporter) Stop() {
	x.mu.Lock()
	if !x.running {
		x.mu.Unlock()
		return
	}
	x.running = false
	if x.debounce != nil {
		x.debounce.Stop()
		x.debounce = nil
	}
	x.outWake.Broadcast()
	x.cmdWake.Broadcast()
	x.mu.Unlock()
	x.unsubscribeAll()
	x.wg.Wait()
	if err := x.tr.Publish(x.statusTopic, []byte("offline"), exportQoS, true); err != nil {
		x.log.Warn("export: publishing offline status failed", "err", err)
	}
	x.exported = map[string]*exportedEntity{}
	x.last = map[string]string{}
}

func (x *Exporter) addUnsub(f func()) {
	x.mu.Lock()
	x.unsubs = append(x.unsubs, f)
	x.mu.Unlock()
}

func (x *Exporter) unsubscribeAll() {
	x.mu.Lock()
	unsubs := x.unsubs
	x.unsubs = nil
	x.mu.Unlock()
	for _, f := range unsubs {
		f()
	}
}

// exportable reports whether the entity id passes the domain rules and the
// include/exclude lists. Exclude wins over include.
func (x *Exporter) exportable(entityID string) bool {
	domain, _ := core.SplitEntityID(entityID)
	if _, ok := exportComponent[domain]; !ok {
		return false
	}
	for _, m := range x.exclude {
		if m.match(entityID) {
			return false
		}
	}
	if len(x.include) == 0 {
		return true
	}
	for _, m := range x.include {
		if m.match(entityID) {
			return true
		}
	}
	return false
}

// --- following the core ---

func (x *Exporter) onStateChanged(ev core.Event) {
	id, _ := ev.Data["entity_id"].(string)
	if id == "" {
		return
	}
	if domain, _ := core.SplitEntityID(id); exportComponent[domain] == "" {
		if !notExported[domain] {
			x.logSkipOnce(id, "domain has no MQTT component")
		}
		return
	}
	if x.exportable(id) {
		x.markDirty(id)
	}
}

// logSkipOnce keeps a chatty entity from filling the log at every state
// change; key identifies the entity and the reason.
func (x *Exporter) logSkipOnce(key, why string) {
	x.mu.Lock()
	seen := x.skipLogged[key]
	x.skipLogged[key] = true
	x.mu.Unlock()
	if !seen {
		x.log.Debug("export: skipping entity", "entity", key, "reason", why)
	}
}

// markDirty queues an entity for a sync. The sync reads the state when it
// runs, so the latest state always wins and repeated marks collapse.
func (x *Exporter) markDirty(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.dirtySet[id] {
		return
	}
	x.dirtySet[id] = true
	x.dirty = append(x.dirty, id)
	x.outWake.Signal()
}

func (x *Exporter) onRegistryUpdated(core.Event) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.debounce != nil {
		return
	}
	x.debounce = time.AfterFunc(x.debounceDelay, func() {
		x.mu.Lock()
		defer x.mu.Unlock()
		x.debounce = nil
		x.resync = true
		x.outWake.Signal()
	})
}

func (x *Exporter) outLoop() {
	defer x.wg.Done()
	for {
		x.mu.Lock()
		for x.running && !x.resync && len(x.dirty) == 0 {
			x.outWake.Wait()
		}
		if !x.running {
			x.mu.Unlock()
			return
		}
		var id string
		all := x.resync
		if all {
			x.resync = false
		} else {
			id = x.dirty[0]
			x.dirty = x.dirty[1:]
			delete(x.dirtySet, id)
		}
		x.outBusy = true
		x.mu.Unlock()
		var err error
		if all {
			err = x.syncAll()
		} else {
			err = x.syncEntity(id)
		}
		if err != nil {
			x.log.Warn("export: sync failed", "entity_id", id, "err", err)
		}
		x.mu.Lock()
		x.outBusy = false
		x.mu.Unlock()
	}
}

// syncAll brings the broker in line with the core: publish every
// exportable state and clear what no longer exists.
func (x *Exporter) syncAll() error {
	seen := map[string]bool{}
	var firstErr error
	for _, s := range x.c.States() {
		if !x.exportable(s.EntityID) {
			continue
		}
		seen[s.EntityID] = true
		if err := x.syncEntity(s.EntityID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for id := range x.exported {
		if !seen[id] {
			if err := x.remove(id); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (x *Exporter) syncEntity(id string) error {
	s := x.c.GetState(id)
	if s == nil {
		return x.remove(id)
	}
	component, cfg, err := x.buildConfig(s)
	if err != nil {
		// Not exportable yet (a select without options, for example); a
		// later state change gets another chance.
		x.logSkipOnce(id+": "+err.Error(), err.Error())
		return nil
	}
	ent := x.exported[id]
	if ent == nil {
		ent = &exportedEntity{topics: map[string]bool{}}
		x.exported[id] = ent
	}
	ent.configTopic = fmt.Sprintf("%s/%s/%s_%s/config", x.opts.DiscoveryPrefix, component, x.hubID, core.Slugify(id))
	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("%s: discovery config: %w", id, err)
	}
	if err := x.publishChanged(ent.configTopic, string(payload)); err != nil {
		return err
	}
	attrs, err := json.Marshal(s.Attributes)
	if err != nil {
		return fmt.Errorf("%s: attributes: %w", id, err)
	}
	outs := []topicPayload{
		{x.topic("state", id), x.statePayload(s, component)},
		{x.topic("attr", id), string(attrs)},
	}
	outs = append(outs, x.extraPayloads(s, component)...)
	for _, o := range outs {
		ent.topics[o.topic] = true
		if err := x.publishChanged(o.topic, o.payload); err != nil {
			return err
		}
	}
	return nil
}

// remove clears the retained config and the retained values of an entity,
// which makes HA delete the entity and keeps stale values from coming back.
func (x *Exporter) remove(id string) error {
	ent := x.exported[id]
	if ent == nil {
		return nil
	}
	delete(x.exported, id)
	var firstErr error
	for _, t := range append([]string{ent.configTopic}, topicKeys(ent.topics)...) {
		delete(x.last, t)
		if err := x.tr.Publish(t, nil, exportQoS, true); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("clearing %s: %w", t, err)
		}
	}
	return firstErr
}

func topicKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// publishChanged skips payloads identical to the last one, so a registry
// resync does not re-send the whole hub.
func (x *Exporter) publishChanged(topic, payload string) error {
	if prev, ok := x.last[topic]; ok && prev == payload {
		return nil
	}
	if err := x.tr.Publish(topic, []byte(payload), exportQoS, true); err != nil {
		return fmt.Errorf("publishing %s: %w", topic, err)
	}
	x.last[topic] = payload
	return nil
}

type topicPayload struct{ topic, payload string }

func (x *Exporter) topic(kind, entityID string) string {
	return x.ns + "/" + kind + "/" + entityID
}

// --- discovery config ---

func (x *Exporter) buildConfig(s *core.State) (component string, cfg map[string]any, err error) {
	domain, object := core.SplitEntityID(s.EntityID)
	component = exportComponent[domain]
	a := s.Attributes
	name := attrString(a, "friendly_name")
	if name == "" {
		name = object
	}
	cfg = map[string]any{
		"name":                  name,
		"unique_id":             x.hubID + "_" + s.EntityID,
		"object_id":             x.hubID + "_" + object,
		"availability_topic":    x.statusTopic,
		"payload_available":     "online",
		"payload_not_available": "offline",
		"json_attributes_topic": x.topic("attr", s.EntityID),
		"device":                x.device,
		"origin":                x.origin,
	}
	if icon := attrString(a, "icon"); icon != "" {
		cfg["icon"] = icon
	}
	if e := x.c.Entity(s.EntityID); e != nil && e.EntityCategory != "" {
		// HA refuses the "config" category on read-only components.
		if !(e.EntityCategory == "config" && (component == "sensor" || component == "binary_sensor")) {
			cfg["entity_category"] = e.EntityCategory
		}
	}
	id := s.EntityID
	state := x.topic("state", id)
	cmd := x.topic("cmd", id)
	set := func(pairs ...any) {
		for i := 0; i+1 < len(pairs); i += 2 {
			cfg[pairs[i].(string)] = pairs[i+1]
		}
	}
	// copyAttrs forwards attributes whose HA option has the same name.
	copyAttrs := func(keys ...string) {
		for _, k := range keys {
			if v, ok := a[k]; ok && v != nil {
				cfg[k] = v
			}
		}
	}
	switch component {
	case "switch":
		set("state_topic", state, "command_topic", cmd, "payload_on", "ON", "payload_off", "OFF", "state_on", core.StateOn, "state_off", core.StateOff)
		copyAttrs("device_class")
	case "light":
		x.lightConfig(cfg, s, state, cmd)
	case "number":
		set("state_topic", state, "command_topic", cmd)
		copyAttrs("min", "max", "step", "mode", "device_class", "unit_of_measurement")
	case "select":
		options := attrStrings(a, "options")
		if len(options) == 0 {
			return "", nil, fmt.Errorf("select has no options attribute")
		}
		set("state_topic", state, "command_topic", cmd, "options", options)
	case "text":
		set("state_topic", state, "command_topic", cmd)
		copyAttrs("min", "max", "mode", "pattern")
	case "button":
		set("command_topic", cmd, "payload_press", "PRESS")
		copyAttrs("device_class")
	case "binary_sensor":
		set("state_topic", state, "payload_on", core.StateOn, "payload_off", core.StateOff)
		copyAttrs("device_class")
	case "sensor":
		set("state_topic", state)
		copyAttrs("device_class")
		if attrString(a, "unit_of_measurement") != "" {
			// A state class only makes sense for numeric measurements,
			// which is what a unit of measurement marks.
			copyAttrs("unit_of_measurement", "state_class")
		}
		if attrString(a, "device_class") == "enum" {
			copyAttrs("options")
		}
	case "cover":
		set("state_topic", state, "command_topic", cmd, "payload_open", "OPEN", "payload_close", "CLOSE", "payload_stop", "STOP",
			"state_open", "open", "state_closed", "closed", "state_opening", "opening", "state_closing", "closing")
		copyAttrs("device_class")
		if _, ok := attrFloat(a, "current_position"); ok {
			set("set_position_topic", x.topic("pos", id), "position_topic", x.topic("pos_state", id))
		}
	case "lock":
		set("state_topic", state, "command_topic", cmd, "payload_lock", "LOCK", "payload_unlock", "UNLOCK",
			"state_locked", "locked", "state_unlocked", "unlocked", "state_locking", "locking", "state_unlocking", "unlocking", "state_jammed", "jammed")
	case "fan":
		set("state_topic", state, "command_topic", cmd, "payload_on", "ON", "payload_off", "OFF",
			"state_value_template", "{{ 'ON' if value == 'on' else 'OFF' }}")
		_, hasPct := attrFloat(a, "percentage")
		_, hasCtl := x.c.Controller(id).(core.FanSpeed)
		if hasPct || hasCtl {
			set("percentage_command_topic", x.topic("pct", id))
		}
		if hasPct {
			set("percentage_state_topic", x.topic("pct_state", id))
		}
	case "climate":
		// Climate has no plain state topic; its state is the HVAC mode.
		set("mode_command_topic", x.topic("mode", id), "mode_state_topic", state,
			"temperature_command_topic", x.topic("temp", id), "temperature_state_topic", x.topic("temp_state", id),
			"current_temperature_topic", x.topic("curtemp", id))
		if modes := attrStrings(a, "hvac_modes"); len(modes) > 0 {
			set("modes", modes)
		}
		for attr, opt := range map[string]string{"min_temp": "min_temp", "max_temp": "max_temp", "target_temp_step": "temp_step"} {
			if v, ok := a[attr]; ok && v != nil {
				cfg[opt] = v
			}
		}
	}
	return component, cfg, nil
}

func (x *Exporter) lightConfig(cfg map[string]any, s *core.State, state, cmd string) {
	a := s.Attributes
	modes := attrStrings(a, "supported_color_modes")
	if len(modes) == 0 {
		if _, ok := attrFloat(a, "brightness"); ok {
			modes = []string{"brightness"}
		} else {
			modes = []string{"onoff"}
		}
	}
	cfg["schema"] = "json"
	cfg["state_topic"] = state
	cfg["command_topic"] = cmd
	cfg["supported_color_modes"] = modes
	cfg["color_mode"] = true
	dimmable, colorTemp := false, false
	for _, m := range modes {
		if m != "onoff" {
			dimmable = true
		}
		if m == "color_temp" {
			colorTemp = true
		}
	}
	cfg["brightness"] = dimmable
	if colorTemp {
		// Kelvin is unambiguous on the wire; mireds are being phased out.
		cfg["color_temp_kelvin"] = true
		for _, k := range []string{"min_color_temp_kelvin", "max_color_temp_kelvin"} {
			if v, ok := a[k]; ok && v != nil {
				cfg[k] = v
			}
		}
	}
	if effects := attrStrings(a, "effect_list"); len(effects) > 0 {
		cfg["effect"] = true
		cfg["effect_list"] = effects
	}
}

// --- state payloads ---

// unknownOrUnavailable states have no counterpart in most HA components;
// "None" is the reset payload they understand.
func neutralState(state string) string {
	if state == core.StateUnknown || state == core.StateUnavailable {
		return "None"
	}
	return state
}

func (x *Exporter) statePayload(s *core.State, component string) string {
	switch component {
	case "light":
		return lightStateJSON(s)
	case "text":
		return s.State
	}
	return neutralState(s.State)
}

func lightStateJSON(s *core.State) string {
	m := map[string]any{}
	switch s.State {
	case core.StateOn:
		m["state"] = "ON"
	case core.StateOff:
		m["state"] = "OFF"
	default:
		m["state"] = nil
	}
	if s.State == core.StateOn {
		a := s.Attributes
		if v, ok := a["brightness"]; ok && v != nil {
			m["brightness"] = v
		}
		mode := attrString(a, "color_mode")
		if mode != "" {
			m["color_mode"] = mode
		}
		switch mode {
		case "color_temp":
			if k, ok := attrFloat(a, "color_temp_kelvin"); ok {
				m["color_temp"] = k
			} else if mired, ok := attrFloat(a, "color_temp"); ok && mired > 0 {
				m["color_temp"] = float64(int(1000000/mired + 0.5))
			}
		case "rgb":
			if c := attrFloats(a, "rgb_color", 3); c != nil {
				m["color"] = map[string]any{"r": c[0], "g": c[1], "b": c[2]}
			}
		case "rgbw":
			if c := attrFloats(a, "rgbw_color", 4); c != nil {
				m["color"] = map[string]any{"r": c[0], "g": c[1], "b": c[2], "w": c[3]}
			}
		case "rgbww":
			if c := attrFloats(a, "rgbww_color", 5); c != nil {
				m["color"] = map[string]any{"r": c[0], "g": c[1], "b": c[2], "c": c[3], "w": c[4]}
			}
		case "hs":
			if c := attrFloats(a, "hs_color", 2); c != nil {
				m["color"] = map[string]any{"h": c[0], "s": c[1]}
			}
		case "xy":
			if c := attrFloats(a, "xy_color", 2); c != nil {
				m["color"] = map[string]any{"x": c[0], "y": c[1]}
			}
		}
		if e := attrString(a, "effect"); e != "" {
			m["effect"] = e
		}
	}
	b, _ := json.Marshal(m) // only strings, numbers and nil
	return string(b)
}

// extraPayloads are the additional state topics of cover, fan and climate.
func (x *Exporter) extraPayloads(s *core.State, component string) []topicPayload {
	id, a := s.EntityID, s.Attributes
	num := func(key string) string {
		if f, ok := attrFloat(a, key); ok {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return "None"
	}
	switch component {
	case "cover":
		if _, ok := attrFloat(a, "current_position"); ok {
			return []topicPayload{{x.topic("pos_state", id), num("current_position")}}
		}
	case "fan":
		if _, ok := attrFloat(a, "percentage"); ok {
			return []topicPayload{{x.topic("pct_state", id), num("percentage")}}
		}
	case "climate":
		return []topicPayload{
			{x.topic("temp_state", id), num("temperature")},
			{x.topic("curtemp", id), num("current_temperature")},
		}
	}
	return nil
}

// --- commands ---

func (x *Exporter) onCommand(kind string, m Message) {
	if m.Retained {
		// A retained command is stale by definition: the broker replays it
		// to every new subscriber long after it was issued.
		x.log.Debug("export: ignoring retained command", "topic", m.Topic)
		return
	}
	id := strings.TrimPrefix(m.Topic, x.ns+"/"+kind+"/")
	if id == m.Topic || !core.ValidEntityID(id) || !x.exportable(id) {
		x.log.Warn("export: command for an entity that is not exported", "topic", m.Topic)
		return
	}
	payload := strings.TrimSpace(string(m.Payload))
	x.mu.Lock()
	defer x.mu.Unlock()
	x.cmds = append(x.cmds, queuedCommand{at: time.Now(), run: func(ctx context.Context) {
		if err := x.execute(ctx, kind, id, payload); err != nil {
			x.log.Warn("export: command failed", "topic", m.Topic, "payload", payload, "err", err)
		}
	}})
	x.cmdWake.Signal()
}

func (x *Exporter) cmdLoop() {
	defer x.wg.Done()
	for {
		x.mu.Lock()
		for x.running && len(x.cmds) == 0 {
			x.cmdWake.Wait()
		}
		if !x.running {
			x.mu.Unlock()
			return
		}
		job := x.cmds[0]
		x.cmds = x.cmds[1:]
		x.cmdBusy = true
		x.mu.Unlock()
		if age := time.Since(job.at); age > x.opts.CommandMaxAge {
			x.log.Warn("export: dropping stale command", "age", age)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
			job.run(ctx)
			cancel()
		}
		x.mu.Lock()
		x.cmdBusy = false
		x.mu.Unlock()
	}
}

// idle reports whether both queues are empty and both workers are waiting;
// tests use it to wait for the asynchronous part to finish.
func (x *Exporter) idle() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return !x.resync && len(x.dirty) == 0 && !x.outBusy && len(x.cmds) == 0 && !x.cmdBusy && x.debounce == nil
}

func (x *Exporter) call(ctx context.Context, domain, service string, data map[string]any) error {
	_, err := x.c.CallService(ctx, domain, service, data, core.NewContext())
	return err
}

func (x *Exporter) execute(ctx context.Context, kind, id, payload string) error {
	domain, _ := core.SplitEntityID(id)
	target := map[string]any{"entity_id": id}
	with := func(k string, v any) map[string]any {
		d := map[string]any{"entity_id": id}
		d[k] = v
		return d
	}
	switch kind {
	case "cmd":
		switch exportComponent[domain] {
		case "light":
			return x.lightCommand(ctx, id, payload)
		case "switch":
			return x.onOff(ctx, domain, id, payload)
		case "fan":
			return x.onOff(ctx, domain, id, payload)
		case "number":
			f, err := strconv.ParseFloat(payload, 64)
			if err != nil {
				return fmt.Errorf("number payload %q: %w", payload, err)
			}
			return x.call(ctx, domain, "set_value", with("value", f))
		case "text":
			return x.call(ctx, domain, "set_value", with("value", payload))
		case "select":
			return x.call(ctx, domain, "select_option", with("option", payload))
		case "button":
			if payload != "PRESS" {
				return fmt.Errorf("button payload %q is not PRESS", payload)
			}
			return x.call(ctx, domain, "press", target)
		case "cover":
			switch payload {
			case "OPEN":
				return x.call(ctx, domain, "open_cover", target)
			case "CLOSE":
				return x.call(ctx, domain, "close_cover", target)
			case "STOP":
				return x.call(ctx, domain, "stop_cover", target)
			}
			return fmt.Errorf("cover payload %q is not OPEN, CLOSE or STOP", payload)
		case "lock":
			switch payload {
			case "LOCK":
				return x.call(ctx, domain, "lock", target)
			case "UNLOCK":
				return x.call(ctx, domain, "unlock", target)
			}
			return fmt.Errorf("lock payload %q is not LOCK or UNLOCK", payload)
		}
		return fmt.Errorf("%s accepts no command on the cmd topic", id)
	case "pos":
		if domain != "cover" {
			return fmt.Errorf("%s has no position", id)
		}
		p, err := strconv.Atoi(payload)
		if err != nil {
			return fmt.Errorf("position payload %q: %w", payload, err)
		}
		return x.call(ctx, domain, "set_cover_position", with("position", p))
	case "pct":
		if domain != "fan" {
			return fmt.Errorf("%s has no speed percentage", id)
		}
		p, err := strconv.Atoi(payload)
		if err != nil {
			return fmt.Errorf("percentage payload %q: %w", payload, err)
		}
		return x.call(ctx, domain, "set_percentage", with("percentage", p))
	case "mode":
		if domain != "climate" {
			return fmt.Errorf("%s has no HVAC mode", id)
		}
		return x.call(ctx, domain, "set_hvac_mode", with("hvac_mode", payload))
	case "temp":
		if domain != "climate" {
			return fmt.Errorf("%s has no target temperature", id)
		}
		f, err := strconv.ParseFloat(payload, 64)
		if err != nil {
			return fmt.Errorf("temperature payload %q: %w", payload, err)
		}
		return x.call(ctx, domain, "set_temperature", with("temperature", f))
	}
	return fmt.Errorf("unknown command kind %q", kind)
}

func (x *Exporter) onOff(ctx context.Context, domain, id, payload string) error {
	switch strings.ToUpper(payload) {
	case "ON":
		return x.call(ctx, domain, "turn_on", map[string]any{"entity_id": id})
	case "OFF":
		return x.call(ctx, domain, "turn_off", map[string]any{"entity_id": id})
	}
	return fmt.Errorf("payload %q is not ON or OFF", payload)
}

// lightCommand maps the HA JSON light schema to light.turn_on / turn_off.
func (x *Exporter) lightCommand(ctx context.Context, id, payload string) error {
	var cmd map[string]any
	if err := json.Unmarshal([]byte(payload), &cmd); err != nil {
		// HA also sends bare ON/OFF for lights with a plain payload.
		if up := strings.ToUpper(payload); up == "ON" || up == "OFF" {
			cmd = map[string]any{"state": up}
		} else {
			return fmt.Errorf("light payload %q is neither JSON nor ON/OFF", payload)
		}
	}
	state, _ := cmd["state"].(string)
	data := map[string]any{"entity_id": id}
	if tr, ok := cmd["transition"]; ok {
		data["transition"] = tr
	}
	switch strings.ToUpper(state) {
	case "OFF":
		return x.call(ctx, "light", "turn_off", data)
	case "ON":
	default:
		return fmt.Errorf("light command has no state ON or OFF: %q", payload)
	}
	if v, ok := cmd["brightness"]; ok {
		data["brightness"] = v
	}
	if v, ok := cmd["color_temp"]; ok {
		// The config announces color_temp_kelvin, so this is Kelvin.
		data["color_temp_kelvin"] = v
	}
	if v, ok := cmd["effect"]; ok {
		data["effect"] = v
	}
	if col, ok := cmd["color"].(map[string]any); ok {
		if err := lightColor(data, col); err != nil {
			return err
		}
	}
	return x.call(ctx, "light", "turn_on", data)
}

func lightColor(data, col map[string]any) error {
	pick := func(keys ...string) ([]any, bool) {
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			v, ok := col[k]
			if !ok {
				return nil, false
			}
			out = append(out, v)
		}
		return out, true
	}
	switch {
	case has(col, "r", "g", "b", "c", "w"):
		v, _ := pick("r", "g", "b", "c", "w")
		data["rgbww_color"] = v
	case has(col, "r", "g", "b", "w"):
		v, _ := pick("r", "g", "b", "w")
		data["rgbw_color"] = v
	case has(col, "r", "g", "b"):
		v, _ := pick("r", "g", "b")
		data["rgb_color"] = v
	case has(col, "x", "y"):
		v, _ := pick("x", "y")
		data["xy_color"] = v
	case has(col, "h", "s"):
		v, _ := pick("h", "s")
		data["hs_color"] = v
	default:
		return fmt.Errorf("light color %v is none of r/g/b, x/y or h/s", col)
	}
	return nil
}

func has(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// --- attribute helpers ---

func attrString(a map[string]any, key string) string {
	s, _ := a[key].(string)
	return s
}

func attrFloat(a map[string]any, key string) (float64, bool) {
	switch v := a[key].(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func attrStrings(a map[string]any, key string) []string {
	switch v := a[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// attrFloats returns n numbers from a list attribute, or nil when the
// attribute is missing, too short or not numeric.
func attrFloats(a map[string]any, key string, n int) []float64 {
	var list []any
	switch v := a[key].(type) {
	case []any:
		list = v
	case []int:
		for _, e := range v {
			list = append(list, e)
		}
	case []float64:
		for _, e := range v {
			list = append(list, e)
		}
	default:
		return nil
	}
	if len(list) < n {
		return nil
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		f, ok := attrFloat(map[string]any{"v": list[i]}, "v")
		if !ok {
			return nil
		}
		out[i] = f
	}
	return out
}
