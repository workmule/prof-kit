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
<html><head><meta charset="utf-8"><title>profkit serve</title>
<style>
*{box-sizing:border-box}
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;margin:0;background:#f3f5f7;color:#1d2129;display:flex;justify-content:center;padding:40px 16px}
.card{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);padding:28px 32px;width:100%;max-width:640px}
h1{font-size:20px;margin:0 0 4px}
.sub{color:#86909c;font-size:13px;margin:0 0 24px}
.field{margin-bottom:16px}
label{display:block;font-size:13px;color:#4e5969;margin-bottom:6px}
input,select{width:100%;padding:9px 12px;border:1px solid #e5e6eb;border-radius:8px;font-size:14px;background:#fff;outline:none;transition:border .2s}
input:focus,select:focus{border-color:#165dff}
.row{display:flex;gap:12px}
.row .field{flex:1}
button{width:100%;padding:10px 0;border:none;border-radius:8px;background:#165dff;color:#fff;font-size:15px;cursor:pointer;transition:background .2s;margin-top:4px}
button:hover{background:#4080ff}
button:disabled{background:#94bfff;cursor:not-allowed}
#status{display:block;margin-top:12px;font-size:13px;color:#86909c;min-height:18px}
h2{font-size:14px;color:#4e5969;margin:28px 0 10px}
ul{list-style:none;margin:0;padding:0}
li{margin:0}
li a{display:block;padding:8px 12px;border-radius:8px;color:#1d2129;text-decoration:none;font-family:monospace;font-size:13px;background:#f7f8fa;margin-bottom:6px;transition:background .15s}
li a:hover{background:#e8f0ff}
.empty{color:#c9cdd4;font-size:13px}
code{background:#f2f3f5;padding:2px 6px;border-radius:4px;font-size:12px;word-break:break-all}
</style></head><body>
<div class="card">
<h1>⚡ profkit serve</h1>
<p class="sub">Go 服务性能采集与分析 — 采集完成自动打开官方 pprof 分析页</p>
<form method="POST" action="fetch">
  <div class="field">
    <label>目标服务 (ip:port)</label>
    <input name="target" value="{{.Target}}" required>
    <p style="margin:6px 0 0;font-size:12px;color:#86909c">也可直接代理访问：<code>{{.Base}}proxy/{ip}/{port}/debug/pprof/heap</code>（可喂给 go tool pprof）</p>
  </div>
  <div class="field">
    <label>任务名（可选，用于区分采集文件）</label>
    <input name="task" value="{{.Task}}" placeholder="如 gateway-压测-1">
  </div>
  <div class="row">
    <div class="field">
      <label>类型</label>
      <select name="type">
        {{range $t := .Types}}<option {{if eq $t $.Type}}selected{{end}}>{{$t}}</option>
        {{end}}</select>
    </div>
    <div class="field">
      <label>时长（仅 cpu）</label>
      <input name="seconds" value="{{.Seconds}}">
    </div>
  </div>
  <button>开始采集</button>
  <span id="status"></span>
</form>
<script>
document.forms[0].addEventListener('submit', function(){
  this.querySelector('button').disabled = true;
  var st = document.getElementById('status');
  if (this.type.value !== 'cpu') { st.textContent = '⏳ 采集中...'; return; }
  var left = parseInt(this.seconds.value) || 0;
  st.textContent = '⏳ 采集中，剩余 ' + left + 's...';
  var t = setInterval(function(){
    left--;
    if (left <= 0) { clearInterval(t); st.textContent = '📦 生成报告中...'; return; }
    st.textContent = '⏳ 采集中，剩余 ' + left + 's...';
  }, 1000);
});
</script>
<h2>已采集（最多保留 8 份，新采集自动淘汰最旧）</h2>
{{if .Views}}<ul>{{range .Views}}<li><a href="view/{{.}}/">📁 {{.}}</a></li>{{end}}</ul>
{{else}}<span class="empty">暂无 — 采集后这里会出现可回看的入口</span>{{end}}
</div>
</body></html>`

// formData is the render model: values come from cookies (last submission)
// with fixed fallback defaults.
var formKinds = []string{"cpu", "heap", "goroutine", "allocs"}

func formData(r *http.Request, base string) map[string]any {
	viewMu.Lock()
	views := make([]string, 0, len(viewLRU))
	for i := len(viewLRU) - 1; i >= 0; i-- { // newest first
		views = append(views, viewLRU[i])
	}
	viewMu.Unlock()
	return map[string]any{
		"Base":    base,
		"Target":  cookieOr(r, "pk_target", "127.0.0.1:8091"),
		"Type":    cookieOr(r, "pk_type", "cpu"),
		"Seconds": cookieOr(r, "pk_seconds", "30"),
		"Task":    cookieOr(r, "pk_task", "default"),
		"Types":   formKinds,
		"Views":   views,
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
	task := r.FormValue("task")
	setCookie(w, "pk_target", target)
	setCookie(w, "pk_type", kind)
	setCookie(w, "pk_seconds", secs)
	setCookie(w, "pk_task", task)
	seconds, _ := strconv.Atoi(secs)
	path, err := core.Fetch(target, kind, seconds, tmp, r.FormValue("task"))
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
