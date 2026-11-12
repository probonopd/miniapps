/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"strings"
	"testing"
	"time"
)

// fakeState is the shape the automation engine exposes as states.x.y.
type fakeState struct {
	id, state string
	attrs     map[string]any
	changed   time.Time
}

func (f fakeState) TemplateAttr(name string) (any, bool) {
	switch name {
	case "entity_id":
		return f.id, true
	case "state":
		return f.state, true
	case "attributes":
		return FromGo(f.attrs), true
	case "last_changed":
		return DateTime{f.changed}, true
	}
	return nil, false
}

type fakeStates struct{ list []fakeState }

// states.<domain> yields the domain's states; states['domain.id'] one state.
func (s fakeStates) TemplateAttr(name string) (any, bool) {
	var dom fakeStates
	for _, st := range s.list {
		if st.id == name {
			return st, true
		}
		if strings.HasPrefix(st.id, name+".") {
			dom.list = append(dom.list, st)
		}
	}
	if len(dom.list) > 0 {
		return fakeDomain{dom, name}, true
	}
	return nil, false
}

type fakeDomain struct {
	fakeStates
	domain string
}

func (d fakeDomain) TemplateAttr(name string) (any, bool) {
	return d.fakeStates.TemplateAttr(d.domain + "." + name)
}
func (s fakeStates) TemplateItems() []any {
	out := []any{}
	for _, st := range s.list {
		out = append(out, st)
	}
	return out
}
func (s fakeStates) TemplateCall(args []any, _ map[string]any) (any, error) {
	if v, ok := s.TemplateAttr(pystr(args[0])); ok {
		return v.(fakeState).state, nil
	}
	return "unknown", nil
}

func TestHostObjects(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 5, 14, 30, 15, 123456000, loc)
	Clock = func() time.Time { return now }
	defer func() { Clock = time.Now }()
	states := fakeStates{[]fakeState{
		{"light.bulb", "on", map[string]any{"brightness": 128, "friendly_name": "Bulb"}, now.Add(-90 * time.Second)},
		{"light.lamp", "off", map[string]any{"friendly_name": "Lamp"}, now.Add(-time.Hour)},
		{"sensor.t", "21.5", map[string]any{"unit_of_measurement": "C"}, now},
	}}
	env := &Env{Globals: map[string]any{
		"states": states,
		"is_state": Func(func(args []any, _ map[string]any) (any, error) {
			v, _ := states.TemplateCall(args[:1], nil)
			return v == pystr(args[1]), nil
		}),
	}}
	cases := []struct{ tpl, want string }{
		{`{{ states.light.bulb.state }}`, "on"},
		{`{{ states('light.bulb') }}`, "on"},
		{`{{ states('light.nope') }}`, "unknown"},
		{`{{ states.light.bulb.attributes.brightness }}`, "128"},
		{`{{ state_attr_missing is not defined }}`, "True"},
		{`{{ is_state('light.bulb', 'on') }}`, "True"},
		{`{{ states['sensor.t'].state | float > 20 }}`, "True"},
		{`{{ now().hour }}:{{ now().minute }}`, "14:30"},
		{`{{ now() }}`, "2026-11-05 14:30:15.123456+01:00"},
		{`{{ now().isoformat() }}`, "2026-11-05T14:30:15.123456+01:00"},
		{`{{ now().strftime('%Y-%m-%d %H:%M %A') }}`, "2026-11-05 14:30 Thursday"},
		{`{{ now().weekday() }}`, "3"},
		{`{{ (now() - states.light.bulb.last_changed).total_seconds() }}`, "90.0"},
		{`{{ now() - states.light.bulb.last_changed }}`, "0:01:30"},
		{`{{ now() - states.light.lamp.last_changed > timedelta(minutes=30) }}`, "True"},
		{`{{ (now() + timedelta(hours=1)).hour }}`, "15"},
		{`{{ now() > states.light.bulb.last_changed }}`, "True"},
		{`{{ as_timestamp(now()) | int }}`, "1793885415"},
		{`{{ as_timestamp('2026-11-05T13:30:15+00:00') | timestamp_custom('%H:%M') }}`, "14:30"},
		{`{{ as_timestamp('2026-11-05T13:30:15+00:00') | timestamp_custom('%H:%M', false) }}`, "13:30"},
		{`{{ as_datetime('2026-01-02T03:04:05+00:00').day }}`, "2"},
		{`{{ 'garbage' | as_timestamp(0) }}`, "0"},
		{`{{ states.light | map(attribute='state') | list }}`, "['on', 'off']"},
		{`{{ states.light | selectattr('state', 'eq', 'on') | map(attribute='entity_id') | join(',') }}`, "light.bulb"},
		{`{{ states.light | selectattr('state', '==', 'off') | list | count }}`, "1"},
		{`{{ states.light | rejectattr('state', 'eq', 'off') | map(attribute='attributes.friendly_name') | list }}`, "['Bulb']"},
		{`{{ states.light | map(attribute='attributes.brightness', default=0) | list }}`, "[128, 0]"},
		{`{{ [3,1,2] | sort }}`, "[1, 2, 3]"},
		{`{{ [3,1,2] | sort(reverse=true) }}`, "[3, 2, 1]"},
		{`{{ ['b','a','b'] | unique | list }}`, "['b', 'a']"},
		{`{{ [1,2,3] | reverse | list }}`, "[3, 2, 1]"},
		{`{{ ['a','B'] | map('upper') | list }}`, "['A', 'B']"},
		{`{{ [0, 1, '', 'x'] | select | list }}`, "[1, 'x']"},
		{`{{ [1, 5, 10] | select('>', 3) | list }}`, "[5, 10]"},
		{`{% for s in states.light %}{{ s.entity_id }} {% endfor %}`, "light.bulb light.lamp "},
		{`{{ states.light | list | length }}`, "2"},
		{`{{ timedelta(days=1, hours=2) }}`, "1 day, 2:00:00"},
		{`{{ timedelta(seconds=90).seconds }}`, "90"},
		{`{{ now().replace(hour=0, minute=0).hour }}`, "0"},
	}
	for _, c := range cases {
		tpl, err := Parse(c.tpl)
		if err != nil {
			t.Errorf("%s: parse: %v", c.tpl, err)
			continue
		}
		got, err := tpl.Render(nil, env)
		if err != nil {
			t.Errorf("%s: render: %v", c.tpl, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.tpl, got, c.want)
		}
	}
}
