package chat

import (
	"embed"
	"io/fs"
)

//go:embed web/*
var webFiles embed.FS

func webUIFS() fs.FS {
	sub, _ := fs.Sub(webFiles, "web")
	return sub
}
