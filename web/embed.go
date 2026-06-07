//go:build embed

package web

import (
	"embed"
	"io/fs"
)

// distEmbed holds the built browser client. `all:dist` includes files whose names
// begin with '.' or '_' too, so nothing in the build output is silently dropped.
// This requires web/dist to exist and be non-empty at build time — run
// `npm run build` first (the Makefile `release` target does this before
// `go build -tags embed`).
//
//go:embed all:dist
var distEmbed embed.FS

// DistFS is the embedded built web client with dist/ as its root, served at the
// WebSocket server's "/" so waved is a single self-contained binary (no -webroot).
var DistFS fs.FS = mustSub(distEmbed, "dist")

func mustSub(e embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(e, dir)
	if err != nil {
		panic("web: embed dist: " + err.Error())
	}
	return sub
}
