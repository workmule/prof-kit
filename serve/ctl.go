// ctl: background instance management over a unix socket. Server side
// (ctlMux/listenCtl) runs inside serve; client side (Ctl) is `profkit ctl`.
// Liveness 判据只有 socket 本身：dial 通 = 在运行（无 pid/addr 落盘文件）。
package serve

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const defaultCtlSock = "profkit.sock"

// ctlStatus is the GET /ctl/status response body.
type ctlStatus struct {
	Pid     int    `json:"pid"`
	Addr    string `json:"addr"`
	Version string `json:"version"`
}

// ctlMux builds the control endpoints served on the unix socket.
// stop/restart are injected so tests can run without os.Exit/exec.
func ctlMux(addr, version, sock string, stop, restart func()) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /ctl/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"pid":%d,"addr":%q,"version":%q}`, os.Getpid(), addr, version)
	})
	m.HandleFunc("POST /ctl/stop", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // 未读的请求体会让连接以 RST 收场，应答丢失
		flushAnd(w, func() {
			os.Remove(sock)
			stop()
		})
	})
	m.HandleFunc("POST /ctl/restart", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		flushAnd(w, restart)
	})
	return m
}

// checkAlreadyRunning reports whether a live serve instance owns the ctl
// socket（-d 启动前的前台预检，避免拉起子进程后才发现重复）。
func checkAlreadyRunning(tmp, ctlSock string) error {
	sock := ctlSock
	if sock == "" {
		sock = filepath.Join(tmp, defaultCtlSock)
	}
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("已有 profkit serve 在运行（%s 可连接）— 重启用 profkit ctl restart，停止用 profkit ctl stop", sock)
	}
	return nil
}

// listenCtl serves the ctl mux on the unix socket at sock, returning the
// listener (restart 需要把它作为 fd 传给子进程).
// 已有活实例（socket 可连）时拒绝启动，防止重复启动劫持老实例的管控通道；
// socket 文件存在但连不上（kill -9 残留）则清理后正常 bind。
func listenCtl(sock string, h http.Handler) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("已有 profkit serve 在运行（%s 可连接）— 重启用 profkit ctl restart，停止用 profkit ctl stop", sock)
	}
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen ctl socket %s: %w", sock, err)
	}
	go http.Serve(l, h)
	return l, nil
}

// flushAnd runs action after the response is fully flushed to the client.
func flushAnd(w http.ResponseWriter, action func()) {
	if f, ok := w.(http.Flusher); ok {
		io.WriteString(w, "ok")
		f.Flush()
	}
	action()
}

// unixClient returns an HTTP client that talks to the ctl unix socket.
// DisableKeepAlives：restart/stop 后旧进程退出，复用指向它的长连接只会
// 得到 EOF/RST，每次新建连接才能稳定命中接管的新进程。
func unixClient(sock string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}

// Ctl connects to the background serve's ctl socket and performs
// stop/restart/status. Socket path: -sock, default <storage>/profkit.sock
// (must match the serve instance's -ctl-sock when customized).
func Ctl(args []string) error {
	fs := flag.NewFlagSet("ctl", flag.ExitOnError)
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir (must match the serve instance)")
	sock := fs.String("sock", "", "ctl unix socket path (default <storage>/profkit.sock)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: profkit ctl <stop|restart|status>")
	}
	if *sock == "" {
		*sock = filepath.Join(*tmp, defaultCtlSock)
	}
	action := fs.Arg(0)

	if _, err := os.Stat(*sock); err != nil {
		return fmt.Errorf("没有运行中的 profkit serve（%s 不存在）", *sock)
	}
	client := unixClient(*sock, 5*time.Second)

	switch action {
	case "status":
		// 交接窗口内可能撞上旧进程退出产生的 RST，重试一次
		for attempt := 0; attempt < 2; attempt++ {
			resp, err := client.Get("http://local/ctl/status")
			if err != nil {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			var st ctlStatus
			err = json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			if err != nil {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			fmt.Printf("pid=%d addr=%s version=%s health=up\n", st.Pid, st.Addr, st.Version)
			return nil
		}
		return fmt.Errorf("连接 %s 失败", *sock)
	case "stop":
		resp, err := client.Post("http://local/ctl/stop", "", nil)
		if err != nil {
			return fmt.Errorf("连接 %s 失败: %w", *sock, err)
		}
		resp.Body.Close()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(*sock); err != nil { // 服务端 stop 时删除 socket 文件
				fmt.Println("stopped")
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("stop 请求已发出但 socket 仍在")
	case "restart":
		// 记录旧 pid：应答来自新进程才算重启完成（父进程排水期间仍在应答）
		var oldPid int
		if resp, err := client.Get("http://local/ctl/status"); err == nil {
			var st ctlStatus
			json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			oldPid = st.Pid
		}
		resp, err := client.Post("http://local/ctl/restart", "", nil)
		if err != nil {
			return fmt.Errorf("连接 %s 失败: %w", *sock, err)
		}
		resp.Body.Close()
		// deadline 覆盖最长排水期（max-cpu-seconds 内的在途采集）
		deadline := time.Now().Add(65 * time.Second)
		for time.Now().Before(deadline) {
			if resp, err := client.Get("http://local/ctl/status"); err == nil {
				var st ctlStatus
				json.NewDecoder(resp.Body).Decode(&st)
				resp.Body.Close()
				if st.Pid != 0 && st.Pid != oldPid {
					fmt.Printf("restarted, pid=%d addr=%s\n", st.Pid, st.Addr)
					return nil
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("restart 请求已发出但服务未恢复")
	default:
		return fmt.Errorf("未知操作 %q（want stop|restart|status）", action)
	}
}
