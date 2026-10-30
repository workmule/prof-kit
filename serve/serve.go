// Package serve implements the profkit web service: form, whitelisted pprof
// proxy, and health endpoint. Thin shell over core (see AGENTS.md).
package serve

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"profkit/core"
)

const formHTML = `<!doctype html>
<html><head><title>profkit serve</title></head><body>
<h1>profkit serve</h1>
<form method="POST" action="fetch">
  target: <input name="target" placeholder="127.0.0.1:6060" required>
  type: <select name="type">
    <option>cpu</option><option>heap</option><option>goroutine</option><option>allocs</option>
  </select>
  seconds (cpu only): <input name="seconds" value="30" size="4">
  <button>fetch</button>
</form>
<p>Direct proxy: <code>{{.Base}}proxy/{ip}/{port}/debug/pprof/heap</code></p>
</body></html>`

// Run parses flags from args and blocks serving until error.
func Run(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("l", envOr("PPROF_LISTEN_ADDRESS", ":8080"), "listen address")
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	base := fs.String("b", envOr("PPROF_BASE_PATH", "/"), "base path when behind a reverse proxy")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		template.Must(template.New("f").Parse(formHTML)).Execute(w, map[string]string{"Base": *base})
	})
	mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) { serveFetch(w, r, *tmp) })
	mux.HandleFunc("GET /proxy/{host}/{port}/debug/pprof/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		serveProxy(w, r, *tmp)
	})

	var h http.Handler = mux
	if *base != "/" {
		h = http.StripPrefix(strings.TrimSuffix(*base, "/"), mux)
	}
	fmt.Printf("profkit serve on %s (storage %s, base %s)\n", *addr, *tmp, *base)
	return http.ListenAndServe(*addr, h)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serveFetch(w http.ResponseWriter, r *http.Request, tmp string) {
	seconds, _ := strconv.Atoi(r.FormValue("seconds"))
	path, err := core.Fetch(r.FormValue("target"), r.FormValue("type"), seconds, tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rep, err := core.Top(path, 20)
	if err != nil {
		rep = "(top failed: " + err.Error() + ")\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "saved: %s\nanalyze: go tool pprof -http=:0 %s\n\n%s\n", path, path, rep)
}

func serveProxy(w http.ResponseWriter, r *http.Request, tmp string) {
	host, port := r.PathValue("host"), r.PathValue("port")
	rest := r.PathValue("rest")
	if !core.PProfTypes[rest] {
		http.Error(w, "pprof path not in whitelist: "+rest, http.StatusForbidden)
		return
	}
	if _, err := strconv.Atoi(port); err != nil || port == "" {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	query := r.URL.RawQuery
	timeout := 30 * time.Second
	if rest == "profile" {
		query = capSeconds(query)
		if s, err := strconv.Atoi(queryValue(query, "seconds")); err == nil {
			timeout = time.Duration(s+5) * time.Second
		}
	}
	u, err := core.ProfileURL(host+":"+port, rest, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var src io.Reader = resp.Body
	if rest == "profile" || rest == "heap" || rest == "goroutine" || rest == "allocs" {
		// archive a copy so profiles survive for later review (pprof-web semantics)
		if f := archive(tmp, rest, host+":"+port); f != nil {
			defer f.Close()
			src = io.TeeReader(resp.Body, f)
		}
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	io.Copy(w, src)
}

func archive(tmp, kind, target string) *os.File {
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil
	}
	name := fmt.Sprintf("%s_%s_%s.pb.gz", kind, strings.ReplaceAll(target, ":", "_"), time.Now().Format("20060102_150405"))
	f, err := os.Create(filepath.Join(tmp, name))
	if err != nil {
		return nil
	}
	return f
}

func capSeconds(q string) string {
	qv, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	if s, err := strconv.Atoi(qv.Get("seconds")); err == nil && s > core.MaxCPUSeconds {
		qv.Set("seconds", strconv.Itoa(core.MaxCPUSeconds))
	}
	return qv.Encode()
}

func queryValue(q, key string) string {
	qv, err := url.ParseQuery(q)
	if err != nil {
		return ""
	}
	return qv.Get(key)
}
