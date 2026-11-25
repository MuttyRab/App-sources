// Package web holds the embedded user interface.
package web

import "embed"

// FS contains index.html and the static assets.
//
//go:embed index.html css js vendor img
var FS embed.FS
