/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import "testing"

func TestAddManualSensor(t *testing.T) {
	c, tr, d := setup(t)
	err := d.AddManual("sensor", map[string]any{
		"name":                "Outdoor temperature",
		"state_topic":         "garden/temp",
		"value_template":      "{{ value_json.t }}",
		"unit_of_measurement": "C",
		"expire_after":        uint64(0), // YAML integers arrive as Go integers
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.inject("garden/temp", `{"t": 21.5}`)
	s := state(t, c, "sensor.outdoor_temperature")
	if s.State != "21.5" || s.Attributes["unit_of_measurement"] != "C" {
		t.Fatalf("%+v", s)
	}
	// A second entity of the same component gets its own id.
	if err := d.AddManual("sensor", map[string]any{"name": "Indoor", "stat_t": "house/temp"}); err != nil {
		t.Fatal(err)
	}
	tr.inject("house/temp", "19")
	if s := state(t, c, "sensor.indoor"); s.State != "19" {
		t.Fatalf("%+v", s)
	}
}

func TestAddManualRejects(t *testing.T) {
	_, _, d := setup(t)
	if err := d.AddManual("no_such_component", map[string]any{"name": "x"}); err == nil {
		t.Fatal("unknown component accepted")
	}
	if err := d.AddManual("sensor", map[string]any{"name": "no topic"}); err == nil {
		t.Fatal("sensor without state_topic accepted")
	}
}
