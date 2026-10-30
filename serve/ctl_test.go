package serve

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

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
