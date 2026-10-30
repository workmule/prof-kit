// Package ui serves the official pprof web UI for saved profiles by shelling
// out to `go tool pprof -http` — same UI pprof-web exposes (it also just wraps
// google/pprof), without re-implementing the driver glue.
package ui

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// Run launches the official pprof web UI for the given profile file(s).
// Extra args pass through (e.g. -http=:8123 to pin a port). Blocks until stopped.
func Run(args []string) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("'go' not in PATH: the pprof web UI comes from the Go toolchain; %w", err)
	}
	hasHTTP := false
	for _, a := range args {
		if a == "-http" || strings.HasPrefix(a, "-http=") {
			hasHTTP = true
		}
	}
	if !hasHTTP {
		// ponytail: newer pprof no longer resolves -http=:0, so grab a free
		// port ourselves (tiny TOCTOU race, fine for a local viewer).
		l, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			return err
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		args = append([]string{fmt.Sprintf("-http=localhost:%d", port)}, args...)
	}
	cmd := exec.Command(goBin, append([]string{"tool", "pprof"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
