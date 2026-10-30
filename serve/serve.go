// Package serve implements the profkit web service: form, whitelisted pprof
// proxy, and health endpoint. Thin shell over core (see AGENTS.md).
package serve

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
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
  target: <input name="target" value="{{.Target}}" required>
  type: <select name="type">
    {{range $t := .Types}}<option {{if eq $t $.Type}}selected{{end}}>{{$t}}</option>
    {{end}}</select>
  seconds (cpu only): <input name="seconds" value="{{.Seconds}}" size="4">
  <button>fetch</button> <span id="status" style="color:#888"></span>
</form>
<script>
document.forms[0].addEventListener('submit', function(){
  this.querySelector('button').disabled = true;
  var st = document.getElementById('status');
  if (this.type.value !== 'cpu') { st.textContent = '采集中...'; return; }
  var left = parseInt(this.seconds.value) || 0;
  st.textContent = '采集中，剩余 ' + left + 's...';
  var t = setInterval(function(){
    left--;
    if (left <= 0) { clearInterval(t); st.textContent = '生成报告中...'; return; }
    st.textContent = '采集中，剩余 ' + left + 's...';
  }, 1000);
});
</script>
<p>Direct proxy: <code>{{.Base}}proxy/{ip}/{port}/debug/pprof/heap</code></p>
</body></html>`

// formData is the render model: values come from cookies (last submission)
// with fixed fallback defaults.
var formKinds = []string{"cpu", "heap", "goroutine", "allocs"}

func formData(r *http.Request, base string) map[string]any {
	return map[string]any{
		"Base":    base,
		"Target":  cookieOr(r, "pk_target", "127.0.0.1:8091"),
		"Type":    cookieOr(r, "pk_type", "cpu"),
		"Seconds": cookieOr(r, "pk_seconds", "30"),
		"Types":   formKinds,
	}
}

func cookieOr(r *http.Request, name, def string) string {
	if c, err := r.Cookie(name); err == nil && c.Value != "" {
		return c.Value
	}
	return def
}

func setCookie(w http.ResponseWriter, name, val string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: "/", MaxAge: 2592000}) // 30d
}

// Run parses flags from args and blocks serving until error.
func Run(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("l", envOr("PPROF_LISTEN_ADDRESS", ""), `listen address (empty = auto-pick a free port, bound to localhost)`)
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	base := fs.String("b", envOr("PPROF_BASE_PATH", "/"), "base path when behind a reverse proxy")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec

	if *addr == "" {
		// 同 ui 包的做法：新版环境里写死端口易冲突，未显式指定就挑空闲端口。
		// 对外统一显示 localhost（Addr() 会给 127.0.0.1，语义相同但难读）
		l, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			return err
		}
		*addr = fmt.Sprintf("localhost:%d", l.Addr().(*net.TCPAddr).Port)
		l.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		template.Must(template.New("f").Parse(formHTML)).Execute(w, formData(r, *base))
	})
	mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) { serveFetch(w, r, *tmp) })
	mux.HandleFunc("GET /view/{rest...}", viewHandler)
	mux.HandleFunc("GET /proxy/{host}/{port}/debug/pprof/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		serveProxy(w, r, *tmp)
	})

	var h http.Handler = mux
	if *base != "/" {
		h = http.StripPrefix(strings.TrimSuffix(*base, "/"), mux)
	}
	fmt.Printf("profkit serve on %s (storage %s, base %s)\nweb UI: http://%s/\n", *addr, *tmp, *base, *addr)
	return http.ListenAndServe(*addr, h)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serveFetch(w http.ResponseWriter, r *http.Request, tmp string) {
	target, kind, secs := r.FormValue("target"), r.FormValue("type"), r.FormValue("seconds")
	setCookie(w, "pk_target", target)
	setCookie(w, "pk_type", kind)
	setCookie(w, "pk_seconds", secs)
	seconds, _ := strconv.Atoi(secs)
	path, err := core.Fetch(target, kind, seconds, tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id, err := mountView(path)
	if err != nil {
		fmt.Fprintf(w, "profile saved: %s\nbut web UI failed: %v\nanalyze manually: go tool pprof -http=:0 %s\n", path, err, path)
		return
	}
	http.Redirect(w, r, "view/"+id+"/", http.StatusSeeOther) // relative: works under -b base path too
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
