package examples

import "embed"

// Files contains the default configuration installed by machinist init.
//
//go:embed config.toml
var Files embed.FS
