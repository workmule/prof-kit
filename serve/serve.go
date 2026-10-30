// Package serve implements the profkit web service: form, whitelisted pprof
// proxy, and health endpoint. Thin shell over core (see AGENTS.md).
package serve

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/workmule/prof-kit/core"
	profkitmcp "github.com/workmule/prof-kit/mcp"
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
document.forms[0].addEventListener('submit', function(ev){
  ev.preventDefault();
  var form = this, b = form.querySelector('button');
  b.disabled = true;
  var st = document.getElementById('status');
  if (form.type.value === 'cpu') {
    var left = parseInt(form.seconds.value) || 0;
    var t = setInterval(function(){
      left--;
      if (left <= 0) { clearInterval(t); st.textContent = '📦 生成报告中...'; return; }
      st.textContent = '⏳ 采集中，剩余 ' + left + 's...';
    }, 1000);
  } else {
    st.textContent = '⏳ 采集中...';
  }
  fetch('fetch', {method:'POST', body: new FormData(form)})
    .then(function(r){ return r.json().then(function(d){ return {ok:r.ok, d:d}; }); })
    .then(function(res){
      clearInterval(t);
      if (res.ok && res.d.url) { location.href = res.d.url; return; }
      b.disabled = false;
      st.textContent = '❌ 采集失败';
      alert('采集失败：\n' + (res.d.error || ('HTTP ' + res.ok)));
    })
    .catch(function(e){
      clearInterval(t);
      b.disabled = false;
      st.textContent = '❌ 请求异常';
      alert('请求异常：' + e);
    });
});
</script>
<h2>已采集（超出保留数自动淘汰最旧，PPROF_MAX_PROFILES 可配，默认 30）</h2>
{{if .Views}}<ul>{{range .Views}}<li><a href="view/{{.}}/">📁 {{.}}</a></li>{{end}}</ul>
{{else}}<span class="empty">暂无 — 采集后这里会出现可回看的入口</span>{{end}}
</div>
</body></html>`

// formData is the render model: values come from cookies (last submission)
// with fixed fallback defaults.
var formKinds = []string{"cpu", "heap", "goroutine", "allocs", "block", "mutex"}

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
// version is the build-injected version string (see Makefile), passed to the
// MCP server mounted at /mcp.
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("l", envOr("PPROF_LISTEN_ADDRESS", ""), `listen address (empty = auto-pick a free port, bound to localhost)`)
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	base := fs.String("b", envOr("PPROF_BASE_PATH", "/"), "base path when behind a reverse proxy")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	daemon := fs.Bool("d", false, "run in background: detach from terminal, log to <storage>/profkit.log (manage via `profkit ctl`)")
	ctlSock := fs.String("ctl-sock", "", "unix socket path for ctl commands (default <storage>/profkit.sock); cli 侧用 profkit ctl -sock 对应")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec
	// 存储目录不存在则创建（首次部署/干净容器里 /tmp/pprof-web 不存在，
	// ctl socket 与 profile 落盘都依赖它）
	if err := os.MkdirAll(*tmp, 0o755); err != nil {
		return fmt.Errorf("create storage dir %s: %w", *tmp, err)
	}
	rebuildViews(*tmp) // 重启后从存储目录重建 view 注册表，并清理超量旧文件

	if *daemon {
		if err := checkAlreadyRunning(*tmp, *ctlSock); err != nil {
			return err
		}
		return detach(args, *tmp)
	}

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
	// {$} = 精确匹配根路径，避免与无方法限定的 /mcp（POST/GET/DELETE）产生
	// ServeMux 模式冲突
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
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
	// MCP over streamable HTTP, same tools as `profkit mcp` (stdio).
	// No method pattern: the transport uses POST/GET/DELETE on one path.
	mux.Handle("/mcp", profkitmcp.HTTPHandler(version, *tmp))

	// —— 监听器获取：restart 传递（fd 继承）或全新创建 ——
	inherited := os.Getenv(inheritEnv) == "3,4,5"
	var tcpLn net.Listener
	var ctlLn net.Listener
	if inherited {
		var err error
		if tcpLn, err = inheritListener(3, "tcp"); err != nil {
			return err
		}
		if ctlLn, err = inheritListener(4, "ctl"); err != nil {
			return err
		}
		if ul, ok := ctlLn.(*net.UnixListener); ok {
			ul.SetUnlinkOnClose(false) // 文件由父进程创建，关闭时不删除
		}
	}

	// 管控通道：unix socket（本机文件权限天然隔离，不经 TCP 暴露）。
	sock := *ctlSock
	if sock == "" {
		sock = filepath.Join(*tmp, defaultCtlSock)
	}

	// -b base path：反代把 /<base>/... 转进来时先剥前缀再进业务 mux
	var root http.Handler = mux
	if *base != "/" {
		root = http.StripPrefix(strings.TrimSuffix(*base, "/"), mux)
	}
	httpSrv := &http.Server{Handler: root}
	doStop := func() {
		os.Remove(sock)
		time.Sleep(100 * time.Millisecond) // 给内核冲刷应答的窗口，避免客户端读到 RST
		os.Exit(0)                         // 无状态服务：profile 均已落盘，直接退出
	}
	var sig chan os.Signal
	doRestart := func() {
		gracefulRestart(tcpLn, ctlLn, httpSrv, *addr, sig)
	}

	if inherited {
		// 子进程：fd 已绑定，通知父进程可以排水退出
		if f := os.NewFile(5, "ready"); f != nil {
			f.Write([]byte("ok"))
			f.Close()
		}
		go http.Serve(ctlLn, ctlMux(*addr, version, sock, doStop, doRestart))
		return httpSrv.Serve(tcpLn)
	}

	var err error
	if tcpLn, err = net.Listen("tcp", *addr); err != nil {
		return err
	}
	if ctlLn, err = listenCtl(sock, ctlMux(*addr, version, sock, doStop, doRestart)); err != nil {
		return err
	}

	// SIGTERM/SIGINT 走与 ctl stop 相同的清理路径，kill 后不留 pid/addr/socket 垃圾
	sig = make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		os.Remove(sock)
		os.Exit(0)
	}()

	fmt.Printf("profkit serve on %s (storage %s, base %s)\nweb UI: http://%s/\nMCP:    http://%s/mcp\n", *addr, *tmp, *base, *addr, *addr)
	return httpSrv.Serve(tcpLn)
}

const inheritEnv = "PROFKIT_INHERIT_FDS"

// gracefulRestart：把 TCP/ctl 监听 fd 传给重新拉起的自身进程，等子进程
// 就绪后旧进程排水退出——监听不断、在途请求（如进行中的 CPU 采集）由
// 旧进程处理完毕。fd 编号约定：3=TCP、4=ctl、5=ready 回执管道。
func gracefulRestart(tcpLn, ctlLn net.Listener, srv *http.Server, addr string, sigCh chan os.Signal) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restart:", err)
		return
	}
	argv := make([]string, 0, len(os.Args)+2)
	for i := 0; i < len(os.Args); i++ {
		if os.Args[i] == "-l" { // 跳过旧的 -l 及其值
			i++
			continue
		}
		if strings.HasPrefix(os.Args[i], "-l=") {
			continue
		}
		argv = append(argv, os.Args[i])
	}
	argv = append(argv, "-l", addr) // 钉住实际地址，自动选端口不漂移
	tcpf, err := tcpLn.(*net.TCPListener).File()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restart:", err)
		return
	}
	ctlf, err := ctlLn.(*net.UnixListener).File()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restart:", err)
		return
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restart:", err)
		return
	}
	cmd := &exec.Cmd{
		Path: exe, Args: argv,
		Env:        append(envWithout(os.Environ(), inheritEnv), inheritEnv+"=3,4,5"),
		ExtraFiles: []*os.File{tcpf, ctlf, readyW},
		Stdout:     os.Stdout, Stderr: os.Stderr, // daemon 模式下即日志文件
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "restart:", err)
		return
	}
	readyW.Close()
	errCh := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(readyR, make([]byte, 2))
		readyR.Close()
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err != nil { // 子进程未就绪：中止重启，旧进程继续服务，避免中断
			cmd.Process.Kill()
			fmt.Fprintln(os.Stderr, "restart: 子进程未就绪，已中止:", err)
			return
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		fmt.Fprintln(os.Stderr, "restart: 子进程就绪超时，已中止")
		return
	}
	// 移交完成：旧进程停止收 ctl（立即）与 TCP（排水后），退出
	if sigCh != nil {
		signal.Stop(sigCh) // 旧进程退出路径不再清理文件（那是子进程的了）
	}
	if ul, ok := ctlLn.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false) // socket 文件仍被子进程的继承 fd 绑定，不能 unlink
	}
	ctlLn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx) // 等在途请求完成（Serve 随即返回 ErrServerClosed）
}

func inheritListener(fd uintptr, name string) (net.Listener, error) {
	return net.FileListener(os.NewFile(fd, name))
}

func envWithout(envs []string, key string) []string {
	out := envs[:0:0]
	for _, e := range envs {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return out
}

// detach re-execs the serve command without -d in a new session, so the
// child survives the terminal. Logs go to <storage>/profkit.log.
func detach(args []string, tmp string) error {
	argv := make([]string, 0, len(args)+1)
	for _, a := range args { // 子进程必须以前台方式运行，去掉 -d
		if a == "-d" || a == "--d" || a == "-d=true" {
			continue
		}
		argv = append(argv, a)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(tmp, "profkit.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, append([]string{"serve"}, argv...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	fmt.Printf("profkit serve 已后台启动 pid=%d 日志=%s\n管控: profkit ctl status|stop|restart\n", pid, logPath)
	return nil
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
	path, err := core.Fetch(target, kind, seconds, tmp, task)
	if err != nil {
		jsonError(w, err.Error())
		return
	}
	id, err := mountView(path)
	if err != nil {
		jsonError(w, fmt.Sprintf("profile 已保存: %s，但分析页挂载失败: %v（可手动 go tool pprof -http=:0 %s）", path, err, path))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": "view/" + id + "/"}) // 相对路径，-b base path 下同样有效
}

// jsonError: 表单用 fetch API 提交，失败以 JSON 返回给前端弹框展示
func jsonError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
