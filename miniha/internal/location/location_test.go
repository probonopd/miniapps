/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package location

import (
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

const (
	berlinLat = 52.52
	berlinLon = 13.405
)

// metersNorth returns a latitude the given distance north of lat.
func metersNorth(lat, meters float64) float64 {
	return lat + meters/111195.0
}

func testZones(t *testing.T, c *core.Core) *Zones {
	t.Helper()
	z, err := NewZones(c, Home{Latitude: berlinLat, Longitude: berlinLon, Name: "Home"}, []map[string]any{
		{"name": "Office", "latitude": metersNorth(berlinLat, 5000), "longitude": berlinLon, "radius": 200, "icon": "mdi:office-building"},
		{"name": "Annex", "latitude": metersNorth(berlinLat, 5100), "longitude": berlinLon, "radius": 200},
		{"name": "Quiet", "latitude": berlinLat, "longitude": berlinLon + 0.5, "radius": 1000, "passive": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func TestDistance(t *testing.T) {
	if d := Distance(berlinLat, berlinLon, berlinLat, berlinLon); d != 0 {
		t.Fatalf("same point: %v", d)
	}
	// Berlin to Munich is about 504 km.
	d := Distance(52.52, 13.405, 48.137, 11.575)
	if d < 500000 || d > 508000 {
		t.Fatalf("Berlin-Munich: %v", d)
	}
}

func TestZonesFromConfig(t *testing.T) {
	c := core.New()
	testZones(t, c)
	home := c.GetState("zone.home")
	if home == nil || home.State != "0" || home.Attributes["radius"] != 100.0 || home.Attributes["passive"] != false ||
		home.Attributes["icon"] != "mdi:home" || home.Attributes["friendly_name"] != "Home" || home.Attributes["latitude"] != berlinLat {
		t.Fatalf("zone.home: %+v", home)
	}
	office := c.GetState("zone.office")
	if office == nil || office.State != "0" || office.Attributes["radius"] != 200.0 || office.Attributes["icon"] != "mdi:office-building" ||
		office.Attributes["friendly_name"] != "Office" {
		t.Fatalf("zone.office: %+v", office)
	}
	if q := c.GetState("zone.quiet"); q == nil || q.Attributes["passive"] != true {
		t.Fatalf("zone.quiet: %+v", q)
	}
}

func TestZonesConfigErrors(t *testing.T) {
	home := Home{Latitude: berlinLat, Longitude: berlinLon}
	for name, cfg := range map[string][]map[string]any{
		"no name":      {{"latitude": 1.0, "longitude": 2.0}},
		"no latitude":  {{"name": "A", "longitude": 2.0}},
		"no longitude": {{"name": "A", "latitude": 1.0}},
		"bad latitude": {{"name": "A", "latitude": "x", "longitude": 2.0}},
		"range":        {{"name": "A", "latitude": 91.0, "longitude": 2.0}},
		"bad radius":   {{"name": "A", "latitude": 1.0, "longitude": 2.0, "radius": -5}},
		"bad passive":  {{"name": "A", "latitude": 1.0, "longitude": 2.0, "passive": "yes"}},
		"duplicate":    {{"name": "A", "latitude": 1.0, "longitude": 2.0}, {"name": "a", "latitude": 1.0, "longitude": 2.0}},
		"home name":    {{"name": "Home", "latitude": 1.0, "longitude": 2.0}},
	} {
		if _, err := NewZones(core.New(), home, cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// YAML decodes whole numbers as int.
	if _, err := NewZones(core.New(), home, []map[string]any{{"name": "A", "latitude": 1, "longitude": 2, "radius": 50}}); err != nil {
		t.Errorf("int numbers: %v", err)
	}
}

func TestLocate(t *testing.T) {
	c := core.New()
	z := testZones(t, c)
	office := metersNorth(berlinLat, 5000)
	cases := []struct {
		name     string
		lat, lon float64
		acc      float64
		want     string
	}{
		{"home center", berlinLat, berlinLon, 0, "home"},
		{"inside home", metersNorth(berlinLat, 90), berlinLon, 0, "home"},
		{"outside home", metersNorth(berlinLat, 150), berlinLon, 0, "not_home"},
		{"accuracy widens home", metersNorth(berlinLat, 150), berlinLon, 60, "home"},
		{"accuracy too small", metersNorth(berlinLat, 150), berlinLon, 40, "not_home"},
		{"office", office, berlinLon, 0, "Office"},
		// 5050 m is 50 m from both Office and Annex; the nearer center wins.
		{"closer to office", metersNorth(berlinLat, 5030), berlinLon, 0, "Office"},
		{"closer to annex", metersNorth(berlinLat, 5070), berlinLon, 0, "Annex"},
		{"passive ignored", berlinLat, berlinLon + 0.5, 0, "not_home"},
		{"far away", 0, 0, 0, "not_home"},
	}
	for _, tc := range cases {
		if got := c.Locate(tc.lat, tc.lon, tc.acc); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if !z.InZone(office, berlinLon, 0, "zone.office") || z.InZone(berlinLat, berlinLon, 0, "zone.office") {
		t.Error("InZone office")
	}
	if !z.InZone(berlinLat, berlinLon, 0, "zone.home") || z.InZone(berlinLat, berlinLon, 0, "zone.nope") {
		t.Error("InZone home/unknown")
	}
}

// ticker hands out strictly increasing times so "latest updated" is
// well defined in tests.
func ticker(c *core.Core) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

func setTracker(t *testing.T, c *core.Core, id, state string, attrs map[string]any) {
	t.Helper()
	if err := c.SetState(id, state, attrs, core.Context{}); err != nil {
		t.Fatal(err)
	}
}

func TestPersonFollowsTrackers(t *testing.T) {
	c := core.New()
	ticker(c)
	testZones(t, c)
	if _, err := NewPersons(c, []map[string]any{{
		"name":            "Jane Doe",
		"device_trackers": []any{"device_tracker.phone", "device_tracker.router"},
		"picture":         "/local/jane.png",
	}}); err != nil {
		t.Fatal(err)
	}
	id := "person.jane_doe"
	s := c.GetState(id)
	if s == nil || s.State != core.StateUnknown || s.Attributes["id"] != "jane_doe" || s.Attributes["entity_picture"] != "/local/jane.png" {
		t.Fatalf("initial: %+v", s)
	}
	setTracker(t, c, "device_tracker.router", "home", map[string]any{"source_type": "router"})
	if s := c.GetState(id); s.State != "home" || s.Attributes["source"] != "device_tracker.router" {
		t.Fatalf("router home: %+v", s)
	}
	// HA: a stationary tracker that sees the person at home wins over GPS.
	setTracker(t, c, "device_tracker.phone", "Office", map[string]any{"source_type": "gps", "latitude": 52.6, "longitude": 13.4, "gps_accuracy": 15.0})
	if s := c.GetState(id); s.State != "home" || s.Attributes["source"] != "device_tracker.router" {
		t.Fatalf("router home beats gps: %+v", s)
	}
	setTracker(t, c, "device_tracker.router", "not_home", map[string]any{"source_type": "router"})
	s = c.GetState(id)
	if s.State != "Office" || s.Attributes["source"] != "device_tracker.phone" || s.Attributes["latitude"] != 52.6 ||
		s.Attributes["longitude"] != 13.4 || s.Attributes["gps_accuracy"] != 15.0 {
		t.Fatalf("gps used when router is away: %+v", s)
	}
	setTracker(t, c, "device_tracker.phone", "unavailable", map[string]any{"source_type": "gps"})
	if s := c.GetState(id); s.State != "not_home" || s.Attributes["source"] != "device_tracker.router" {
		t.Fatalf("gps unavailable falls back: %+v", s)
	}
}

func TestPersonLatestGPSWins(t *testing.T) {
	c := core.New()
	ticker(c)
	if _, err := NewPersons(c, []map[string]any{{"name": "Sam", "device_trackers": []string{"device_tracker.a", "device_tracker.b"}}}); err != nil {
		t.Fatal(err)
	}
	gps := map[string]any{"source_type": "gps"}
	setTracker(t, c, "device_tracker.a", "home", gps)
	setTracker(t, c, "device_tracker.b", "not_home", gps)
	if s := c.GetState("person.sam"); s.State != "not_home" || s.Attributes["source"] != "device_tracker.b" {
		t.Fatalf("latest: %+v", s)
	}
	setTracker(t, c, "device_tracker.a", "Office", gps)
	if s := c.GetState("person.sam"); s.State != "Office" || s.Attributes["source"] != "device_tracker.a" {
		t.Fatalf("a newer: %+v", s)
	}
}

func TestPersonConfigErrors(t *testing.T) {
	for name, cfg := range map[string][]map[string]any{
		"no name":     {{"device_trackers": []any{"device_tracker.a"}}},
		"bad tracker": {{"name": "A", "device_trackers": []any{"sensor.a"}}},
		"bad list":    {{"name": "A", "device_trackers": 5}},
		"bad id":      {{"name": "A", "id": 5}},
		"duplicate":   {{"name": "A"}, {"name": "a"}},
	} {
		if _, err := NewPersons(core.New(), cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// user_id is accepted and ignored.
	c := core.New()
	if _, err := NewPersons(c, []map[string]any{{"name": "A", "id": "abc", "user_id": "xyz"}}); err != nil {
		t.Fatal(err)
	}
	if s := c.GetState("person.a"); s == nil || s.Attributes["id"] != "abc" || s.Attributes["user_id"] != nil {
		t.Fatalf("%+v", s)
	}
}

func TestZoneCountFollowsPersons(t *testing.T) {
	c := core.New()
	ticker(c)
	testZones(t, c)
	if _, err := NewPersons(c, []map[string]any{
		{"name": "Jane", "device_trackers": []any{"device_tracker.jane"}},
		{"name": "Joe", "device_trackers": []any{"device_tracker.joe"}},
	}); err != nil {
		t.Fatal(err)
	}
	count := func(zone string) string { return c.GetState(zone).State }
	if count("zone.home") != "0" {
		t.Fatalf("initial home: %s", count("zone.home"))
	}
	setTracker(t, c, "device_tracker.jane", "home", nil)
	setTracker(t, c, "device_tracker.joe", "home", nil)
	if count("zone.home") != "2" {
		t.Fatalf("both home: %s", count("zone.home"))
	}
	if p := c.GetState("zone.home").Attributes["persons"]; len(p.([]string)) != 2 {
		t.Fatalf("persons attribute: %v", p)
	}
	setTracker(t, c, "device_tracker.joe", "Office", nil)
	if count("zone.home") != "1" || count("zone.office") != "1" {
		t.Fatalf("joe at office: home %s office %s", count("zone.home"), count("zone.office"))
	}
	setTracker(t, c, "device_tracker.jane", "not_home", nil)
	if count("zone.home") != "0" || count("zone.annex") != "0" {
		t.Fatalf("jane away: home %s", count("zone.home"))
	}
}

func TestZoneCountWhenPersonsExistFirst(t *testing.T) {
	c := core.New()
	ticker(c)
	if _, err := NewPersons(c, []map[string]any{{"name": "Jane", "device_trackers": []any{"device_tracker.jane"}}}); err != nil {
		t.Fatal(err)
	}
	setTracker(t, c, "device_tracker.jane", "home", nil)
	testZones(t, c)
	if s := c.GetState("zone.home"); s.State != "1" {
		t.Fatalf("zone.home: %+v", s)
	}
}

// sunCore returns a core whose clock stands at the given Berlin hour on
// 2026-06-21.
func sunCore(t *testing.T, hour int) (*core.Core, *time.Location) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}
	at := time.Date(2026, 6, 21, hour, 0, 0, 0, loc)
	c := core.New()
	c.Now = func() time.Time { return at }
	return c, loc
}

func parseAttr(t *testing.T, s *core.State, key string) time.Time {
	t.Helper()
	str, ok := s.Attributes[key].(string)
	if !ok {
		t.Fatalf("%s missing: %+v", key, s.Attributes)
	}
	v, err := time.Parse(time.RFC3339, str)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return v
}

func near(t *testing.T, what string, got time.Time, loc *time.Location, hour, min int, day int) {
	t.Helper()
	want := time.Date(2026, 6, day, hour, min, 0, 0, loc)
	if d := got.Sub(want); d < -3*time.Minute || d > 3*time.Minute {
		t.Errorf("%s: got %s, want about %s", what, got.In(loc).Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestSunMidday(t *testing.T) {
	c, loc := sunCore(t, 13)
	s := NewSun(c, berlinLat, berlinLon, loc)
	defer s.Stop()
	st := c.GetState("sun.sun")
	if st == nil || st.State != "above_horizon" {
		t.Fatalf("state: %+v", st)
	}
	if e := st.Attributes["elevation"].(float64); e < 50 || e > 62 {
		t.Errorf("elevation %v", e)
	}
	// Solar noon is about 13:08 CEST, so at 13:00 the sun is still climbing.
	if st.Attributes["rising"] != true {
		t.Errorf("rising: %v", st.Attributes["rising"])
	}
	// Today's sunrise has passed, so the next one is tomorrow's.
	near(t, "next_rising", parseAttr(t, st, "next_rising"), loc, 4, 44, 22)
	near(t, "next_setting", parseAttr(t, st, "next_setting"), loc, 21, 33, 21)
	near(t, "next_dusk", parseAttr(t, st, "next_dusk"), loc, 22, 24, 21)
	near(t, "next_noon", parseAttr(t, st, "next_noon"), loc, 13, 8, 21)
	near(t, "next_midnight", parseAttr(t, st, "next_midnight"), loc, 1, 8, 22)
	if st.Attributes["friendly_name"] != "Sun" {
		t.Errorf("friendly_name: %v", st.Attributes["friendly_name"])
	}
}

func TestSunNight(t *testing.T) {
	c, loc := sunCore(t, 1)
	s := NewSun(c, berlinLat, berlinLon, loc)
	defer s.Stop()
	st := c.GetState("sun.sun")
	if st.State != "below_horizon" {
		t.Fatalf("state: %+v", st)
	}
	if e := st.Attributes["elevation"].(float64); e >= 0 {
		t.Errorf("elevation %v", e)
	}
	// Solar midnight is about 01:08 CEST, so at 01:00 the sun is still sinking.
	if st.Attributes["rising"] != false {
		t.Errorf("rising: %v", st.Attributes["rising"])
	}
	near(t, "next_rising", parseAttr(t, st, "next_rising"), loc, 4, 44, 21)
	near(t, "next_setting", parseAttr(t, st, "next_setting"), loc, 21, 33, 21)
	near(t, "next_dawn", parseAttr(t, st, "next_dawn"), loc, 3, 52, 21)
	near(t, "next_dusk", parseAttr(t, st, "next_dusk"), loc, 22, 24, 21)
}
