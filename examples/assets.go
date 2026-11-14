package examples

import "embed"

// Files contains the configuration and starter factory installed by machinist init.
//
//go:embed config.toml runner.toml all:factory
var Files embed.FS
