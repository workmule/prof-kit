package serve

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
)

func writeTestProfile(t *testing.T, dir, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".pb.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestViewHandlerRouting(t *testing.T) {
	dir := t.TempDir()
	writeTestProfile(t, dir, "unit")
	viewPaths["unit"] = filepath.Join(dir, "unit.pb.gz")
	viewLRU = append(viewLRU, "unit")
	defer func() {
		delete(viewPaths, "unit")
		delete(viewParsed, "unit")
		viewLRU = nil
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /view/{rest...}", viewHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/view/unit/graph", "/view/unit/flamegraph"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// 测试环境的 goroutine profile 样本极少，页面可能是空图 — 只断言 200 且有内容
		if resp.StatusCode != 200 || len(body) == 0 {
			t.Errorf("%s: status %d body %d B, want 200 with content", path, resp.StatusCode, len(body))
		}
	}

	// unknown id → 404; "/" → official 301 to flamegraph
	if resp, _ := http.Get(srv.URL + "/view/nope/graph"); resp.StatusCode != 404 {
		t.Errorf("unknown id: status %d, want 404", resp.StatusCode)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, _ := client.Get(srv.URL + "/view/unit/"); resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("/: status %d, want 301", resp.StatusCode)
	}
}
