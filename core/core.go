// Package core wires profile fetching and analysis.
// Fetch/proxy/whitelist are hand-written glue; all profile parsing and
// aggregation sits on github.com/google/pprof (see AGENTS.md hard rule 1).
package core

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/pprof/profile"
)

// MaxCPUSeconds caps CPU profile duration. Configurable via -max-cpu-seconds
// on serve/mcp (AGENTS.md hard rule 5: never hardcode).
var MaxCPUSeconds = 60

// PProfTypes is the endpoint whitelist. Anything else is rejected
// (anti open-relay/SSRF, docs/prof-web.md §一).
var PProfTypes = map[string]bool{
	"": true, "allocs": true, "block": true, "cmdline": true,
	"goroutine": true, "heap": true, "mutex": true, "profile": true,
	"threadcreate": true, "trace": true, "symbol": true,
}

// ProfileURL builds the upstream pprof URL for a whitelisted type.
func ProfileURL(target, ptype, query string) (string, error) {
	if !PProfTypes[ptype] {
		return "", fmt.Errorf("pprof type %q not in whitelist", ptype)
	}
	u := "http://" + target + "/debug/pprof/" + ptype
	if query != "" {
		u += "?" + query
	}
	return u, nil
}

var fetchKinds = map[string]bool{"cpu": true, "heap": true, "goroutine": true, "allocs": true, "block": true, "mutex": true}

// MaxProfiles caps stored profile files per storage dir; the oldest are
// removed beyond the cap after each collection. Configurable via
// PPROF_MAX_PROFILES (<=0 keeps everything).
var MaxProfiles = 30

func init() {
	if v := os.Getenv("PPROF_MAX_PROFILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			MaxProfiles = n
		}
	}
}

// pruneProfiles removes the oldest saved profiles beyond MaxProfiles.
func pruneProfiles(dir string) {
	if MaxProfiles <= 0 {
		return
	}
	list, err := ListProfiles(dir) // newest first
	if err != nil || len(list) <= MaxProfiles {
		return
	}
	for _, p := range list[MaxProfiles:] {
		os.Remove(p.Path)
	}
}

// Fetch pulls one profile from target ("host:port") and saves it under dir.
// task is an optional label (sanitized) prefixed to the file name to
// distinguish collections; empty means auto naming. Returns the saved path.
func Fetch(target, kind string, seconds int, dir, task string) (string, error) {
	if !fetchKinds[kind] {
		return "", fmt.Errorf("unsupported profile kind %q (want cpu|heap|goroutine|allocs|block|mutex)", kind)
	}
	ptype, query, timeout := kind, "", 30*time.Second
	if kind == "cpu" {
		ptype = "profile"
		if seconds > MaxCPUSeconds {
			seconds = MaxCPUSeconds
		}
		if seconds <= 0 {
			seconds = 30
		}
		query = fmt.Sprintf("seconds=%d", seconds)
		timeout = time.Duration(seconds+5) * time.Second
	}
	u, err := ProfileURL(target, ptype, query)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: timeout}).Get(u)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("fetch %s: status %d: %s", u, resp.StatusCode, body)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s_%s.pb.gz", kind, strings.ReplaceAll(target, ":", "_"), time.Now().Format("20060102_150405"))
	if task != "" {
		name = sanitizeTask(task) + "_" + name
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	pruneProfiles(dir)
	return path, nil
}

func parseFile(path string) (*profile.Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return profile.Parse(f)
}

// valueIndex picks the sample value to report.
// ponytail: last type is the meaningful one (cpu→nanoseconds, heap→inuse_space);
// make it selectable if someone needs the other column.
func valueIndex(p *profile.Profile) int { return len(p.SampleType) - 1 }

func locName(l *profile.Location) string {
	for _, ln := range l.Line {
		if ln.Function != nil && ln.Function.Name != "" {
			return ln.Function.Name
		}
	}
	return "??"
}

// Top renders a flat top-N report (text, directly readable by humans and LLMs).
// focus (substring matched against function names) restricts samples to stacks
// containing it, like `go tool pprof -focus`; empty means all samples.
func Top(path string, n int, focus string) (string, error) {
	p, err := parseFile(path)
	if err != nil {
		return "", err
	}
	return topOf(p, n, focus)
}

func stackHas(locations []*profile.Location, focus string) bool {
	for _, l := range locations {
		if strings.Contains(locName(l), focus) {
			return true
		}
	}
	return false
}

// aggregate sums flat (leaf) and cumulative values per function over the last
// sample type (cpu→nanoseconds, heap→inuse_space, ...).
func aggregate(p *profile.Profile, focus string) (flat, cum map[string]int64, total int64, st *profile.ValueType) {
	idx := valueIndex(p)
	st = p.SampleType[idx]
	flat, cum = map[string]int64{}, map[string]int64{}
	for _, s := range p.Sample {
		if len(s.Value) <= idx {
			continue
		}
		if focus != "" && !stackHas(s.Location, focus) {
			continue
		}
		v := s.Value[idx]
		total += v
		if len(s.Location) > 0 {
			flat[locName(s.Location[0])] += v // sample locations are leaf-first
		}
		seen := map[string]bool{}
		for _, l := range s.Location {
			nm := locName(l)
			if !seen[nm] {
				seen[nm] = true
				cum[nm] += v
			}
		}
	}
	return flat, cum, total, st
}

