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
	"slices"
)

func init() {
	components["device_tracker"] = newDeviceTracker
}

var trackerSourceTypes = []string{"gps", "router", "bluetooth", "bluetooth_le"}

func newDeviceTracker(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("state_topic") && !cfg.Has("json_attributes_topic") {
		return nil, fmt.Errorf("state_topic or json_attributes_topic is required")
	}
	source := cfg.StrDef("source_type", "gps")
	if !slices.Contains(trackerSourceTypes, source) {
		return nil, fmt.Errorf("invalid source_type %q", source)
	}
	home, notHome, reset := cfg.StrDef("payload_home", "home"), cfg.StrDef("payload_not_home", "not_home"), cfg.StrDef("payload_reset", "None")
	// latitude, longitude and gps_accuracy arrive through the entity's
	// JSON attributes; only the source type is added here.
	e.attrs = func() map[string]any { return map[string]any{"source_type": source} }
	if err := subscribeState(e, func(payload string) {
		switch payload {
		case home:
			e.setState("home", context.Background())
		case notHome:
			e.setState("not_home", context.Background())
		case reset:
			e.setState("", context.Background())
		default:
			// Any other payload names a zone.
			e.setState(payload, context.Background())
		}
	}); err != nil {
		return nil, err
	}
	if source == "gps" && cfg.Has("json_attributes_topic") {
		if err := locateFromAttributes(e); err != nil {
			return nil, err
		}
	}
	e.write(false)
	return trackerComponent{}, nil
}

// locateFromAttributes resolves the zone of a GPS tracker that only
// reports coordinates, as most trackers do; without it such a tracker
// would stay unknown and its person could never be home. The entity's own
// attribute subscription publishes latitude and longitude; this one only
// derives the state, so a later state_topic payload still overrides it.
func locateFromAttributes(e *entity) error {
	tpl, err := e.cfg.Template("json_attributes_template")
	if err != nil {
		return err
	}
	e.subscribe(e.cfg.Str("json_attributes_topic"), func(m Message) {
		payload := m.Payload
		if tpl != nil {
			s, err := e.render(tpl, m.Payload)
			if err != nil {
				// The entity's own subscription logs the template failure.
				return
			}
			payload = []byte(s)
		}
		var attrs map[string]any
		if err := json.Unmarshal(payload, &attrs); err != nil {
			return
		}
		lat, latOK := attrs["latitude"].(float64)
		lon, lonOK := attrs["longitude"].(float64)
		if !latOK || !lonOK {
			return
		}
		accuracy, _ := attrs["gps_accuracy"].(float64)
		e.setState(e.d.core.Locate(lat, lon, accuracy), context.Background())
	})
	return nil
}

type trackerComponent struct{}

func (trackerComponent) stop() {}
