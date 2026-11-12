/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"fmt"
	"math"
	"strconv"

	"github.com/probonopd/miniha/internal/core"
)

const earthRadiusMeters = 6371000.0

// floatAttr reads a numeric attribute; GPS values arrive as floats from
// MQTT/JSON but as strings from some integrations.
func floatAttr(s *core.State, name string) (float64, bool) {
	if s == nil {
		return 0, false
	}
	switch x := s.Attributes[name].(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// haversine returns the great-circle distance in meters.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusMeters * math.Asin(math.Min(1, math.Sqrt(a)))
}

// inZone tells whether a tracker (device_tracker or person) is inside a
// zone. Coordinates win over the tracker's state because a GPS update
// can arrive before the tracker's own zone name is recomputed; trackers
// without coordinates are matched by their state, which is "home" for
// zone.home and the zone's friendly name for any other zone.
func inZone(tracker, zone *core.State) bool {
	if tracker == nil || zone == nil {
		return false
	}
	lat, okLat := floatAttr(tracker, "latitude")
	lon, okLon := floatAttr(tracker, "longitude")
	zLat, okZLat := floatAttr(zone, "latitude")
	zLon, okZLon := floatAttr(zone, "longitude")
	radius, okRadius := floatAttr(zone, "radius")
	if okLat && okLon && okZLat && okZLon && okRadius {
		return haversine(lat, lon, zLat, zLon) <= radius
	}
	if zone.EntityID == "zone.home" {
		return tracker.State == "home"
	}
	name, _ := zone.Attributes["friendly_name"].(string)
	return name != "" && tracker.State == name
}

// zoneIDs validates the entity and zone ids of a zone trigger or
// condition.
func zoneIDs(kind string, entityRaw, zoneRaw node) (entities, zones []string, err error) {
	entities = strList(entityRaw)
	zones = strList(zoneRaw)
	if len(entities) == 0 {
		return nil, nil, fmt.Errorf("%s needs entity_id", kind)
	}
	if len(zones) == 0 {
		return nil, nil, fmt.Errorf("%s needs zone", kind)
	}
	for _, id := range entities {
		if !core.ValidEntityID(id) {
			return nil, nil, fmt.Errorf("%s: invalid entity id %q", kind, id)
		}
		if d, _ := core.SplitEntityID(id); d != "device_tracker" && d != "person" {
			return nil, nil, fmt.Errorf("%s: %s is not a device_tracker or person", kind, id)
		}
	}
	for _, id := range zones {
		if !core.ValidEntityID(id) {
			return nil, nil, fmt.Errorf("%s: invalid zone id %q", kind, id)
		}
		if d, _ := core.SplitEntityID(id); d != "zone" {
			return nil, nil, fmt.Errorf("%s: %s is not a zone", kind, id)
		}
	}
	return entities, zones, nil
}

// zoneCondition requires every entity to be in at least one of the
// zones, as HA does.
func (e *Engine) zoneCondition(m map[string]any) (bool, error) {
	entities, zones, err := zoneIDs("zone condition", m["entity_id"], m["zone"])
	if err != nil {
		return false, err
	}
	for _, id := range entities {
		tracker := e.core.GetState(id)
		in := false
		for _, z := range zones {
			if inZone(tracker, e.core.GetState(z)) {
				in = true
				break
			}
		}
		if !in {
			return false, nil
		}
	}
	return true, nil
}
