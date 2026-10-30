// Multi-profile view registry: official pprof handlers (from the webui
// package) are mounted under /view/<id>/, id being the profile file name.
//
// The registry survives restarts: viewPaths is rebuilt from the storage dir
// on startup (newest maxViews files, older ones deleted). Handlers are parsed
// lazily on first view and cached in viewParsed until their file is evicted.
package serve

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"profkit/webui"
)

const maxViews = 8 // // ponytail: fixed-cap FIFO — enough for a compare session; bump if needed

var (
	viewMu     sync.Mutex
	viewLRU    []string                               // oldest first, ids
	viewPaths  = map[string]string{}                  // id -> profile file path (registry, rebuilt from disk)
	viewParsed = map[string]map[string]http.Handler{} // id -> official handlers (lazy cache)
)

// rebuildViews scans dir for profiles, keeps the newest maxViews as views and
// deletes older files so disk stays in sync with the in-memory registry.
func rebuildViews(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir missing/empty: nothing to rebuild
	}
	type file struct {
		id, path string
		mtime    int64
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pb.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{strings.TrimSuffix(e.Name(), ".pb.gz"), filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime }) // newest first

	viewMu.Lock()
	defer viewMu.Unlock()
	for i, f := range files {
		if i < maxViews {
			viewPaths[f.id] = f.path
			viewLRU = append([]string{f.id}, viewLRU...) // newest ends up first
		} else {
			os.Remove(f.path) // 清理：超出保留数的旧文件
		}
	}
}

// mountView registers a freshly collected profile as a view and prunes the
// storage dir beyond maxViews.
func mountView(path string) (string, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".pb.gz")
	viewMu.Lock()
	defer viewMu.Unlock()
	viewPaths[id] = path
	viewLRU = append(viewLRU, id)
	for len(viewLRU) > maxViews {
		old := viewLRU[0]
		viewLRU = viewLRU[1:]
		if p, ok := viewPaths[old]; ok {
			os.Remove(p)
		}
		delete(viewPaths, old)
		delete(viewParsed, old)
	}
	return id, nil
}

// viewHandler serves the official handlers under /view/{id}/{page},
// parsing the profile lazily on first access. The webui pages use relative
// links (./top, ./flamegraph), so any mount point works.
func viewHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/view"), "/")
	if rest == "" {
		http.Error(w, "usage: /view/<id>/{flamegraph,top,graph,source,peek}", http.StatusNotFound)
		return
	}
	id, page, _ := strings.Cut(rest, "/")
	viewMu.Lock()
	path, ok := viewPaths[id]
	if ok { // touch LRU: move id to newest
		for i, v := range viewLRU {
			if v == id {
				viewLRU = append(append(viewLRU[:i:i], viewLRU[i+1:]...), id)
				break
			}
		}
	}
	handlers, cached := viewParsed[id]
	viewMu.Unlock()
	if !ok {
		http.Error(w, "unknown view id (evicted or never fetched)", http.StatusNotFound)
		return
	}
	if !cached {
		h, err := webui.Mount(path)
		if err != nil {
			http.Error(w, "parse profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		viewMu.Lock()
		viewParsed[id] = h
		viewMu.Unlock()
		handlers = h
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
