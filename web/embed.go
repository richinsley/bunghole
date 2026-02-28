package web

import "embed"

//go:embed index.html bunghole.png bridge.js config
var Content embed.FS
