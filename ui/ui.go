// Package ui serves the official pprof web UI for saved profiles, embedded
// in-process via the webui package (no subprocess, no Go toolchain needed at
// runtime).
package ui

import (
	"errors"
	"flag"
	"fmt"
	"net"

	"github.com/workmule/prof-kit/webui"
)

// Run launches the official pprof web UI for the given profile file.
// Optional args: -http=host:port to pin the address. Blocks until stopped.
func Run(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	addr := fs.String("http", "localhost:0", "listen address (`:0` = pick a free port)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: profkit ui [-http=host:port] <profile.pb.gz>")
	}
	path := fs.Arg(0)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if host == "" || host == "0.0.0.0" || host == "::" || host == "127.0.0.1" {
		host = "localhost"
	}
	url := fmt.Sprintf("http://%s:%s/", host, port)
	fmt.Printf("official pprof web UI on %s (Ctrl-C to stop)\n", url)
	webui.OpenBrowser(url)
	return webui.Serve(ln, path)
}
