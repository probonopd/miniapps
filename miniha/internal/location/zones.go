/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package location provides the HA zone, person and sun integrations: the
// entities that turn GPS positions and device tracker states into "who is
// where" and "where is the sun".
package location

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/probonopd/miniha/internal/core"
)

const (
	// DefaultRadius is HA's default zone radius in meters.
	DefaultRadius = 100.0

	earthRadius = 6371000.0 // meters, mean radius as used by HA's haversine
)

// Home describes the home zone, which HA always has and which comes from
// the homeassistant section instead of the zone section.
type Home struct {
	Latitude  float64
	Longitude float64
	Radius    float64 // meters; 0 means DefaultRadius
	Name      string  // friendly name; empty means "Home"
}

type zone struct {
	entityID string
	name     string
	lat, lon float64
	radius   float64
	passive  bool
	icon     string
	isHome   bool
}

// Zones owns the zone.* entities and answers position lookups.
type Zones struct {
	c     *core.Core
	zones []*zone
	unsub func()
}

// Distance returns the great-circle distance in meters (haversine).
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := p2 - p1
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthRadius * math.Asin(math.Min(1, math.Sqrt(a)))
}

// NewZones creates zone.home plus one zone per config entry, installs the
// core's locator and keeps each zone's state at the number of persons in it.
func NewZones(c *core.Core, home Home, cfg []map[string]any) (*Zones, error) {
	z := &Zones{c: c}
	hz := &zone{entityID: "zone.home", name: home.Name, lat: home.Latitude, lon: home.Longitude, radius: home.Radius, icon: "mdi:home", isHome: true}
	if hz.name == "" {
		hz.name = "Home"
	}
	if hz.radius == 0 {
		hz.radius = DefaultRadius
	}
	if hz.radius < 0 {
		return nil, fmt.Errorf("home zone: radius must not be negative")
	}
	z.zones = append(z.zones, hz)
	seen := map[string]bool{"zone.home": true}
	for i, entry := range cfg {
		zn, err := parseZone(entry)
		if err != nil {
			return nil, fmt.Errorf("zone entry %d: %w", i+1, err)
		}
		if seen[zn.entityID] {
			return nil, fmt.Errorf("zone entry %d: %s is defined twice (zone.home always exists)", i+1, zn.entityID)
		}
		seen[zn.entityID] = true
		z.zones = append(z.zones, zn)
	}
	c.SetLocator(z.locate)
	// Person states change after trackers report, so the counts follow the
	// person entities rather than the trackers.
	z.unsub = c.Subscribe("state_changed", func(ev core.Event) {
		id, _ := ev.Data["entity_id"].(string)
		if strings.HasPrefix(id, "person.") {
			z.Recount()
		}
	})
	z.Recount()
	return z, nil
}

func parseZone(entry map[string]any) (*zone, error) {
	name, _ := entry["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	slug := core.Slugify(name)
	if slug == "" {
		return nil, fmt.Errorf("name %q has no usable characters for an entity id", name)
	}
	zn := &zone{entityID: "zone." + slug, name: name, radius: DefaultRadius, icon: "mdi:map-marker"}
	var err error
	if zn.lat, err = requiredNumber(entry, "latitude"); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if zn.lon, err = requiredNumber(entry, "longitude"); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if zn.lat < -90 || zn.lat > 90 || zn.lon < -180 || zn.lon > 180 {
		return nil, fmt.Errorf("%s: latitude/longitude out of range", name)
	}
	if v, ok := entry["radius"]; ok {
		r, ok := Number(v)
		if !ok || r < 0 {
			return nil, fmt.Errorf("%s: radius must be a non-negative number", name)
		}
		zn.radius = r
	}
	if v, ok := entry["passive"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: passive must be true or false", name)
		}
		zn.passive = b
	}
	if v, ok := entry["icon"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: icon must be a string", name)
		}
		zn.icon = s
	}
	return zn, nil
}

func requiredNumber(entry map[string]any, key string) (float64, error) {
	v, ok := entry[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	f, ok := Number(v)
	if !ok {
		return 0, fmt.Errorf("%s must be a number", key)
	}
	return f, nil
}

// Number converts a decoded YAML/JSON number to float64; YAML yields ints
// for whole numbers, so float64 alone is not enough.
func Number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// locate implements HA's active-zone rule: inside when the distance to the
// center is at most radius plus accuracy, passive zones never match, the
// closest center wins and a smaller radius breaks ties.
func (z *Zones) locate(lat, lon, accuracy float64) string {
	var best *zone
	bestDist := 0.0
	for _, zn := range z.zones {
		if zn.passive {
			continue
		}
		d := Distance(lat, lon, zn.lat, zn.lon)
		if d > zn.radius+accuracy {
			continue
		}
		if best == nil || d < bestDist || (d == bestDist && zn.radius < best.radius) {
			best, bestDist = zn, d
		}
	}
	switch {
	case best == nil:
		return "not_home"
	case best.isHome:
		return "home"
	}
	return best.name
}

// InZone reports whether a position is inside the given zone entity; a
// passive zone still answers true here because the caller names it
// explicitly (HA's zone condition does the same).
func (z *Zones) InZone(lat, lon, accuracy float64, zoneEntityID string) bool {
	for _, zn := range z.zones {
		if zn.entityID == zoneEntityID {
			return Distance(lat, lon, zn.lat, zn.lon) <= zn.radius+accuracy
		}
	}
	return false
}

// Recount rewrites every zone entity with the number of persons whose
// state names that zone.
func (z *Zones) Recount() {
	counts := map[string][]string{}
	for _, s := range z.c.States() {
		if s.Domain() == "person" {
			counts[s.State] = append(counts[s.State], s.EntityID)
		}
	}
	for _, zn := range z.zones {
		stateName := zn.name
		if zn.isHome {
			stateName = "home"
		}
		persons := counts[stateName]
		sort.Strings(persons)
		if persons == nil {
			persons = []string{}
		}
		attrs := map[string]any{
			"latitude":      zn.lat,
			"longitude":     zn.lon,
			"radius":        zn.radius,
			"passive":       zn.passive,
			"persons":       persons,
			"editable":      false,
			"icon":          zn.icon,
			"friendly_name": zn.name,
		}
		if err := z.c.SetState(zn.entityID, fmt.Sprint(len(persons)), attrs, core.Context{}); err != nil {
			// Names are validated at construction, so a failure here is a bug.
			panic(fmt.Sprintf("location: writing %s: %v", zn.entityID, err))
		}
	}
}

// Stop detaches the zone entities from the event bus; the entities stay.
func (z *Zones) Stop() {
	z.unsub()
}