func topOf(p *profile.Profile, n int, focus string) (string, error) {
	if n <= 0 {
		n = 20
	}
	flat, cum, total, st := aggregate(p, focus)
	type item struct {
		name      string
		flat, cum int64
	}
	items := make([]item, 0, len(cum))
	for nm, c := range cum {
		items = append(items, item{nm, flat[nm], c})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].flat > items[j].flat })
	var b strings.Builder
	fmt.Fprintf(&b, "type=%s unit=%s total=%d\n\n%12s %12s  %s\n", st.Type, st.Unit, total, "flat", "cum", "name")
	for i, it := range items {
		if i == n {
			break
		}
		fmt.Fprintf(&b, "%12d %12d  %s\n", it.flat, it.cum, it.name)
	}
	return b.String(), nil
}

// TreeNode is a call-tree node with cumulative values, for flamegraph-style clients.
type TreeNode struct {
	Name     string      `json:"name"`
	Cum      int64       `json:"cum"`
	Children []*TreeNode `json:"children,omitempty"`
}

// Tree builds a cumulative call tree from a saved profile (last sample type).
func Tree(path string) (*TreeNode, error) {
	data, err := FlameData(path)
	if err != nil {
		return nil, err
	}
	return data[len(data)-1].Tree, nil
}

// SampleTypeData is one value dimension of a profile: its call tree plus
// leaf-attributed flat values per function.
type SampleTypeData struct {
	Name  string           `json:"name"`
	Unit  string           `json:"unit"`
	Total int64            `json:"total"`
	Tree  *TreeNode        `json:"tree"`
	Flat  map[string]int64 `json:"flat"`
}

// FlameData parses the profile once and returns every sample-type dimension
// (e.g. cpu profiles have samples/count + cpu/nanoseconds).
func FlameData(path string) ([]SampleTypeData, error) {
	p, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]SampleTypeData, 0, len(p.SampleType))
	for idx := range p.SampleType {
		d := SampleTypeData{
			Name: p.SampleType[idx].Type, Unit: p.SampleType[idx].Unit,
			Tree: &TreeNode{Name: "root"}, Flat: map[string]int64{},
		}
		for _, s := range p.Sample {
			if len(s.Value) <= idx {
				continue
			}
			v := s.Value[idx]
			d.Total += v
			d.Tree.Cum += v
			if len(s.Location) > 0 {
				d.Flat[locName(s.Location[0])] += v // leaf-attributed
			}
			walkStack(d.Tree, s.Location, v)
		}
		out = append(out, d)
	}
	return out, nil
}

// sanitizeTask keeps only filename-safe chars; anything else becomes '_'.
func sanitizeTask(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ProfileFileInfo describes one saved profile on disk.
type ProfileFileInfo struct {
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	SizeKB  int64     `json:"size_kb"`
	ModTime time.Time `json:"mod_time"`
}

// ListProfiles lists saved profiles in dir (storage convention <dir>/*.pb.gz),
// newest first. Kind is taken from the file-name convention.
func ListProfiles(dir string) ([]ProfileFileInfo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ProfileFileInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pb.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind, _, _ := strings.Cut(e.Name(), "_")
		out = append(out, ProfileFileInfo{
			Path:    filepath.Join(dir, e.Name()),
			Kind:    kind,
			SizeKB:  info.Size() / 1024,
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// Diff renders a top-N delta report (new − base) per function, sorted by
// absolute cumulative delta — the `go tool pprof -base` workflow: compare
// before/after an optimization or across two load runs.
func Diff(basePath, newPath string, n int) (string, error) {
	if n <= 0 {
		n = 20
	}
	baseP, err := parseFile(basePath)
	if err != nil {
		return "", err
	}
	newP, err := parseFile(newPath)
	if err != nil {
		return "", err
	}
	bf, bc, bt, _ := aggregate(baseP, "")
	nf, nc, nt, nst := aggregate(newP, "")
	type item struct {
		name             string
		dFlat, dCum, cum int64
	}
	seen := map[string]bool{}
	var items []item
	for nm, c := range nc {
		seen[nm] = true
		items = append(items, item{nm, nf[nm] - bf[nm], c - bc[nm], c})
	}
	for nm, c := range bc {
		if !seen[nm] {
			items = append(items, item{nm, -bf[nm], -c, 0})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		ai, aj := items[i].dCum, items[j].dCum
		if ai < 0 {
			ai = -ai
		}
		if aj < 0 {
			aj = -aj
		}
		return ai > aj
	})
	var b strings.Builder
	fmt.Fprintf(&b, "diff (new − base) type=%s unit=%s total %d→%d (Δ%+d)\n\n%12s %12s %12s  %s\n",
		nst.Type, nst.Unit, bt, nt, nt-bt, "d_flat", "d_cum", "new_cum", "name")
	for i, it := range items {
		if i == n {
			break
		}
		fmt.Fprintf(&b, "%12d %+12d %12d  %s\n", it.dFlat, it.dCum, it.cum, it.name)
	}
	return b.String(), nil
}

func walkStack(root *TreeNode, locations []*profile.Location, v int64) {
	cur := root
	// locations are leaf-first; walk reversed so root is the entry frame
	for i := len(locations) - 1; i >= 0; i-- {
		nm := locName(locations[i])
		// ponytail: linear child scan, O(width) per insert — fine for
		// human-scale profiles; switch to a map per node if trees get wide.
		found := false
		for _, c := range cur.Children {
			if c.Name == nm {
				cur, found = c, true
				break
			}
		}
		if !found {
			c := &TreeNode{Name: nm}
			cur.Children = append(cur.Children, c)
			cur = c
		}
		cur.Cum += v
	}
}
