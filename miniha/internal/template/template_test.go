/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"testing"
)

// Expected outputs were produced with Home Assistant's Jinja2 environment
// (render with parse_result=False), so they are the compatibility contract.
func TestRenderHACompat(t *testing.T) {
	payload := `{"temperature":21.5,"POWER":"ON","battery":87,"state":{"on":true},"linkquality":0,"color":{"r":255,"g":0,"b":10},"tags":["a","b"],"big":12345678901234567890,"neg":-3,"nil":null}`
	cases := []struct{ tpl, want string }{
		{`{{ value_json.temperature }}`, "21.5"},
		{`{{ value_json.battery }}`, "87"},
		{`{{value_json.POWER}}`, "ON"},
		{`{{ value_json.state.on }}`, "True"},
		{`{{ 'ON' if value_json.state.on else 'OFF' }}`, "ON"},
		{`{% if value_json.temperature > 20 %}hot{% else %}cold{% endif %}`, "hot"},
		{`{% if value_json.missing is defined %}y{% else %}n{% endif %}`, "n"},
		{`{% if value_json.battery is defined %}y{% else %}n{% endif %}`, "y"},
		{`{{ value_json.temperature | round(0) }}`, "22"},
		{`{{ value_json.temperature | round }}`, "22"},
		{`{{ 22.5 | round }}`, "22"},
		{`{{ value_json.temperature | round(1) }}`, "21.5"},
		{`{{ value | float / 10 }}`, "12.3"},
		{`{{ value_json.temperature * 2 }}`, "43.0"},
		{`{{ value_json.battery / 2 }}`, "43.5"},
		{`{{ value_json.battery // 2 }}`, "43"},
		{`{{ value_json.battery % 10 }}`, "7"},
		{`{{ 2 ** 10 }}`, "1024"},
		{`{{ value_json.missing }}`, ""},
		{`{{ value_json.missing | default('x') }}`, "x"},
		{`{{ value_json.missing | d('x') }}`, "x"},
		{`{{ value_json.nil | default('x') }}`, "None"},
		{`{{ value_json.nil | default('x', true) }}`, "x"},
		{`{{ value_json.linkquality }}`, "0"},
		{`{{ value_json.color.r }}`, "255"},
		{`{{ value_json.color }}`, "{'r': 255, 'g': 0, 'b': 10}"},
		{`{{ value_json.tags }}`, "['a', 'b']"},
		{`{{ value_json.tags[1] }}`, "b"},
		{`{{ value_json['color']['g'] }}`, "0"},
		{`{{ value_json.tags | join(',') }}`, "a,b"},
		{`{{ value_json.tags | length }}`, "2"},
		{`{{ value | int }}`, "123"},
		{`{{ value | int + 1 }}`, "124"},
		{`{{ value | float }}`, "123.0"},
		{`{{ 'abc' | upper }}`, "ABC"},
		{`{{ value_json.POWER | lower }}`, "on"},
		{`{{ value_json.POWER == 'ON' }}`, "True"},
		{`{{ value_json.POWER != 'ON' }}`, "False"},
		{`{{ iif(value_json.state.on, 'yes', 'no') }}`, "yes"},
		{`{{ value_json.state.on | iif('yes', 'no') }}`, "yes"},
		{`{{ value_json.big }}`, "12345678901234567890"},
		{`{{ value_json.neg | abs }}`, "3"},
		{`{{ -value_json.neg }}`, "3"},
		{`{{ 1 == 1.0 }}`, "True"},
		{`{{ not value_json.state.on }}`, "False"},
		{`{{ value_json.temperature > 20 and value_json.battery < 90 }}`, "True"},
		{`{{ 'a' ~ 1 ~ 2.5 }}`, "a12.5"},
		{`{{ "x" in value_json.tags }}`, "False"},
		{`{{ value_json.nil is none }}`, "True"},
		{`{{ value_json.temperature is number }}`, "True"},
		{`{{ value_json.POWER is string }}`, "True"},
		{`{{ is_number(value) }}`, "True"},
		{`{{ float(value) }}`, "123.0"},
		{`{{ int('x', 5) }}`, "5"},
		{`{{ float('x', 0) }}`, "0"},
		{`{{ 'x' | float(0) }}`, "0"},
		{`{% set t = value_json.temperature %}{{ t + 0.5 }}`, "22.0"},
		{`{% for x in value_json.tags %}{{ x }}{% endfor %}`, "ab"},
		{`{% for k, v in value_json.color.items() %}{{ k }}={{ v }} {% endfor %}`, "r=255 g=0 b=10 "},
		{`{{ (value_json.temperature * 9 / 5 + 32) | round(1) }}`, "70.7"},
		{`{{ value_json.temperature | multiply(10) | int }}`, "215"},
		{`{{ value_json | to_json }}`, `{"temperature": 21.5, "POWER": "ON", "battery": 87, "state": {"on": true}, "linkquality": 0, "color": {"r": 255, "g": 0, "b": 10}, "tags": ["a", "b"], "big": 12345678901234567890, "neg": -3, "nil": null}`},
		{`{{ '{"a":1}' | from_json }}`, "{'a': 1}"},
		{`{{ value_json.temperature | string }}`, "21.5"},
		{`{{ 1e16 }}`, "1e+16"},
		{`{{ 0.00001 }}`, "1e-05"},
		{`{{ 100000.0 }}`, "100000.0"},
		{`{{ 10 / 2 }}`, "5.0"},
		{`{{ 7 // 2.0 }}`, "3.0"},
		{`{{ -7 // 2 }}`, "-4"},
		{`{{ [1,2,3] | max }}`, "3"},
		{`{{ [1,2,3] | min }}`, "1"},
		{`{{ [1,2,3] | sum }}`, "6"},
		{`{{ 'ON' if value_json.linkquality else 'OFF' }}`, "OFF"},
		{`{{ 'a' if value_json.missing else 'b' }}`, "b"},
		{`{{ value_json.tags | first }}`, "a"},
		{`{{ value_json.tags | last }}`, "b"},
		{`{{ ' x ' | trim }}`, "x"},
		{`{{ 'a-b' | replace('-', '_') }}`, "a_b"},
		{`{{ 'Hello' | title }}`, "Hello"},
		{`{{ 'hello world' | title }}`, "Hello World"},
		{`{{ 'hello' | capitalize }}`, "Hello"},
		{`{{ none }}`, "None"},
		{`{{ None }}`, "None"},
		{`{{ true }}`, "True"},
		{`{# comment #}x`, "x"},
		{`a {{- 1 -}} b`, "a1b"},
		{`{{ value_json.state | is_defined }}`, "{'on': True}"},
		{`{{ value_json.missing is not defined }}`, "True"},
		{`{{ 'ab' * 2 }}`, "abab"},
		{`{{ value_json.temperature | int }}`, "21"},
		{`{{ value_json.temperature | round(0) | int }}`, "22"},
		{`{{ (value_json.battery / 3) | round(2) }}`, "29.0"},
		{`{{ 2.675 | round(2) }}`, "2.67"},
		{`{{ value_json.battery > 50 }}`, "True"},
		{`{{ 1 if 0 else 2 }}`, "2"},
		{`{{ 3 if 1 }}`, "3"},
		{`{{ 3 if 0 }}`, ""},
		{`{{ "quote's" }}`, "quote's"},
		{`{{ value_json.color.keys() | list }}`, "['r', 'g', 'b']"},
		{`{{ 'x' in 'xyz' }}`, "True"},
		{`{{ 'ON' if value_json.POWER == 'ON' else 'OFF' }}`, "ON"},
		{`{{ value_json.POWER[:1] }}`, "O"},
		{`{{ 'abcdef'[2:4] }}`, "cd"},
		{`{{ 'abcdef'[-2:] }}`, "ef"},
		{`{{ 'abc'[:250] }}`, "abc"},
		{`{{ value_json.tags[1:] }}`, "['b']"},
		{`{{ value_json.color | tojson }}`, `{"b": 10, "g": 0, "r": 255}`},
		{`{{ "<a>" | tojson }}`, `"\u003ca\u003e"`},
	}
	for _, c := range cases {
		tpl, err := Parse(c.tpl)
		if err != nil {
			t.Errorf("%s: parse: %v", c.tpl, err)
			continue
		}
		got, err := tpl.RenderWithPossibleJSON("123", []byte(payload), nil)
		if err != nil {
			t.Errorf("%s: render: %v", c.tpl, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.tpl, got, c.want)
		}
	}
}

