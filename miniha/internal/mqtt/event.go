/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"encoding/json"
	"slices"
)

func init() {
	components["event"] = newEvent
}

// timestampFormat is how HA stores the time of the last event and of the
// last button press as the entity state.
const timestampFormat = "2006-01-02T15:04:05.000000-07:00"

type event struct {
	e          *entity
	eventTypes []string
	last       map[string]any // payload of the last event, nil before the first
	lastType   string
}

func newEvent(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("state_topic") {
		return nil, errMissing("state_topic")
	}
	types := cfg.Strings("event_types")
	if len(types) == 0 {
		return nil, errMissing("event_types")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	ev := &event{e: e, eventTypes: types}
	e.attrs = ev.attributes
	e.subscribe(cfg.Str("state_topic"), func(m Message) {
		// A retained message is an event from the past, not a new one.
		if m.Retained {
			return
		}
		payload := m.Payload
		if tpl != nil {
			s, err := e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("value_template failed", "entity_id", e.entityID, "err", err)
				return
			}
			payload = []byte(s)
		}
		if len(payload) == 0 {
			return
		}
		var data map[string]any
		if err := json.Unmarshal(payload, &data); err != nil {
			e.d.log.Warn("event payload is not a JSON object", "entity_id", e.entityID)
			return
		}
		t, ok := data["event_type"].(string)
		if !ok || !slices.Contains(ev.eventTypes, t) {
			e.d.log.Warn("event_type is not one of event_types", "entity_id", e.entityID, "event_type", data["event_type"])
			return
		}
		delete(data, "event_type")
		e.mu.Lock()
		ev.last, ev.lastType = data, t
		e.state = e.d.core.Now().Format(timestampFormat)
		e.mu.Unlock()
		// Every event is a state_changed event in HA, even two identical
		// ones in the same microsecond.
		e.write(true)
	})
	e.write(false)
	return ev, nil
}

func (ev *event) stop() {}

// attributes runs under the entity lock, called from entity.write.
func (ev *event) attributes() map[string]any {
	a := map[string]any{}
	for k, v := range ev.last {
		a[k] = v
	}
	a["event_types"] = ev.eventTypes
	if ev.lastType != "" {
		a["event_type"] = ev.lastType
	} else {
		a["event_type"] = nil
	}
	if v := ev.e.cfg.Str("device_class"); v != "" {
		a["device_class"] = v
	}
	return a
}
