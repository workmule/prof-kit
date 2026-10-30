package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
)

func TestPProfWhitelist(t *testing.T) {
	for _, ok := range []string{"", "heap", "profile", "goroutine", "symbol"} {
		if !PProfTypes[ok] {
			t.Errorf("PProfTypes[%q] = false, want true", ok)
		}
	}
	for _, bad := range []string{"../etc/passwd", "heap/../../x", "Profile", "heap?x"} {
		if PProfTypes[bad] {
			t.Errorf("PProfTypes[%q] = true, want false", bad)
		}
	}
	if _, err := ProfileURL("h:1", "../secret", ""); err == nil {
		t.Error("ProfileURL accepted non-whitelisted type")
	}
}

// goroutineProfileBytes produces a real, parseable pprof profile for tests.
func goroutineProfileBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchTopTree(t *testing.T) {
	prof := goroutineProfileBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/pprof/goroutine" {
			http.NotFound(w, r)
			return
		}
		w.Write(prof)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	path, err := Fetch(host, "goroutine", 0, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fetched file missing: %v", err)
	}

	rep, err := Top(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep, "goroutine profile") && !strings.Contains(rep, "type=goroutine") {
		t.Errorf("top report missing goroutine header:\n%s", rep)
	}
	if !strings.Contains(rep, "total=") || strings.Contains(rep, "total=0") {
		t.Errorf("top report total should be > 0:\n%s", rep)
	}

	tree, err := Tree(path)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Name != "root" || tree.Cum == 0 {
		t.Errorf("tree root wrong: %+v", tree)
	}
}

func TestFetchRejectsBadKind(t *testing.T) {
	if _, err := Fetch("127.0.0.1:1", "trace", 0, t.TempDir(), ""); err == nil {
		t.Error("Fetch accepted non-fetchable kind")
	}
}

func TestCPUCapped(t *testing.T) {
	var gotSeconds string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSeconds = r.URL.Query().Get("seconds")
		w.Write(nil) // empty body: fetch succeeds, parse never happens here
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	old := MaxCPUSeconds
	MaxCPUSeconds = 60
	defer func() { MaxCPUSeconds = old }()

	path, err := Fetch(host, "cpu", 9999, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Dir(path))
	if gotSeconds != "60" {
		t.Errorf("upstream seconds = %q, want capped 60", gotSeconds)
	}
}
