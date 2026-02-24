package web

import "embed"

//go:embed index.html bunghole.png config
var Content embed.FS