func TestRenderNonJSONValue(t *testing.T) {
	tpl, err := Parse(`{{ value | float * 2 }}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tpl.RenderWithPossibleJSON("1.5", []byte("1.5"), nil)
	if err != nil || got != "3.0" {
		t.Fatalf("got %q err %v", got, err)
	}
	// Access to value_json when the payload is not JSON is an error, as in HA.
	tpl, _ = Parse(`{{ value_json.x }}`)
	if _, err := tpl.RenderWithPossibleJSON("notjson", []byte("notjson"), nil); err == nil {
		t.Fatal("expected error for value_json on non-JSON payload")
	}
}

func TestErrors(t *testing.T) {
	for _, s := range []string{`{{ `, `{% if %}`, `{{ 1 +* 2 }}`, `{% for %}`, `{{ x | nosuchfilter }}`} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q: expected parse error", s)
		}
	}
	tpl, _ := Parse(`{{ value_json.missing.deeper }}`)
	if _, err := tpl.RenderWithPossibleJSON("", []byte(`{}`), nil); err == nil {
		t.Error("attribute of undefined must be an error")
	}
	tpl, _ = Parse(`{{ 'a' > 1 }}`)
	if _, err := tpl.RenderWithPossibleJSON("", []byte(`{}`), nil); err == nil {
		t.Error("comparing str with int must be an error")
	}
}

func TestIsTemplate(t *testing.T) {
	if IsTemplate("plain") || !IsTemplate("{{ x }}") || !IsTemplate("{% if x %}") {
		t.Fatal("IsTemplate wrong")
	}
}
