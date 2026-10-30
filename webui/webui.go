// Package webui embeds the official google/pprof web UI via driver.PProf +
// the HTTPServer plugin: the pprof library parses the profile and hands back
// its full handler set (flamegraph/top/graph/source/peek), which hosts either
// mount into an existing mux (Mount) or serve blocking on a listener (Serve).
// Single process — no subprocess, no dependency on the Go toolchain at runtime.
package webui

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/google/pprof/driver"
)

// Mount parses the profile in-process and returns the official handlers.
// PProf returns as soon as the callback stores them; the handlers stay alive
// (they close over the parsed profile) as long as the caller keeps the map.
func Mount(path string) (map[string]http.Handler, error) {
	var handlers map[string]http.Handler
	o := &driver.Options{
		Flagset: newPProfFlags([]string{"-http=localhost:0", path}),
		UI:      headlessUI{},
		HTTPServer: func(args *driver.HTTPServerArgs) error {
			handlers = args.Handlers
			return nil
		},
	}
	return handlers, driver.PProf(o)
}

// Serve blocks serving the official UI for path on ln (typically a listener
// on a free localhost port). Ctrl-C/interrupting the process stops it.
func Serve(ln net.Listener, path string) error {
	var handlers map[string]http.Handler
	o := &driver.Options{
		Flagset: newPProfFlags([]string{"-http=localhost:0", path}),
		UI:      headlessUI{},
		HTTPServer: func(args *driver.HTTPServerArgs) error {
			handlers = args.Handlers
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				h, ok := handlers[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				h.ServeHTTP(w, r)
			})
			return http.Serve(ln, mux)
		},
	}
	return driver.PProf(o)
}

// OpenBrowser opens url in the system browser (best effort).
func OpenBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// pprofFlags adapts std flag to driver.FlagSet, parsing a fixed arg list.
type pprofFlags struct {
	fs   *flag.FlagSet
	args []string
}

func newPProfFlags(args []string) *pprofFlags {
	fs := flag.NewFlagSet("pprof", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return &pprofFlags{fs: fs, args: args}
}

func (f *pprofFlags) Bool(name string, def bool, usage string) *bool {
	return f.fs.Bool(name, def, usage)
}

func (f *pprofFlags) Int(name string, def int, usage string) *int {
	return f.fs.Int(name, def, usage)
}

func (f *pprofFlags) Float64(name string, def float64, usage string) *float64 {
	return f.fs.Float64(name, def, usage)
}

func (f *pprofFlags) String(name, def, usage string) *string {
	return f.fs.String(name, def, usage)
}

// stringList implements the multi-value flag driver.StringList needs.
type stringList []*string

func (s *stringList) String() string {
	if len(*s) == 0 {
		return ""
	}
	return *(*s)[0]
}

func (s *stringList) Set(v string) error {
	*s = append(*s, &v)
	return nil
}

func (f *pprofFlags) StringList(name, def, usage string) *[]*string {
	sl := &stringList{}
	f.fs.Var(sl, name, usage)
	return (*[]*string)(sl)
}

func (f *pprofFlags) ExtraUsage() string   { return "" }
func (f *pprofFlags) AddExtraUsage(string) {}

func (f *pprofFlags) Parse(usage func()) []string {
	if err := f.fs.Parse(f.args); err != nil {
		usage()
		return nil
	}
	return f.fs.Args()
}

// headlessUI satisfies driver.UI without a terminal.
type headlessUI struct{}

func (headlessUI) ReadLine(string) (string, error)     { return "", io.EOF }
func (headlessUI) Print(...interface{})                {}
func (headlessUI) PrintErr(i ...interface{})           { fmt.Fprintln(os.Stderr, i...) }
func (headlessUI) IsTerminal() bool                    { return false }
func (headlessUI) WantBrowser() bool                   { return false }
func (headlessUI) SetAutoComplete(func(string) string) {}
