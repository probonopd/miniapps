/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"math/big"
	"strconv"
)

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}

func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.val == s
}

func (p *parser) isName(s string) bool {
	t := p.peek()
	return t.kind == tName && t.val == s
}

func (p *parser) expectOp(s string) error {
	t := p.next()
	if t.kind != tOp || t.val != s {
		return fmt.Errorf("expected %q, got %s at %d", s, t, t.pos)
	}
	return nil
}

func (p *parser) expectKind(k tokKind, what string) (token, error) {
	t := p.next()
	if t.kind != k {
		return t, fmt.Errorf("expected %s, got %s at %d", what, t, t.pos)
	}
	return t, nil
}

// parseBody parses nodes until one of the given block keywords starts a
// block tag; that tag is left for the caller.
func (p *parser) parseBody(until ...string) ([]node, error) {
	var nodes []node
	for {
		t := p.peek()
		switch t.kind {
		case tEOF:
			if len(until) > 0 {
				return nil, fmt.Errorf("unexpected end of template, expected one of %v", until)
			}
			return nodes, nil
		case tText:
			p.next()
			nodes = append(nodes, textNode{t.val})
		case tVarBegin:
			p.next()
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expectKind(tVarEnd, "'}}'"); err != nil {
				return nil, err
			}
			nodes = append(nodes, outputNode{e})
		case tBlockBegin:
			kw := p.toks[p.pos+1]
			if kw.kind != tName {
				return nil, fmt.Errorf("expected block keyword at %d", kw.pos)
			}
			for _, u := range until {
				if kw.val == u {
					return nodes, nil
				}
			}
			p.next()
			p.next()
			n, err := p.parseBlock(kw.val)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, n)
		default:
			return nil, fmt.Errorf("unexpected %s at %d", t, t.pos)
		}
	}
}

func (p *parser) endBlock() error {
	_, err := p.expectKind(tBlockEnd, "'%}'")
	return err
}

// skipBlockTag consumes "{% name %}" where name was already peeked.
func (p *parser) skipBlockTag() error {
	p.next()
	p.next()
	return p.endBlock()
}

func (p *parser) parseBlock(kw string) (node, error) {
	switch kw {
	case "if":
		n := &ifNode{}
		for {
			c, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.endBlock(); err != nil {
				return nil, err
			}
			body, err := p.parseBody("elif", "else", "endif")
			if err != nil {
				return nil, err
			}
			n.conds = append(n.conds, c)
			n.bodies = append(n.bodies, body)
			next := p.toks[p.pos+1].val
			if next == "elif" {
				p.next()
				p.next()
				continue
			}
			if next == "else" {
				if err := p.skipBlockTag(); err != nil {
					return nil, err
				}
				n.els, err = p.parseBody("endif")
				if err != nil {
					return nil, err
				}
			}
			return n, p.skipBlockTag()
		}
	case "for":
		n := &forNode{}
		for {
			t, err := p.expectKind(tName, "loop variable")
			if err != nil {
				return nil, err
			}
			n.vars = append(n.vars, t.val)
			if !p.isOp(",") {
				break
			}
			p.next()
		}
		if !p.isName("in") {
			return nil, fmt.Errorf("expected 'in' at %d", p.peek().pos)
		}
		p.next()
		var err error
		n.iter, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.endBlock(); err != nil {
			return nil, err
		}
		n.body, err = p.parseBody("else", "endfor")
		if err != nil {
			return nil, err
		}
		if p.toks[p.pos+1].val == "else" {
			if err := p.skipBlockTag(); err != nil {
				return nil, err
			}
			n.els, err = p.parseBody("endfor")
			if err != nil {
				return nil, err
			}
		}
		return n, p.skipBlockTag()
	case "set":
		t, err := p.expectKind(tName, "variable name")
		if err != nil {
			return nil, err
		}
		if err := p.expectOp("="); err != nil {
			return nil, err
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		return &setNode{t.val, e}, p.endBlock()
	}
	return nil, fmt.Errorf("unknown block %q", kw)
}

// Expression grammar follows Jinja2's precedence:
// ternary < or < and < not < compare < +,- < ~ < *,/,//,% < ** < unary < postfix.

func (p *parser) parseExpr() (expr, error) {
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.isName("if") {
		p.next()
		c, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		var b expr
		if p.isName("else") {
			p.next()
			b, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
		return &condExpr{c, e, b}, nil
	}
	return e, nil
}

func (p *parser) parseOr() (expr, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isName("or") {
		p.next()
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{"or", l, r}
	}
	return l, nil
}

func (p *parser) parseAnd() (expr, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isName("and") {
		p.next()
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{"and", l, r}
	}
	return l, nil
}

func (p *parser) parseNot() (expr, error) {
	if p.isName("not") {
		p.next()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &unaryExpr{"not", x}, nil
	}
	return p.parseCompare()
}

func (p *parser) parseCompare() (expr, error) {
	l, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		var op string
		switch {
		case t.kind == tOp && (t.val == "==" || t.val == "!=" || t.val == "<" || t.val == ">" || t.val == "<=" || t.val == ">="):
			op = t.val
			p.next()
		case t.kind == tName && t.val == "in":
			op = "in"
			p.next()
		case t.kind == tName && t.val == "not" && p.toks[p.pos+1].kind == tName && p.toks[p.pos+1].val == "in":
			op = "not in"
			p.next()
			p.next()
		default:
			return l, nil
		}
		r, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op, l, r}
	}
}

func (p *parser) parseAdd() (expr, error) {
	l, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().val
		r, err := p.parseConcat()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op, l, r}
	}
	return l, nil
}

