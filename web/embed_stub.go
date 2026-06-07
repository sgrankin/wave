//go:build !embed

// Package web carries the browser client for single-binary builds of waved. This
// is the DEFAULT (non-embed) half: DistFS is nil, so waved serves the client from
// the -webroot directory on disk. Build with `-tags embed` (after `npm run build`
// populates web/dist) to embed the built client into the binary instead — see
// embed.go and the Makefile `release` target.
package web

import "io/fs"

// DistFS is the embedded built web client, or nil in a non-embed build (this file),
// in which case waved falls back to serving -webroot from disk.
var DistFS fs.FS
