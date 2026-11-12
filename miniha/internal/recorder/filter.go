/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package recorder

import (
	"fmt"
	"path"
	"strings"
)

type matchKind int

const (
	matchEntity matchKind = iota
	matchDomain
	matchGlob
)

type matcher struct {
	kind matchKind
	pat  string
}

func (m matcher) match(entityID string) bool {
	switch m.kind {
	case matchEntity:
		return entityID == m.pat
	case matchDomain:
		return strings.HasPrefix(entityID, m.pat+".")
	default:
		ok, _ := path.Match(m.pat, entityID)
		return ok
	}
}

// filter implements the recorder include/exclude lists: with an include
// list only matching entities are recorded, and exclude always wins.
type filter struct {
	include []matcher
	exclude []matcher
}

func parseMatchers(list []string) ([]matcher, error) {
	var out []matcher
	for _, e := range list {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
			return nil, fmt.Errorf("empty entry")
		case strings.ContainsAny(e, "*?["):
			// Surface a malformed pattern at startup, not as a silent
			// never-matching rule.
			if _, err := path.Match(e, ""); err != nil {
				return nil, fmt.Errorf("glob %q: %w", e, err)
			}
			out = append(out, matcher{matchGlob, e})
		case strings.Contains(e, "."):
			out = append(out, matcher{matchEntity, e})
		default:
			out = append(out, matcher{matchDomain, e})
		}
	}
	return out, nil
}

func newFilter(include, exclude []string) (filter, error) {
	var f filter
	var err error
	if f.include, err = parseMatchers(include); err != nil {
		return f, fmt.Errorf("recorder include: %w", err)
	}
	if f.exclude, err = parseMatchers(exclude); err != nil {
		return f, fmt.Errorf("recorder exclude: %w", err)
	}
	return f, nil
}

func (f filter) allows(entityID string) bool {
	for _, m := range f.exclude {
		if m.match(entityID) {
			return false
		}
	}
	if len(f.include) == 0 {
		return true
	}
	for _, m := range f.include {
		if m.match(entityID) {
			return true
		}
	}
	return false
}
