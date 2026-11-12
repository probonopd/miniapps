/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package template

type node interface{}

type textNode struct{ s string }
type outputNode struct{ e expr }
type ifNode struct {
	conds  []expr
	bodies [][]node
	els    []node
}
type forNode struct {
	vars []string
	iter expr
	body []node
	els  []node
}
type setNode struct {
	name string
	e    expr
}

type expr interface{}

type litExpr struct{ v any }
type nameExpr struct{ name string }
type attrExpr struct {
	obj  expr
	name string
}
type indexExpr struct{ obj, idx expr }
type sliceExpr struct{ obj, start, stop expr }
type callExpr struct {
	fn     expr
	args   []expr
	kwargs map[string]expr
}
type filterExpr struct {
	e      expr
	name   string
	args   []expr
	kwargs map[string]expr
}
type testExpr struct {
	e      expr
	name   string
	args   []expr
	negate bool
}
type unaryExpr struct {
	op string
	x  expr
}
type binaryExpr struct {
	op   string
	l, r expr
}
type condExpr struct{ test, a, b expr }
type listExpr struct{ items []expr }
type dictExpr struct{ keys, vals []expr }
