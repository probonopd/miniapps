/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// blueprintSubdir is where automation blueprints live below the data
// directory, as in HA.
var blueprintSubdir = filepath.Join("blueprints", "automation")

type useBlueprint struct {
	path  string
	input map[string]any
}

// parseUseBlueprint checks the use_blueprint mapping of an automation.
func parseUseBlueprint(raw node) (useBlueprint, error) {
	m, ok := asMap(raw)
	if !ok {
		return useBlueprint{}, fmt.Errorf("use_blueprint must be a mapping with path and input")
	}
	u := useBlueprint{path: str(m["path"])}
	if u.path == "" {
		return u, fmt.Errorf("use_blueprint needs path")
	}
	// The path comes from a user-editable file and is joined below the
	// blueprint directory, so it must not climb out of it.
	if !filepath.IsLocal(u.path) {
		return u, fmt.Errorf("use_blueprint path %q must be relative and stay inside the blueprints directory", u.path)
	}
	if in, ok := m["input"]; ok && in != nil {
		im, ok := asMap(in)
		if !ok {
			return u, fmt.Errorf("use_blueprint input must be a mapping")
		}
		u.input = im
	}
	for k := range m {
		if k != "path" && k != "input" {
			return u, fmt.Errorf("use_blueprint: unknown key %q", k)
		}
	}
	return u, nil
}

func blueprintLabel(m map[string]any) string {
	if a := str(m["alias"]); a != "" {
		return a
	}
	return str(m["id"])
}

// inputTag matches an "!input name" tag. The goccy parser does not know
// the tag, so the substitution is textual, before parsing.
var inputTag = regexp.MustCompile(`(^|[\s\[{,])!input[ \t]+([A-Za-z0-9_-]+)`)

// substituteInputs replaces every !input tag with the input's value in
// YAML flow syntax. lookup reports an error for undeclared inputs.
func substituteInputs(src string, lookup func(name string) (any, error)) (string, error) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		var firstErr error
		lines[i] = inputTag.ReplaceAllStringFunc(line, func(match string) string {
			sub := inputTag.FindStringSubmatch(match)
			v, err := lookup(sub[2])
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return match
			}
			text, err := yamlFlow(v)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("input %q: %w", sub[2], err)
				}
				return match
			}
			return sub[1] + text
		})
		if firstErr != nil {
			return "", firstErr
		}
	}
	return strings.Join(lines, "\n"), nil
}

// yamlFlow renders a value as single-line YAML flow syntax. Scalars use
// their JSON form, which is also valid YAML; collections are written
// with spaces after the separators so that every YAML parser reads the
// keys as plain strings.
func yamlFlow(v any) (string, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			key, err := yamlFlow(k)
			if err != nil {
				return "", err
			}
			val, err := yamlFlow(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, key+": "+val)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case []any:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			val, err := yamlFlow(it)
			if err != nil {
				return "", err
			}
			parts = append(parts, val)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Templates contain < > & that must stay readable.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// blueprintInputs collects the declared inputs, descending into the
// sections newer blueprints group them in.
func blueprintInputs(decl map[string]any, into map[string]map[string]any) {
	for name, raw := range decl {
		def, _ := asMap(raw)
		if sub, ok := asMap(def["input"]); ok {
			blueprintInputs(sub, into)
			continue
		}
		into[name] = def
	}
}

// expandBlueprint loads the blueprint, substitutes its inputs and
// returns an automation mapping. Keys of the automation entry (alias,
// description, mode, ...) override those of the blueprint.
func expandBlueprint(dataDir string, use useBlueprint, entry map[string]any) (map[string]any, error) {
	file := filepath.Join(dataDir, blueprintSubdir, use.path)
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("blueprint %q: %w", use.path, err)
	}
	src := string(raw)

	// The header is read first with the tags neutralized, to learn which
	// inputs exist and what their defaults are.
	probe, err := substituteInputs(src, func(string) (any, error) { return nil, nil })
	if err != nil {
		return nil, err
	}
	var head map[string]any
	if err := yaml.Unmarshal([]byte(probe), &head); err != nil {
		return nil, fmt.Errorf("blueprint %q: %w", use.path, err)
	}
	bp, ok := asMap(head["blueprint"])
	if !ok {
		return nil, fmt.Errorf("blueprint %q: missing blueprint: section", use.path)
	}
	if d := str(bp["domain"]); d != "automation" {
		return nil, fmt.Errorf("blueprint %q: domain is %q, want automation", use.path, d)
	}
	declared := map[string]map[string]any{}
	if decl, ok := asMap(bp["input"]); ok {
		blueprintInputs(decl, declared)
	}
	for name := range use.input {
		if _, ok := declared[name]; !ok {
			return nil, fmt.Errorf("blueprint %q has no input %q", use.path, name)
		}
	}
	values := map[string]any{}
	for name, def := range declared {
		if v, ok := use.input[name]; ok {
			values[name] = v
		} else if d, ok := def["default"]; ok {
			values[name] = d
		} else {
			return nil, fmt.Errorf("blueprint %q: input %q is required and has no default", use.path, name)
		}
	}
	final, err := substituteInputs(src, func(name string) (any, error) {
		v, ok := values[name]
		if !ok {
			return nil, fmt.Errorf("blueprint %q uses undeclared input %q", use.path, name)
		}
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(final), &doc); err != nil {
		return nil, fmt.Errorf("blueprint %q after input substitution: %w", use.path, err)
	}
	delete(doc, "blueprint")
	for k, v := range entry {
		if k != "use_blueprint" {
			doc[k] = v
		}
	}
	return doc, nil
}

// blueprintSignature identifies the blueprint files so that the file
// watcher can reload when one is edited, added or removed.
func blueprintSignature(dataDir string) string {
	var b strings.Builder
	root := filepath.Join(dataDir, blueprintSubdir)
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s|%d|%d\n", path, info.ModTime().UnixNano(), info.Size())
		}
		return nil
	})
	return b.String()
}
