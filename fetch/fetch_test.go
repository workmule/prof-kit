package fetch

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/workmule/prof-kit/core"
)

func goroutineProfileBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRunEndToEnd(t *testing.T) {
	prof := goroutineProfileBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(prof)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	out := t.TempDir() + "/report.html"
	// flags must precede positionals (standard Go flag parsing)
	err := Run([]string{"-o", out, "-open=false", host, "goroutine"})
	if err != nil {
		t.Fatal(err)
	}
	htmlBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	h := string(htmlBytes)
	if !strings.Contains(h, "const DATA") || !strings.Contains(h, `id="flame"`) {
		t.Error("report missing interactive flame graph")
	}
	if !strings.Contains(h, "goroutine") {
		t.Error("report missing sample type data")
	}
}

func TestReportEscapes(t *testing.T) {
	data := []core.SampleTypeData{{
		Name: "heap", Unit: "space", Total: 5,
		Tree: &core.TreeNode{Name: "root", Cum: 5, Children: []*core.TreeNode{{Name: "<script>alert(1)</script>", Cum: 5}}},
		Flat: map[string]int64{"<script>alert(1)</script>": 5},
	}}
	h := Report("heap", "t", "top", data)
	if strings.Contains(h, "<script>alert") {
		t.Error("HTML not escaped")
	}
	if !strings.Contains(h, "u003c") {
		t.Error("expected JSON-escaped angle brackets in data payload")
	}
}
