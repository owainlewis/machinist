package main

import (
	"os"

	"github.com/owainlewis/factory/internal/factorycli"
)

func main() {
	os.Exit(factorycli.Run(factorycli.Options{
		Arguments: os.Args[1:],
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}))
}
