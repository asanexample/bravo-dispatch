//go:build !tracker

package web

import "io/fs"

// No SPA embedded (built without -tags tracker). The tracker runs the BFF API only — used by
// `go build ./...`, tests, and local API-only runs. The release Docker build sets -tags tracker.
func distFS() (fs.FS, bool) { return nil, false }
