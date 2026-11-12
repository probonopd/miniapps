/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package web embeds the browser UI into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
