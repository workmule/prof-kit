package core

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
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

	rep, err := Top(path, 10, "")
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

func TestFetchPrunesBeyondMaxProfiles(t *testing.T) {
	prof := goroutineProfileBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(prof)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	old := MaxProfiles
	MaxProfiles = 2
	defer func() { MaxProfiles = old }()

	dir := t.TempDir()
	var last string
	for i := 0; i < 3; i++ {
		p, err := Fetch(host, "goroutine", 0, dir, fmt.Sprintf("t%d", i)) // task 前缀防同秒重名
		if err != nil {
			t.Fatal(err)
		}
		last = p
		time.Sleep(10 * time.Millisecond) // 保证 mtime 可区分
	}
	list, err := ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("kept %d profiles, want 2: %+v", len(list), list)
	}
	if list[0].Path != last {
		t.Errorf("kept %s, want newest %s", list[0].Path, last)
	}
}

func TestTopFocusDiffList(t *testing.T) {
	prof := goroutineProfileBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(prof)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	dir := t.TempDir()
	p1, err := Fetch(host, "goroutine", 0, dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Fetch(host, "goroutine", 0, dir, "b")
	if err != nil {
		t.Fatal(err)
	}

	// focus narrows the sample set; a bogus focus yields an empty report.
	full, err := Top(p1, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "total=") {
		t.Errorf("top missing total:\n%s", full)
	}
	empty, err := Top(p1, 10, "no-such-func-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, "total=0") {
		t.Errorf("bogus focus should give total=0:\n%s", empty)
	}

	// diff of the same profile twice: every data-row delta must be zero.
	rep, err := Diff(p1, p2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep, "(Δ+0)") {
		t.Errorf("self-diff total delta should be 0:\n%s", rep)
	}
	for _, line := range strings.Split(rep, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] == "" || f[0][0] < '0' || f[0][0] > '9' { // 只校验数据行
			continue
		}
		if f[0] != "0" || f[1] != "+0" {
			t.Errorf("self-diff nonzero delta: %q", line)
		}
	}

	// list sees both files, newest first (force distinct mtimes).
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p1, old, old); err != nil {
		t.Fatal(err)
	}
	list, err := ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Path != p2 || list[1].Path != p1 {
		t.Errorf("ListProfiles = %+v, want %s before %s", list, p2, p1)
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
