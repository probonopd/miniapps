/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"strings"
	"unicode"
)

type tokKind int

const (
	tText tokKind = iota
	tVarBegin
	tVarEnd
	tBlockBegin
	tBlockEnd
	tName
	tInt
	tFloat
	tString
	tOp
	tEOF
)

type token struct {
	kind tokKind
	val  string
	pos  int
}

func (t token) String() string {
	if t.kind == tEOF {
		return "end of template"
	}
	return fmt.Sprintf("%q", t.val)
}

// Longest operators first so that "//" wins over "/" and "**" over "*".
var operators = []string{"**", "//", "==", "!=", "<=", ">=", "+", "-", "*", "/", "%", "~", "|", "(", ")", "[", "]", "{", "}", ",", ":", ".", "<", ">", "="}

// lex splits a template into text chunks and expression tokens. Whitespace
// control ("{{-", "-}}") is resolved here so the parser never sees it.
func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		start := strings.Index(src[i:], "{")
		if start < 0 {
			toks = append(toks, token{tText, src[i:], i})
			break
		}
		start += i
		if start+1 >= len(src) || (src[start+1] != '{' && src[start+1] != '%' && src[start+1] != '#') {
			// A lone brace is text.
			toks = append(toks, token{tText, src[i : start+1], i})
			i = start + 1
			continue
		}
		if start > i {
			toks = append(toks, token{tText, src[i:start], i})
		}
		kind := src[start+1]
		j := start + 2
		if j < len(src) && src[j] == '-' {
			// Strip whitespace at the end of the preceding text.
			if n := len(toks); n > 0 && toks[n-1].kind == tText {
				toks[n-1].val = strings.TrimRightFunc(toks[n-1].val, unicode.IsSpace)
			}
			j++
		}
		if kind == '#' {
			end := strings.Index(src[j:], "#}")
			if end < 0 {
				return nil, fmt.Errorf("unclosed comment at %d", start)
			}
			i = j + end + 2
			continue
		}
		closer := "}}"
		open, close := tVarBegin, tVarEnd
		if kind == '%' {
			closer = "%}"
			open, close = tBlockBegin, tBlockEnd
		}
		toks = append(toks, token{open, src[start : start+2], start})
		var stripAfter bool
		for {
			// Skip whitespace inside the tag.
			for j < len(src) && unicode.IsSpace(rune(src[j])) {
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unclosed tag at %d", start)
			}
			if strings.HasPrefix(src[j:], closer) {
				j += 2
				break
			}
			if strings.HasPrefix(src[j:], "-"+closer) {
				stripAfter = true
				j += 3
				break
			}
			tok, n, err := lexExprToken(src, j)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			j += n
		}
		toks = append(toks, token{close, closer, j - 2})
		if stripAfter {
			k := j
			for k < len(src) && unicode.IsSpace(rune(src[k])) {
				k++
			}
			j = k
		}
		i = j
	}
	toks = append(toks, token{tEOF, "", len(src)})
	return toks, nil
}

func lexExprToken(src string, i int) (token, int, error) {
	c := src[i]
	switch {
	case c == '\'' || c == '"':
		var sb strings.Builder
		j := i + 1
		for j < len(src) && src[j] != c {
			if src[j] == '\\' && j+1 < len(src) {
				j++
				switch src[j] {
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				default:
					sb.WriteByte(src[j])
				}
				j++
				continue
			}
			sb.WriteByte(src[j])
			j++
		}
		if j >= len(src) {
			return token{}, 0, fmt.Errorf("unterminated string at %d", i)
		}
		return token{tString, sb.String(), i}, j + 1 - i, nil
	case c >= '0' && c <= '9' || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
		j := i
		isFloat := false
		for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == '_') {
			if src[j] == '.' {
				// "1.5.x" is not a number; stop at the second dot.
				if isFloat {
					break
				}
				isFloat = true
			}
			j++
		}
		if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
			k := j + 1
			if k < len(src) && (src[k] == '+' || src[k] == '-') {
				k++
			}
			if k < len(src) && src[k] >= '0' && src[k] <= '9' {
				for k < len(src) && src[k] >= '0' && src[k] <= '9' {
					k++
				}
				j = k
				isFloat = true
			}
		}
		kind := tInt
		if isFloat {
			kind = tFloat
		}
		return token{kind, strings.ReplaceAll(src[i:j], "_", ""), i}, j - i, nil
	case c == '_' || unicode.IsLetter(rune(c)):
		j := i
		for j < len(src) && (src[j] == '_' || unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j]))) {
			j++
		}
		return token{tName, src[i:j], i}, j - i, nil
	}
	for _, op := range operators {
		if strings.HasPrefix(src[i:], op) {
			return token{tOp, op, i}, len(op), nil
		}
	}
	return token{}, 0, fmt.Errorf("unexpected character %q at %d", c, i)
}
