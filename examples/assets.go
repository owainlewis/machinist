package examples

import "embed"

// Files contains the default configuration installed by machinist init.
//
//go:embed config.toml worker.toml prompts/*.md
var Files embed.FS
