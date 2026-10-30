package serve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestViewHandlerRouting(t *testing.T) {
	viewSets["unit"] = map[string]http.Handler{
		"/":           redirectStub("/flamegraph"),
		"/flamegraph": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "FLAME") }),
		"/graph":      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "GRAPH") }),
	}
	viewLRU = append(viewLRU, "unit")
	defer func() { delete(viewSets, "unit"); viewLRU = nil }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /view/{rest...}", viewHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for path, want := range map[string]string{
		"/view/unit/graph":      "GRAPH",
		"/view/unit/flamegraph": "FLAME",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != want {
			t.Errorf("%s: status %d body %q, want 200 %q", path, resp.StatusCode, body, want)
		}
	}
	// "/" is the official 301 redirect to flamegraph (don't follow: assert the hop)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/view/unit/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("/view/unit/: status %d, want 301", resp.StatusCode)
	}
}

func redirectStub(to string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", to)
		w.WriteHeader(http.StatusMovedPermanently)
	})
}