func (p *parser) parseConcat() (expr, error) {
	l, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for p.isOp("~") {
		p.next()
		r, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{"~", l, r}
	}
	return l, nil
}

func (p *parser) parseMul() (expr, error) {
	l, err := p.parsePow()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("//") || p.isOp("%") {
		op := p.next().val
		r, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op, l, r}
	}
	return l, nil
}

func (p *parser) parsePow() (expr, error) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if p.isOp("**") {
		p.next()
		r, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		return &binaryExpr{"**", l, r}, nil
	}
	return l, nil
}

func (p *parser) parseUnary() (expr, error) {
	if p.isOp("-") || p.isOp("+") {
		op := p.next().val
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryExpr{op, x}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (expr, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			p.next()
			t := p.next()
			if t.kind != tName && t.kind != tInt {
				return nil, fmt.Errorf("expected attribute name at %d", t.pos)
			}
			e = &attrExpr{e, t.val}
		case p.isOp("["):
			p.next()
			// Python slice: [start:stop], either side optional.
			var start, stop expr
			var err error
			if !p.isOp(":") {
				start, err = p.parseExpr()
				if err != nil {
					return nil, err
				}
			}
			if p.isOp(":") {
				p.next()
				if !p.isOp("]") {
					stop, err = p.parseExpr()
					if err != nil {
						return nil, err
					}
				}
				if err := p.expectOp("]"); err != nil {
					return nil, err
				}
				e = &sliceExpr{e, start, stop}
				continue
			}
			if err := p.expectOp("]"); err != nil {
				return nil, err
			}
			e = &indexExpr{e, start}
		case p.isOp("("):
			args, kwargs, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			e = &callExpr{e, args, kwargs}
		case p.isOp("|"):
			p.next()
			t, err := p.expectKind(tName, "filter name")
			if err != nil {
				return nil, err
			}
			if _, ok := filters[t.val]; !ok {
				return nil, fmt.Errorf("unknown filter %q", t.val)
			}
			f := &filterExpr{e: e, name: t.val}
			if p.isOp("(") {
				f.args, f.kwargs, err = p.parseArgs()
				if err != nil {
					return nil, err
				}
			}
			e = f
		case p.isName("is"):
			p.next()
			neg := false
			if p.isName("not") {
				p.next()
				neg = true
			}
			t, err := p.expectKind(tName, "test name")
			if err != nil {
				return nil, err
			}
			if _, ok := tests[t.val]; !ok {
				return nil, fmt.Errorf("unknown test %q", t.val)
			}
			te := &testExpr{e: e, name: t.val, negate: neg}
			if p.isOp("(") {
				te.args, _, err = p.parseArgs()
				if err != nil {
					return nil, err
				}
			} else if t := p.peek(); t.kind == tInt || t.kind == tFloat || t.kind == tString {
				// "x is divisibleby 3": a single bare argument.
				a, err := p.parsePrimary()
				if err != nil {
					return nil, err
				}
				te.args = []expr{a}
			}
			e = te
		default:
			return e, nil
		}
	}
}

func (p *parser) parseArgs() ([]expr, map[string]expr, error) {
	if err := p.expectOp("("); err != nil {
		return nil, nil, err
	}
	var args []expr
	var kwargs map[string]expr
	for !p.isOp(")") {
		if p.peek().kind == tName && p.toks[p.pos+1].kind == tOp && p.toks[p.pos+1].val == "=" {
			name := p.next().val
			p.next()
			v, err := p.parseExpr()
			if err != nil {
				return nil, nil, err
			}
			if kwargs == nil {
				kwargs = map[string]expr{}
			}
			kwargs[name] = v
		} else {
			a, err := p.parseExpr()
			if err != nil {
				return nil, nil, err
			}
			args = append(args, a)
		}
		if p.isOp(",") {
			p.next()
			continue
		}
		if !p.isOp(")") {
			return nil, nil, fmt.Errorf("expected ',' or ')' at %d", p.peek().pos)
		}
	}
	p.next()
	return args, kwargs, nil
}

func (p *parser) parsePrimary() (expr, error) {
	t := p.next()
	switch t.kind {
	case tInt:
		if n, err := strconv.ParseInt(t.val, 10, 64); err == nil {
			return &litExpr{n}, nil
		}
		b, ok := new(big.Int).SetString(t.val, 10)
		if !ok {
			return nil, fmt.Errorf("bad integer %q", t.val)
		}
		return &litExpr{b}, nil
	case tFloat:
		f, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", t.val)
		}
		return &litExpr{f}, nil
	case tString:
		return &litExpr{t.val}, nil
	case tName:
		switch t.val {
		case "true", "True":
			return &litExpr{true}, nil
		case "false", "False":
			return &litExpr{false}, nil
		case "none", "None":
			return &litExpr{nil}, nil
		}
		return &nameExpr{t.val}, nil
	case tOp:
		switch t.val {
		case "(":
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			return e, p.expectOp(")")
		case "[":
			l := &listExpr{}
			for !p.isOp("]") {
				e, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				l.items = append(l.items, e)
				if p.isOp(",") {
					p.next()
				}
			}
			p.next()
			return l, nil
		case "{":
			d := &dictExpr{}
			for !p.isOp("}") {
				k, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				if err := p.expectOp(":"); err != nil {
					return nil, err
				}
				v, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				d.keys = append(d.keys, k)
				d.vals = append(d.vals, v)
				if p.isOp(",") {
					p.next()
				}
			}
			p.next()
			return d, nil
		}
	}
	return nil, fmt.Errorf("unexpected %s at %d", t, t.pos)
}
