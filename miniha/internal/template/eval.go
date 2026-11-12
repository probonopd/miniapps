/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

type evalCtx struct {
	scopes []map[string]any
	env    *Env
	out    strings.Builder
}

func (c *evalCtx) lookup(name string) (any, bool) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if v, ok := c.scopes[i][name]; ok {
			return v, true
		}
	}
	if c.env != nil {
		if v, ok := c.env.Globals[name]; ok {
			if f, isFunc := v.(Func); isFunc {
				return method(f), true
			}
			return v, true
		}
	}
	if f, ok := functions[name]; ok {
		return f, true
	}
	return nil, false
}

func (c *evalCtx) set(name string, v any) {
	c.scopes[len(c.scopes)-1][name] = v
}

func (c *evalCtx) push() { c.scopes = append(c.scopes, map[string]any{}) }
func (c *evalCtx) pop()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *evalCtx) execNodes(nodes []node) error {
	for _, n := range nodes {
		if err := c.exec(n); err != nil {
			return err
		}
	}
	return nil
}

func (c *evalCtx) exec(n node) error {
	switch x := n.(type) {
	case textNode:
		c.out.WriteString(x.s)
	case outputNode:
		v, err := c.eval(x.e)
		if err != nil {
			return err
		}
		c.out.WriteString(pystr(v))
	case *ifNode:
		for i, cond := range x.conds {
			v, err := c.eval(cond)
			if err != nil {
				return err
			}
			if truthy(v) {
				return c.execNodes(x.bodies[i])
			}
		}
		return c.execNodes(x.els)
	case *forNode:
		it, err := c.eval(x.iter)
		if err != nil {
			return err
		}
		items, err := iterate(it)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return c.execNodes(x.els)
		}
		c.push()
		defer c.pop()
		for i, item := range items {
			if len(x.vars) == 1 {
				c.set(x.vars[0], item)
			} else {
				parts, ok := item.([]any)
				if !ok || len(parts) != len(x.vars) {
					return fmt.Errorf("cannot unpack %s into %d variables", typeName(item), len(x.vars))
				}
				for j, name := range x.vars {
					c.set(name, parts[j])
				}
			}
			loop := NewDict()
			loop.Set("index", int64(i+1))
			loop.Set("index0", int64(i))
			loop.Set("first", i == 0)
			loop.Set("last", i == len(items)-1)
			loop.Set("length", int64(len(items)))
			c.set("loop", loop)
			if err := c.execNodes(x.body); err != nil {
				return err
			}
		}
	case *setNode:
		v, err := c.eval(x.e)
		if err != nil {
			return err
		}
		c.set(x.name, v)
	default:
		return fmt.Errorf("unknown node %T", n)
	}
	return nil
}

func iterate(v any) ([]any, error) {
	switch x := v.(type) {
	case Iterable:
		return x.TemplateItems(), nil
	case []any:
		return x, nil
	case *Dict:
		out := make([]any, 0, x.Len())
		for _, k := range x.keys {
			out = append(out, k)
		}
		return out, nil
	case string:
		out := make([]any, 0, len(x))
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	}
	return nil, fmt.Errorf("'%s' object is not iterable", typeName(v))
}

func (c *evalCtx) evalArgs(args []expr, kwargs map[string]expr) ([]any, map[string]any, error) {
	vals := make([]any, len(args))
	for i, a := range args {
		v, err := c.eval(a)
		if err != nil {
			return nil, nil, err
		}
		vals[i] = v
	}
	var kw map[string]any
	if len(kwargs) > 0 {
		kw = make(map[string]any, len(kwargs))
		for k, a := range kwargs {
			v, err := c.eval(a)
			if err != nil {
				return nil, nil, err
			}
			kw[k] = v
		}
	}
	return vals, kw, nil
}

