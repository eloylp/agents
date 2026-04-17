// Package ui embeds the static web UI assets built from the Next.js project.
// The dist/ directory must be populated by `next build && next export` before
// `go build`. A placeholder index.html is committed so that Go contributors
// can build the daemon without a Node.js toolchain installed.
package ui

import "embed"

// FS holds the embedded contents of the dist/ directory. Mount it at /ui/
// via http.FileServer(http.FS(ui.FS)) to serve the dashboard.
//
//go:embed dist
var FS embed.FS
