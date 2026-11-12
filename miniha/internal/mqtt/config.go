/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/probonopd/miniha/internal/template"
)

// Config is a discovery payload after abbreviation expansion. Values keep
// their JSON types (json.Number for numbers) and are coerced on access with
// Python str() semantics, matching HA's cv.string.
type Config map[string]any

func parseConfig(payload []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("discovery payload is not a JSON object: %w", err)
	}
	return expand(raw), nil
}

func expand(raw map[string]any) Config {
	cfg := make(Config, len(raw))
	for k, v := range raw {
		if full, ok := abbreviations[k]; ok {
			k = full
		}
		cfg[k] = v
	}
	if dev, ok := cfg["device"].(map[string]any); ok {
		cfg["device"] = expandWith(dev, deviceAbbreviations)
	}
	if o, ok := cfg["origin"].(map[string]any); ok {
		cfg["origin"] = expandWith(o, originAbbreviations)
	}
	if list, ok := cfg["availability"].([]any); ok {
		for i, item := range list {
			if m, ok := item.(map[string]any); ok {
				list[i] = expandWith(m, abbreviations)
			}
		}
	}
	if base, ok := cfg["~"].(string); ok {
		delete(cfg, "~")
		for k, v := range cfg {
			if s, ok := v.(string); ok && strings.HasSuffix(k, "topic") {
				cfg[k] = applyBase(s, base)
			}
		}
		if list, ok := cfg["availability"].([]any); ok {
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					if s, ok := m["topic"].(string); ok {
						m["topic"] = applyBase(s, base)
					}
				}
			}
		}
	}
	return cfg
}

func expandWith(m map[string]any, table map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if full, ok := table[k]; ok {
			k = full
		}
		out[k] = v
	}
	return out
}

func applyBase(s, base string) string {
	if strings.HasPrefix(s, "~") {
		return base + s[1:]
	}
	if strings.HasSuffix(s, "~") {
		return s[:len(s)-1] + base
	}
	return s
}

func coerce(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	return template.PyStr(v)
}

func (c Config) Has(key string) bool {
	v, ok := c[key]
	return ok && v != nil
}

// Str returns the value coerced to a string, "" when absent or null.
func (c Config) Str(key string) string { return coerce(c[key]) }

func (c Config) StrDef(key, def string) string {
	if !c.Has(key) {
		return def
	}
	return coerce(c[key])
}

func (c Config) Bool(key string, def bool) bool {
	v, ok := c[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(x) {
		case "true", "on", "yes", "1":
			return true
		case "false", "off", "no", "0":
			return false
		}
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	}
	return def
}

func (c Config) Float(key string, def float64) float64 {
	v, ok := c[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err == nil {
			return f
		}
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return f
		}
	case bool:
		if x {
			return 1
		}
		return 0
	// Synthesized configs (Tasmota discovery) carry plain Go numbers.
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return def
}

func (c Config) Int(key string, def int) int {
	return int(c.Float(key, float64(def)))
}

func (c Config) Strings(key string) []string {
	switch x := c[key].(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, coerce(e))
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}

func (c Config) Map(key string) Config {
	if m, ok := c[key].(map[string]any); ok {
		return Config(m)
	}
	return nil
}

// Template parses a template option; nil when the option is absent.
func (c Config) Template(key string) (*template.Template, error) {
	if !c.Has(key) {
		return nil, nil
	}
	t, err := template.Parse(c.Str(key))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return t, nil
}