func (c *evalCtx) eval(e expr) (any, error) {
	switch x := e.(type) {
	case *litExpr:
		return x.v, nil
	case *nameExpr:
		if v, ok := c.lookup(x.name); ok {
			return v, nil
		}
		return Undefined{x.name}, nil
	case *attrExpr:
		obj, err := c.eval(x.obj)
		if err != nil {
			return nil, err
		}
		return getAttr(obj, x.name)
	case *indexExpr:
		obj, err := c.eval(x.obj)
		if err != nil {
			return nil, err
		}
		idx, err := c.eval(x.idx)
		if err != nil {
			return nil, err
		}
		return getItem(obj, idx)
	case *sliceExpr:
		obj, err := c.eval(x.obj)
		if err != nil {
			return nil, err
		}
		bound := func(e expr, def int64) (int64, error) {
			if e == nil {
				return def, nil
			}
			v, err := c.eval(e)
			if err != nil {
				return 0, err
			}
			i, ok := v.(int64)
			if !ok {
				return 0, fmt.Errorf("slice indices must be integers")
			}
			return i, nil
		}
		return getSlice(obj, bound, x.start, x.stop)
	case *callExpr:
		fn, err := c.eval(x.fn)
		if err != nil {
			return nil, err
		}
		args, kw, err := c.evalArgs(x.args, x.kwargs)
		if err != nil {
			return nil, err
		}
		switch f := fn.(type) {
		case method:
			return f(args, kw)
		case Func:
			return f(args, kw)
		case function:
			return f(c, args, kw)
		case Callable:
			return f.TemplateCall(args, kw)
		case Undefined:
			return nil, fmt.Errorf("%s is undefined and cannot be called", f.hint)
		}
		return nil, fmt.Errorf("'%s' object is not callable", typeName(fn))
	case *filterExpr:
		v, err := c.eval(x.e)
		if err != nil {
			return nil, err
		}
		args, kw, err := c.evalArgs(x.args, x.kwargs)
		if err != nil {
			return nil, err
		}
		r, err := filters[x.name](c, v, args, kw)
		if err != nil {
			return nil, fmt.Errorf("filter %s: %w", x.name, err)
		}
		return r, nil
	case *testExpr:
		v, err := c.eval(x.e)
		if err != nil {
			return nil, err
		}
		args, _, err := c.evalArgs(x.args, nil)
		if err != nil {
			return nil, err
		}
		r, err := tests[x.name](v, args)
		if err != nil {
			return nil, err
		}
		return r != x.negate, nil
	case *unaryExpr:
		v, err := c.eval(x.x)
		if err != nil {
			return nil, err
		}
		switch x.op {
		case "not":
			return !truthy(v), nil
		case "+":
			if !isNumber(v) {
				return nil, fmt.Errorf("bad operand type for unary +: '%s'", typeName(v))
			}
			return v, nil
		case "-":
			return arith("-", int64(0), v)
		}
	case *binaryExpr:
		l, err := c.eval(x.l)
		if err != nil {
			return nil, err
		}
		switch x.op {
		case "and":
			if !truthy(l) {
				return l, nil
			}
			return c.eval(x.r)
		case "or":
			if truthy(l) {
				return l, nil
			}
			return c.eval(x.r)
		}
		r, err := c.eval(x.r)
		if err != nil {
			return nil, err
		}
		return binop(x.op, l, r)
	case *condExpr:
		t, err := c.eval(x.test)
		if err != nil {
			return nil, err
		}
		if truthy(t) {
			return c.eval(x.a)
		}
		if x.b == nil {
			return Undefined{"conditional expression"}, nil
		}
		return c.eval(x.b)
	case *listExpr:
		out := make([]any, len(x.items))
		for i, it := range x.items {
			v, err := c.eval(it)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case *dictExpr:
		d := NewDict()
		for i := range x.keys {
			k, err := c.eval(x.keys[i])
			if err != nil {
				return nil, err
			}
			v, err := c.eval(x.vals[i])
			if err != nil {
				return nil, err
			}
			d.Set(pystr(k), v)
		}
		return d, nil
	}
	return nil, fmt.Errorf("unknown expression %T", e)
}

func getAttr(obj any, name string) (any, error) {
	switch o := obj.(type) {
	case Object:
		if v, ok := o.TemplateAttr(name); ok {
			return v, nil
		}
		return Undefined{name}, nil
	case *Dict:
		if v, ok := o.Get(name); ok {
			return v, nil
		}
		if m := dictMethod(o, name); m != nil {
			return m, nil
		}
		return Undefined{name}, nil
	case string:
		if m := stringMethod(o, name); m != nil {
			return m, nil
		}
		return Undefined{name}, nil
	case Undefined:
		return nil, fmt.Errorf("%s has no attribute '%s'", o.Error(), name)
	case nil:
		return nil, fmt.Errorf("'None' has no attribute '%s'", name)
	case []any:
		var i int64
		if _, err := fmt.Sscanf(name, "%d", &i); err == nil {
			return getItem(o, i)
		}
	}
	return Undefined{name}, nil
}

func getItem(obj, idx any) (any, error) {
	switch o := obj.(type) {
	case Object:
		if v, ok := o.TemplateAttr(pystr(idx)); ok {
			return v, nil
		}
		return Undefined{pystr(idx)}, nil
	case *Dict:
		if v, ok := o.Get(pystr(idx)); ok {
			return v, nil
		}
		return Undefined{pystr(idx)}, nil
	case []any:
		i, ok := idx.(int64)
		if !ok {
			return nil, fmt.Errorf("list indices must be integers, not %s", typeName(idx))
		}
		if i < 0 {
			i += int64(len(o))
		}
		if i < 0 || i >= int64(len(o)) {
			return nil, fmt.Errorf("list index out of range")
		}
		return o[i], nil
	case string:
		i, ok := idx.(int64)
		if !ok {
			return nil, fmt.Errorf("string indices must be integers")
		}
		r := []rune(o)
		if i < 0 {
			i += int64(len(r))
		}
		if i < 0 || i >= int64(len(r)) {
			return nil, fmt.Errorf("string index out of range")
		}
		return string(r[i]), nil
	case Undefined:
		return nil, fmt.Errorf("%s is not subscriptable", o.Error())
	}
	return nil, fmt.Errorf("'%s' object is not subscriptable", typeName(obj))
}

// getSlice implements Python's s[a:b] with negative and out-of-range
// bounds clamped the way Python does.
func getSlice(obj any, bound func(expr, int64) (int64, error), startE, stopE expr) (any, error) {
	var n int64
	switch o := obj.(type) {
	case string:
		n = int64(len([]rune(o)))
	case []any:
		n = int64(len(o))
	case Undefined:
		return nil, fmt.Errorf("%s is not subscriptable", o.Error())
	default:
		return nil, fmt.Errorf("'%s' object is not subscriptable", typeName(obj))
	}
	start, err := bound(startE, 0)
	if err != nil {
		return nil, err
	}
	stop, err := bound(stopE, n)
	if err != nil {
		return nil, err
	}
	clamp := func(i int64) int64 {
		if i < 0 {
			i += n
		}
		if i < 0 {
			return 0
		}
		if i > n {
			return n
		}
		return i
	}
	start, stop = clamp(start), clamp(stop)
	if stop < start {
		stop = start
	}
	switch o := obj.(type) {
	case string:
		return string([]rune(o)[start:stop]), nil
	case []any:
		return append([]any{}, o[start:stop]...), nil
	}
	return nil, nil
}

func binop(op string, l, r any) (any, error) {
	switch op {
	case "==":
		return pyEqual(l, r), nil
	case "!=":
		return !pyEqual(l, r), nil
	case "<", ">", "<=", ">=":
		c, err := pyCompare(l, r)
		if err != nil {
			return nil, err
		}
		switch op {
		case "<":
			return c < 0, nil
		case ">":
			return c > 0, nil
		case "<=":
			return c <= 0, nil
		}
		return c >= 0, nil
	case "in", "not in":
		var found bool
		switch cont := r.(type) {
		case []any:
			for _, e := range cont {
				if pyEqual(e, l) {
					found = true
					break
				}
			}
		case *Dict:
			_, found = cont.Get(pystr(l))
		case string:
			s, ok := l.(string)
			if !ok {
				return nil, fmt.Errorf("'in <string>' requires string as left operand, not %s", typeName(l))
			}
			found = strings.Contains(cont, s)
		default:
			return nil, fmt.Errorf("argument of type '%s' is not iterable", typeName(r))
		}
		return found == (op == "in"), nil
	case "~":
		return pystr(l) + pystr(r), nil
	}
	return arith(op, l, r)
}

func pyEqual(l, r any) bool {
	if c, ok := l.(Comparable); ok {
		n, ok := c.CompareTo(r)
		return ok && n == 0
	}
	if isNumber(l) && isNumber(r) {
		if isInt(l) && isInt(r) {
			return toBig(l).Cmp(toBig(r)) == 0
		}
		lf, _ := toFloat(l)
		rf, _ := toFloat(r)
		return lf == rf
	}
	switch a := l.(type) {
	case nil:
		return r == nil
	case string:
		b, ok := r.(string)
		return ok && a == b
	case []any:
		b, ok := r.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !pyEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case *Dict:
		b, ok := r.(*Dict)
		if !ok || a.Len() != b.Len() {
			return false
		}
		for _, k := range a.keys {
			bv, ok := b.Get(k)
			if !ok || !pyEqual(a.m[k], bv) {
				return false
			}
		}
		return true
	}
	return false
}

func pyCompare(l, r any) (int, error) {
	if c, ok := l.(Comparable); ok {
		if n, ok := c.CompareTo(r); ok {
			return n, nil
		}
	}
	if c, ok := r.(Comparable); ok {
		if n, ok := c.CompareTo(l); ok {
			return -n, nil
		}
	}
	if isNumber(l) && isNumber(r) {
		if isInt(l) && isInt(r) {
			return toBig(l).Cmp(toBig(r)), nil
		}
		lf, _ := toFloat(l)
		rf, _ := toFloat(r)
		switch {
		case lf < rf:
			return -1, nil
		case lf > rf:
			return 1, nil
		}
		return 0, nil
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		return strings.Compare(ls, rs), nil
	}
	return 0, fmt.Errorf("'<' not supported between instances of '%s' and '%s'", typeName(l), typeName(r))
}

func arith(op string, l, r any) (any, error) {
	if a, ok := l.(Arith); ok {
		if v, handled, err := a.BinaryOp(op, r, false); handled || err != nil {
			return v, err
		}
	}
	if a, ok := r.(Arith); ok {
		if v, handled, err := a.BinaryOp(op, l, true); handled || err != nil {
			return v, err
		}
	}
	// Sequence operators first: "ab" * 2, "a" + "b", [1] + [2].
	if op == "+" {
		if ls, ok := l.(string); ok {
			if rs, ok := r.(string); ok {
				return ls + rs, nil
			}
		}
		if ll, ok := l.([]any); ok {
			if rl, ok := r.([]any); ok {
				return append(append([]any{}, ll...), rl...), nil
			}
		}
	}
	if op == "*" {
		if s, ok := l.(string); ok && isInt(r) {
			return strings.Repeat(s, int(toBig(r).Int64())), nil
		}
		if s, ok := r.(string); ok && isInt(l) {
			return strings.Repeat(s, int(toBig(l).Int64())), nil
		}
	}
	if !isNumber(l) || !isNumber(r) {
		return nil, fmt.Errorf("unsupported operand type(s) for %s: '%s' and '%s'", op, typeName(l), typeName(r))
	}
	if isInt(l) && isInt(r) && op != "/" {
		a, b := toBig(l), toBig(r)
		switch op {
		case "+":
			return normInt(new(big.Int).Add(a, b)), nil
		case "-":
			return normInt(new(big.Int).Sub(a, b)), nil
		case "*":
			return normInt(new(big.Int).Mul(a, b)), nil
		case "//":
			if b.Sign() == 0 {
				return nil, fmt.Errorf("integer division or modulo by zero")
			}
			q := new(big.Int)
			m := new(big.Int)
			q.DivMod(a, b, m)
			// big.DivMod is Euclidean; Python floors toward negative infinity.
			if m.Sign() != 0 && b.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			}
			return normInt(q), nil
		case "%":
			if b.Sign() == 0 {
				return nil, fmt.Errorf("integer division or modulo by zero")
			}
			m := new(big.Int).Mod(a, b)
			if m.Sign() != 0 && b.Sign() < 0 {
				m.Add(m, b)
			}
			return normInt(m), nil
		case "**":
			if b.Sign() < 0 {
				af, _ := toFloat(l)
				bf, _ := toFloat(r)
				return math.Pow(af, bf), nil
			}
			return normInt(new(big.Int).Exp(a, b, nil)), nil
		}
	}
	a, _ := toFloat(l)
	b, _ := toFloat(r)
	switch op {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		if b == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return a / b, nil
	case "//":
		if b == 0 {
			return nil, fmt.Errorf("float floor division by zero")
		}
		return math.Floor(a / b), nil
	case "%":
		if b == 0 {
			return nil, fmt.Errorf("float modulo by zero")
		}
		m := math.Mod(a, b)
		if m != 0 && (m < 0) != (b < 0) {
			m += b
		}
		return m, nil
	case "**":
		return math.Pow(a, b), nil
	}
	return nil, fmt.Errorf("unknown operator %s", op)
}
