//go:build tracker

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func distFS() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	return sub, true
}
