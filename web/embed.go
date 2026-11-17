// Package web embeds the templates and static assets into the binary.
// It is a Go package because //go:embed cannot reach a parent directory.
package web

import "embed"

// FS holds templates/ and static/. The editor bundle lives in static/js/.
//
//go:embed templates static
var FS embed.FS
