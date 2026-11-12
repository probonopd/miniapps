/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import "github.com/probonopd/miniha/internal/core"

func init() {
	components["tag"] = newTag
}

// newTag turns scanned tag ids into "tag_scanned" events. HA has no tag
// entity, so the constructor never writes a state: the registry entry that
// the shared entity base creates stays invisible in the state machine.
func newTag(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("topic") {
		return nil, errMissing("topic")
	}
	tpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	e.subscribe(cfg.Str("topic"), func(m Message) {
		tagID := string(m.Payload)
		if tpl != nil {
			tagID, err = e.render(tpl, m.Payload)
			if err != nil {
				e.d.log.Warn("tag value_template failed", "entity_id", e.entityID, "err", err)
				return
			}
		}
		if tagID == "" || tagID == "None" {
			return
		}
		var deviceID any
		if e.deviceID != "" {
			deviceID = e.deviceID
		}
		e.d.core.Fire("tag_scanned", map[string]any{"tag_id": tagID, "device_id": deviceID}, core.NewContext())
	})
	return &tagComponent{}, nil
}

type tagComponent struct{}

func (*tagComponent) stop() {}
