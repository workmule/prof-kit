// Package fetch implements the profkit fetch subcommand: pull a profile and
// write a self-contained HTML report (top table + flame graph) viewable locally.
package fetch

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/workmule/prof-kit/core"
	"github.com/workmule/prof-kit/ui"
)

// Run parses args ("profkit fetch <ip:port> [kind] -d N") and blocks during collection.
func Run(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	kind := fs.String("kind", "heap", "profile kind: cpu|heap|goroutine|allocs")
	secs := fs.Int("d", 30, "cpu profile duration in seconds")
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	out := fs.String("o", "", `output HTML path (default "reports/profkit-report-<kind>-<ts>.html")`)
	open := fs.Bool("open", true, "open the HTML report in the browser")
	srvUI := fs.Bool("ui", false, "after fetching, serve the official pprof web UI for the profile (blocks until Ctrl-C)")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: profkit fetch [-o report.html] [-d 30] <ip:port> [cpu|heap|goroutine|allocs]  (flags must come before the target)")
	}
	target := fs.Arg(0)
	if fs.NArg() > 1 {
		*kind = fs.Arg(1) // positional kind wins over flag (matches docs syntax)
	}

	path, err := core.Fetch(target, *kind, *secs, *tmp, "")
	if err != nil {
		return err
	}
	data, err := core.FlameData(path)
	if err != nil {
		return fmt.Errorf("profile saved to %s but parse failed: %w", path, err)
	}
	top, err := core.Top(path, 30, "")
	if err != nil {
		top = "(top failed: " + err.Error() + ")\n"
	}

	if *out == "" {
		*out = filepath.Join("reports", fmt.Sprintf("profkit-report-%s-%s.html", *kind, time.Now().Format("20060102_150405")))
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*out, []byte(Report(*kind, target, top, data)), 0o644); err != nil {
		return err
	}
	fmt.Printf("profile: %s\nreport:  %s\n", path, *out)
	if *srvUI {
		fmt.Println("official pprof web UI (Ctrl-C to stop):")
		return ui.Run([]string{path})
	}
	if *open {
		openBrowser(*out) // best effort; the path is printed regardless
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openBrowser(path string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", path).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		exec.Command("xdg-open", path).Start()
	}
}
