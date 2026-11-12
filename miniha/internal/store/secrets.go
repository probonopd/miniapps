/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

var secretTag = regexp.MustCompile(`!secret[ \t]+([A-Za-z0-9_.-]+)`)

// substituteSecrets replaces every "!secret <name>" in configuration text
// with the value from secrets.yaml in dir. It works on the text instead
// of on custom YAML tag handling so the strict decoder never sees the tag.
// secrets.yaml is only read when a tag is present, so a configuration
// without secrets needs no such file.
func substituteSecrets(data []byte, dir string) ([]byte, error) {
	if !bytes.Contains(data, []byte("!secret")) {
		return data, nil
	}
	secrets, err := loadSecrets(filepath.Join(dir, "secrets.yaml"))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(data), "\n") {
		code, comment := splitComment(line)
		var missing error
		code = secretTag.ReplaceAllStringFunc(code, func(m string) string {
			name := secretTag.FindStringSubmatch(m)[1]
			v, ok := secrets[name]
			if !ok {
				missing = fmt.Errorf("!secret %s: no such key in secrets.yaml", name)
				return m
			}
			lit, err := secretLiteral(name, v)
			if err != nil {
				missing = err
				return m
			}
			return lit
		})
		if missing != nil {
			return nil, missing
		}
		out.WriteString(code)
		out.WriteString(comment)
	}
	return out.Bytes(), nil
}

func loadSecrets(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration uses !secret but secrets.yaml cannot be read: %w", err)
	}
	secrets := map[string]any{}
	if err := yaml.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("secrets.yaml: %w", err)
	}
	return secrets, nil
}

// secretLiteral renders a secret as a YAML scalar. Strings are always
// double-quoted so that characters such as ":" or "#" in a password can
// not change the document structure; numbers and booleans stay unquoted
// so they keep their type for options like ports.
func secretLiteral(name string, v any) (string, error) {
	switch t := v.(type) {
	case string:
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(t); err != nil {
			return "", err
		}
		// JSON string syntax is a subset of YAML double-quoted scalars.
		return strings.TrimSuffix(b.String(), "\n"), nil
	case bool, int, int64, uint64, float64:
		return fmt.Sprint(t), nil
	case nil:
		return "", fmt.Errorf("!secret %s: value is empty", name)
	default:
		return "", errors.New("!secret " + name + ": secrets.yaml values must be scalars")
	}
}

// splitComment separates a line into its YAML part and a trailing
// comment, honoring quotes so that "#" inside a quoted value is kept.
// Secrets named in comments must not be looked up.
func splitComment(line string) (code, comment string) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i], line[i:]
		}
	}
	return line, ""
}
