/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package helpers implements HA's helper integrations: input_boolean,
// input_number, input_select, input_text, input_datetime, input_button,
// counter and timer. They are defined in configuration.yaml, keep their
// state across restarts in helpers_state.json and expose HA's entity
// attributes and services.
package helpers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/store"
)

const (
	domInputBoolean  = "input_boolean"
	domInputNumber   = "input_number"
	domInputSelect   = "input_select"
	domInputText     = "input_text"
	domInputDatetime = "input_datetime"
	domInputButton   = "input_button"
	domCounter       = "counter"
	domTimer         = "timer"
)

// Domains lists the helper domains in the order they are loaded.
var Domains = []string{domInputBoolean, domInputNumber, domInputSelect, domInputText, domInputDatetime, domInputButton, domCounter, domTimer}

// stateFile is the file in the data directory that carries helper state
// across restarts.
const stateFile = "helpers_state.json"

// saveDelay debounces writes: a counter incremented in a loop must not
// rewrite the file on every step.
const saveDelay = 2 * time.Second

// Config is the helper part of configuration.yaml: one raw section per
// domain, keyed by object id, in HA syntax.
type Config struct {
	InputBoolean  map[string]any
	InputNumber   map[string]any
	InputSelect   map[string]any
	InputText     map[string]any
	InputDatetime map[string]any
	InputButton   map[string]any
	Counter       map[string]any
	Timer         map[string]any
	// Location is the time zone input_datetime values are read and shown
	// in. Nil means the host's local zone.
	Location *time.Location
}

func (c Config) section(domain string) map[string]any {
	switch domain {
	case domInputBoolean:
		return c.InputBoolean
	case domInputNumber:
		return c.InputNumber
	case domInputSelect:
		return c.InputSelect
	case domInputText:
		return c.InputText
	case domInputDatetime:
		return c.InputDatetime
	case domInputButton:
		return c.InputButton
	case domCounter:
		return c.Counter
	case domTimer:
		return c.Timer
	}
	return nil
}

// base is what every helper entity shares.
type base struct {
	h        *Helpers
	domain   string
	objectID string
	entityID string // set once by install, before the entity is reachable
	name     string
	icon     string
	unit     string
}

func (b *base) meta() *base { return b }

