// Command profkit dispatches subcommands; all logic lives in the serve/mcp/core packages.
package main

import (
	"fmt"
	"os"

	"profkit/fetch"
	"profkit/mcp"
	"profkit/serve"
	"profkit/ui"
)

// version injected at build time via -ldflags "-X main.version=..." (see Makefile).
var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: profkit <serve|mcp|fetch>  (see README.md)")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve.Run(os.Args[2:])
	case "mcp":
		err = mcp.Run(os.Args[2:], version)
	case "fetch":
		err = fetch.Run(os.Args[2:])
	case "ui":
		err = ui.Run(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q (want serve|mcp|fetch|ui)", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "profkit:", err)
		os.Exit(1)
	}
}
