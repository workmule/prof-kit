// Multi-profile view registry: mounts official pprof handlers (from the
// webui package) under /view/<id>/, keeping up to maxViews in memory with
// FIFO eviction. See webui package for the embedding glue.
package serve

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"profkit/webui"
)

const maxViews = 8 // // ponytail: fixed-cap FIFO — enough for a compare session; bump if needed

var (
	viewMu   sync.Mutex
	viewLRU  []string                               // oldest first
	viewSets = map[string]map[string]http.Handler{} // id -> official handlers
)

// mountView parses the profile in-process (no subprocess) and registers the
// official handlers under /view/<id>/, where id is the profile file name.
func mountView(path string) (string, error) {
	handlers, err := webui.Mount(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(filepath.Base(path), ".pb.gz")
	viewMu.Lock()
	defer viewMu.Unlock()
	viewSets[id] = handlers
	viewLRU = append(viewLRU, id)
	for len(viewLRU) > maxViews {
		delete(viewSets, viewLRU[0])
		viewLRU = viewLRU[1:]
	}
	return id, nil
}

// viewHandler serves the official handlers under /view/{id}/{page}.
// The webui pages use relative links (./top, ./flamegraph), so any mount
// point works.
func viewHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/view"), "/")
	if rest == "" {
		http.Error(w, "usage: /view/<id>/{flamegraph,top,graph,source,peek}", http.StatusNotFound)
		return
	}
	id, page, _ := strings.Cut(rest, "/")
	viewMu.Lock()
	handlers, ok := viewSets[id]
	// touch LRU: move id to newest
	for i, v := range viewLRU {
		if v == id {
			viewLRU = append(append(viewLRU[:i:i], viewLRU[i+1:]...), id)
			break
		}
	}
	viewMu.Unlock()
	if !ok {
		http.Error(w, "unknown view id (evicted or never fetched)", http.StatusNotFound)
		return
	}
	if page != "" {
		page = "/" + page // handler keys are root-form ("/flamegraph")
	} else {
		page = "/"
	}
	h, ok := handlers[page]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}