// record is the persisted form of one helper.
type record struct {
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// event is a bus event to fire once the state write is done.
type event struct {
	typ  string
	data map[string]any
}

// helper is implemented by every helper kind. Methods documented as
// locked run with Helpers.mu held.
type helper interface {
	meta() *base
	// def returns the parsed definition; Reload compares them to find
	// out which entities changed.
	def() any
	// view returns the state and attributes (locked).
	view() (string, map[string]any)
	// record returns what must survive a restart (locked).
	record() record
	// restore sets the initial value from the definition and, where HA
	// would, from a persisted record. reload is true when the entity
	// replaces a changed one, which keeps its value instead of applying
	// "initial" again (locked).
	restore(r *record, reload bool)
	// resume starts background work such as a running timer's deadline;
	// called without the lock after the first state write.
	resume()
	// stop cancels background work (locked).
	stop()
}

// errSkip tells update that the call changed nothing and fires nothing.
var errSkip = errors.New("no change")

var (
	errStopped = errors.New("helpers are stopped")
	errRemoved = errors.New("helper no longer exists")
)

type Helpers struct {
	c   *core.Core
	dir string
	log *slog.Logger
	loc *time.Location

	// mu guards the entity map and the mutable state of every helper.
	// It is never held while writing a state or firing an event because
	// subscribers run synchronously and may call services.
	mu       sync.Mutex
	entities map[string]helper // by entity id
	saved    map[string]record // loaded from disk, consumed by install

	stopped atomic.Bool

	saveMu    sync.Mutex // guards saveTimer
	saveTimer *time.Timer
	fileMu    sync.Mutex // serializes file writes

	loadMu sync.Mutex
	loader func() (Config, error)
}

// New validates cfg, registers one entity per definition, restores the
// persisted state from dir/helpers_state.json and registers the services.
// Any invalid definition makes New fail without registering anything.
func New(c *core.Core, cfg Config, dir string, log *slog.Logger) (*Helpers, error) {
	if c == nil {
		return nil, errors.New("helpers: core is required")
	}
	if dir == "" {
		return nil, errors.New("helpers: data directory is required")
	}
	if log == nil {
		log = slog.Default()
	}
	h := &Helpers{c: c, dir: dir, log: log, loc: cfg.Location, entities: map[string]helper{}}
	if h.loc == nil {
		h.loc = time.Local
	}
	saved, err := h.loadSaved()
	if err != nil {
		return nil, err
	}
	h.saved = saved
	parsed, err := h.parseAll(cfg, Domains)
	if err != nil {
		return nil, err
	}
	h.registerServices()
	for _, d := range Domains {
		for _, e := range parsed[d] {
			if err := h.install(e, nil, false); err != nil {
				h.stopAll()
				return nil, err
			}
		}
	}
	h.mu.Lock()
	h.saved = nil
	h.mu.Unlock()
	return h, nil
}

func (h *Helpers) path() string { return filepath.Join(h.dir, stateFile) }

func (h *Helpers) loadSaved() (map[string]record, error) {
	raw, err := os.ReadFile(h.path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("helpers: reading %s: %w", h.path(), err)
	}
	m := map[string]record{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("helpers: parsing %s: %w", h.path(), err)
	}
	return m, nil
}

// SetLoader installs the function the <domain>.reload services call to
// get a freshly read configuration. Without one, those services fail.
func (h *Helpers) SetLoader(load func() (Config, error)) {
	h.loadMu.Lock()
	h.loader = load
	h.loadMu.Unlock()
}

// Stop cancels running timers and writes the state file.
func (h *Helpers) Stop() {
	if h.stopped.Swap(true) {
		return
	}
	h.stopAll()
	h.saveMu.Lock()
	if h.saveTimer != nil {
		h.saveTimer.Stop()
		h.saveTimer = nil
	}
	h.saveMu.Unlock()
	if err := h.flush(); err != nil {
		h.log.Error("saving helper state failed", "err", err)
	}
}

func (h *Helpers) stopAll() {
	h.mu.Lock()
	for _, e := range h.entities {
		e.stop()
	}
	h.mu.Unlock()
}

// Reload applies a freshly loaded configuration: new definitions become
// entities, changed ones are replaced (keeping their value where it is
// still valid), removed ones disappear and unchanged ones are left alone,
// so a running timer survives a reload that does not touch it. An invalid
// configuration changes nothing.
func (h *Helpers) Reload(cfg Config) error {
	return h.reload(cfg, Domains)
}

func (h *Helpers) reload(cfg Config, domains []string) error {
	if h.stopped.Load() {
		return errStopped
	}
	parsed, err := h.parseAll(cfg, domains)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range domains {
		fresh := map[string]helper{}
		for _, e := range parsed[d] {
			fresh[e.meta().objectID] = e
		}
		old := map[string]helper{}
		h.mu.Lock()
		for _, e := range h.entities {
			if e.meta().domain == d {
				old[e.meta().objectID] = e
			}
		}
		h.mu.Unlock()
		for _, oid := range sortedKeys(old) {
			if _, ok := fresh[oid]; !ok {
				h.remove(old[oid])
			}
		}
		for _, oid := range sortedKeys(fresh) {
			ne := fresh[oid]
			oe, existed := old[oid]
			switch {
			case !existed:
				if err := h.install(ne, nil, false); err != nil {
					errs = append(errs, err)
				}
			case reflect.DeepEqual(oe.def(), ne.def()):
			default:
				h.mu.Lock()
				rec := oe.record()
				oe.stop()
				delete(h.entities, oe.meta().entityID)
				h.mu.Unlock()
				if err := h.install(ne, &rec, true); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type parser func(h *Helpers, id string, raw any) (helper, error)

var parsers = map[string]parser{
	domInputBoolean:  parseInputBoolean,
	domInputNumber:   parseInputNumber,
	domInputSelect:   parseInputSelect,
	domInputText:     parseInputText,
	domInputDatetime: parseInputDatetime,
	domInputButton:   parseInputButton,
	domCounter:       parseCounter,
	domTimer:         parseTimer,
}

// parseAll validates every definition of the given domains and reports
// all problems at once, so one run of the program shows every typo.
func (h *Helpers) parseAll(cfg Config, domains []string) (map[string][]helper, error) {
	out := map[string][]helper{}
	var errs []error
	for _, d := range domains {
		sec := cfg.section(d)
		for _, id := range sortedKeys(sec) {
			e, err := parsers[d](h, id, sec[id])
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out[d] = append(out[d], e)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func newBase(h *Helpers, domain, id, name, icon string) base {
	return base{h: h, domain: domain, objectID: id, name: name, icon: icon}
}

// install registers the entity, restores its value, publishes the first
// state and binds the controller. rec is the record of the entity this
// one replaces on reload; otherwise the record loaded from disk is used.
func (h *Helpers) install(e helper, rec *record, reload bool) error {
	b := e.meta()
	entry, err := h.c.RegisterEntity(core.EntityRegistration{
		Domain:            b.domain,
		Platform:          b.domain,
		UniqueID:          b.objectID,
		SuggestedObjectID: b.objectID,
		OriginalName:      b.name,
		OriginalIcon:      b.icon,
		Unit:              b.unit,
	})
	if err != nil {
		return fmt.Errorf("%s.%s: %w", b.domain, b.objectID, err)
	}
	b.entityID = entry.EntityID
	h.mu.Lock()
	if rec == nil && !reload {
		if r, ok := h.saved[b.entityID]; ok {
			rec = &r
			delete(h.saved, b.entityID)
		}
	}
	e.restore(rec, reload)
	h.entities[b.entityID] = e
	st, attrs := e.view()
	h.mu.Unlock()
	h.c.RegisterController(b.entityID, e)
	if err := h.c.SetState(b.entityID, st, attrs, core.NewContext()); err != nil {
		return fmt.Errorf("%s: %w", b.entityID, err)
	}
	h.markDirty()
	e.resume()
	return nil
}

func (h *Helpers) remove(e helper) {
	id := e.meta().entityID
	h.mu.Lock()
	e.stop()
	delete(h.entities, id)
	h.mu.Unlock()
	h.c.RemoveEntity(id)
	h.markDirty()
}

func (h *Helpers) lookup(entityID string) helper {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entities[entityID]
}

// update is the one way a helper changes: mutate runs under the lock and
// returns the events to fire, then the new state is written outside the
// lock with the caller's context so that CallService reports it.
func (h *Helpers) update(ctx context.Context, e helper, mutate func() ([]event, error)) error {
	if h.stopped.Load() {
		return errStopped
	}
	id := e.meta().entityID
	h.mu.Lock()
	if h.entities[id] != e {
		h.mu.Unlock()
		return errRemoved
	}
	evs, err := mutate()
	if err != nil {
		h.mu.Unlock()
		if errors.Is(err, errSkip) {
			return nil
		}
		return err
	}
	st, attrs := e.view()
	h.mu.Unlock()
	h.markDirty()
	cctx := core.ContextFrom(ctx)
	if err := h.c.SetState(id, st, attrs, cctx); err != nil {
		return err
	}
	for _, ev := range evs {
		h.c.Fire(ev.typ, ev.data, cctx)
	}
	return nil
}

// markDirty schedules the debounced write of the state file.
func (h *Helpers) markDirty() {
	if h.stopped.Load() {
		return
	}
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if h.saveTimer != nil {
		return
	}
	h.saveTimer = time.AfterFunc(saveDelay, func() {
		h.saveMu.Lock()
		h.saveTimer = nil
		h.saveMu.Unlock()
		if err := h.flush(); err != nil {
			h.log.Error("saving helper state failed", "err", err)
		}
	})
}

func (h *Helpers) flush() error {
	h.mu.Lock()
	out := make(map[string]record, len(h.entities))
	for id, e := range h.entities {
		out[id] = e.record()
	}
	h.mu.Unlock()
	raw, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		return err
	}
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	return store.WriteAtomic(h.path(), raw, 0o644)
}
