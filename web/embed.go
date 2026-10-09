// Package web embute o frontend (SPA em JavaScript puro, sem dependências de CDN).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var files embed.FS

// Static devolve o sistema de arquivos com a raiz em static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
