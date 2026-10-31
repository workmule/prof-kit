package serve

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain 兼职 detach 的子进程角色：被 detach 以 ["<测试二进制>", "serve", ...]
// 拉起时（TEST_READY 注入回写内容），按注入模式回写 ready 后退出，不打跑测试。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if os.Getenv("TEST_READY") == "err" {
			notifyReady(errors.New("listen tcp :8899: bind: mock conflict"))
		} else {
			notifyReady(nil)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// detach ready 握手端到端：子进程就绪回写 ok → 父进程报成功；
// 回写 err:... → 父进程以非 nil error 透传原因（端口冲突等不再假启动成功）。
func TestDetachReadyHandshake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inject  string
		wantErr string
	}{
		{"success", "ok", ""},
		{"bind conflict", "err", "mock conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_READY", tc.inject)
			err := detach(nil, t.TempDir())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// socket 通道端到端：status 应答 / stop 触发注入动作（不真退进程）。
func TestCtlOverUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	stopped := make(chan struct{})
	mux := ctlMux("127.0.0.1:9999", "test", sock, func() { close(stopped) }, func() {})
	l, err := listenCtl(sock, mux)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// 防重：已有活实例时再次 listenCtl 必须拒绝
	if _, err := listenCtl(sock, mux); err == nil {
		t.Error("listenCtl 应拒绝活实例上的重复启动")
	}
	// 残留死文件（无进程监听）应被清理并正常 bind
	l.Close()
	if _, err := listenCtl(sock, mux); err != nil {
		t.Fatalf("残留死文件应放行: %v", err)
	}
	client := unixClient(sock, 2*time.Second)

	resp, err := client.Get("http://local/ctl/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Pid  int    `json:"pid"`
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.Pid != 0 && st.Addr != "127.0.0.1:9999" {
		t.Errorf("status = %+v", st)
	}

	if _, err := client.Post("http://local/ctl/stop", "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("stop action not triggered")
	}
}
