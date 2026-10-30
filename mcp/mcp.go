// Package mcp implements the profkit MCP server. The `profkit mcp` command
// speaks stdio; `profkit serve` mounts the same tools over streamable HTTP
// at /mcp. Tools are thin shells over core.
package mcp

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/workmule/prof-kit/core"
)

// NewServer builds the MCP server with all tools. Shared by stdio Run and
// serve's /mcp mount; tmp is the profile storage dir.
func NewServer(version, tmp string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "profkit", Version: version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_profile",
		Description: "Fetch a profile (cpu|heap|goroutine|allocs|block|mutex) from a Go service and save it locally. Returns the saved file path for use with top/flamegraph_data.",
	}, fetchTool(tmp))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "profile_data",
		Description: "Fetch a profile (cpu|heap|goroutine|allocs|block|mutex) from a Go service, save it locally and return the top report inline. The returned path feeds top/flamegraph_data for further analysis.",
	}, profileDataTool(tmp))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "top",
		Description: "Flat top-N report (text) for a saved profile file path. Optional focus restricts samples to stacks containing a function.",
	}, topTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "diff_profiles",
		Description: "Compare two saved profiles (e.g. before/after an optimization or two load runs) and return a per-function delta report, like `go tool pprof -base`.",
	}, diffTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_profiles",
		Description: "List saved profile files in the storage dir (newest first), so previously collected profiles can be found for top/flamegraph_data/diff_profiles.",
	}, listProfilesTool(tmp))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "flamegraph_data",
		Description: "Call tree JSON (cumulative values) for a saved profile file path, for visualization or further analysis.",
	}, treeTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "goroutines",
		Description: "Fetch the goroutine profile from a Go service and return a top-N stack summary.",
	}, goroutinesTool(tmp))
	return server
}

// HTTPHandler returns the streamable-HTTP handler for serve to mount at /mcp.
func HTTPHandler(version, tmp string) http.Handler {
	server := NewServer(version, tmp)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}

// Run parses flags from args and blocks until the stdio server stops.
// version is the build-injected version string (see Makefile).
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec

	fmt.Fprintln(os.Stderr, "profkit mcp on stdio")
	return NewServer(version, *tmp).Run(context.Background(), &mcp.StdioTransport{})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type fetchIn struct {
	Target  string `json:"target" jsonschema:"host:port of the Go service"`
	Kind    string `json:"kind" jsonschema:"cpu, heap, goroutine, allocs, block or mutex"`
	Seconds int    `json:"seconds,omitempty" jsonschema:"cpu profile duration in seconds (default 30)"`
	Task    string `json:"task,omitempty" jsonschema:"optional label prefixed to the saved file name"`
}

type fetchOut struct {
	Path string `json:"path"`
}

// fetchTool needs the storage dir, so it is built as a closure.
func fetchTool(tmp string) mcp.ToolHandlerFor[fetchIn, fetchOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in fetchIn) (*mcp.CallToolResult, fetchOut, error) {
		path, err := core.Fetch(in.Target, in.Kind, in.Seconds, tmp, in.Task)
		if err != nil {
			return nil, fetchOut{}, err
		}
		return nil, fetchOut{Path: path}, nil
	}
}

type topIn struct {
	Path  string `json:"path" jsonschema:"profile file path returned by fetch_profile/profile_data"`
	N     int    `json:"n,omitempty" jsonschema:"top N entries (default 20)"`
	Focus string `json:"focus,omitempty" jsonschema:"only samples whose stack contains this function name (substring)"`
}

type topOut struct {
	Report string `json:"report"`
}

func topTool(ctx context.Context, req *mcp.CallToolRequest, in topIn) (*mcp.CallToolResult, topOut, error) {
	rep, err := core.Top(in.Path, in.N, in.Focus)
	if err != nil {
		return nil, topOut{}, err
	}
	return nil, topOut{Report: rep}, nil
}

type diffIn struct {
	Base string `json:"base" jsonschema:"base profile file path (the 'before' side)"`
	New  string `json:"new" jsonschema:"new profile file path (the 'after' side)"`
	N    int    `json:"n,omitempty" jsonschema:"top N entries (default 20)"`
}

type diffOut struct {
	Report string `json:"report"`
}

func diffTool(ctx context.Context, req *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, diffOut, error) {
	rep, err := core.Diff(in.Base, in.New, in.N)
	if err != nil {
		return nil, diffOut{}, err
	}
	return nil, diffOut{Report: rep}, nil
}

type listProfilesOut struct {
	Profiles []core.ProfileFileInfo `json:"profiles"`
}

// listProfilesTool needs the storage dir, so it is built as a closure.
func listProfilesTool(tmp string) mcp.ToolHandlerFor[struct{}, listProfilesOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, listProfilesOut, error) {
		profs, err := core.ListProfiles(tmp)
		if err != nil {
			return nil, listProfilesOut{}, err
		}
		return nil, listProfilesOut{Profiles: profs}, nil
	}
}

type treeIn struct {
	Path string `json:"path" jsonschema:"profile file path returned by fetch_profile/profile_data"`
}

type treeOut struct {
	// JSON of core.TreeNode. Returned as a string because the SDK cannot
	// build an output schema for the recursive node type.
	JSON string `json:"json" jsonschema:"call tree: {name, cum, children[]}"`
}

func treeTool(ctx context.Context, req *mcp.CallToolRequest, in treeIn) (*mcp.CallToolResult, treeOut, error) {
	tree, err := core.Tree(in.Path)
	if err != nil {
		return nil, treeOut{}, err
	}
	b, err := json.Marshal(tree)
	if err != nil {
		return nil, treeOut{}, err
	}
	return nil, treeOut{JSON: string(b)}, nil
}

type goroutinesIn struct {
	Target string `json:"target" jsonschema:"host:port of the Go service"`
	N      int    `json:"n,omitempty" jsonschema:"top N stacks (default 10)"`
}

type goroutinesOut struct {
	Report string `json:"report"`
}

// goroutinesTool needs the storage dir, so it is built as a closure.
func goroutinesTool(tmp string) mcp.ToolHandlerFor[goroutinesIn, goroutinesOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in goroutinesIn) (*mcp.CallToolResult, goroutinesOut, error) {
		path, err := core.Fetch(in.Target, "goroutine", 0, tmp, "")
		if err != nil {
			return nil, goroutinesOut{}, err
		}
		rep, err := core.Top(path, in.N, "")
		if err != nil {
			return nil, goroutinesOut{}, err
		}
		return nil, goroutinesOut{Report: rep}, nil
	}
}

type profileDataIn struct {
	Target   string `json:"target" jsonschema:"host:port of the Go service"`
	Kind     string `json:"kind" jsonschema:"cpu, heap, goroutine, allocs, block or mutex"`
	Seconds  int    `json:"seconds,omitempty" jsonschema:"cpu profile duration in seconds (default 30)"`
	N        int    `json:"n,omitempty" jsonschema:"top N entries in report (default 20)"`
	Task     string `json:"task,omitempty" jsonschema:"optional label prefixed to the saved file name"`
	WithTree bool   `json:"with_tree,omitempty" jsonschema:"also return the cumulative call tree JSON"`
}

type profileDataOut struct {
	Path   string `json:"path"`
	Report string `json:"report"`
	Tree   string `json:"tree,omitempty" jsonschema:"call tree JSON: {name, cum, children[]}; present when with_tree=true"`
}

// profileDataTool saves the profile (for later top/flamegraph_data) and
// returns the analyzed report inline.
func profileDataTool(tmp string) mcp.ToolHandlerFor[profileDataIn, profileDataOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in profileDataIn) (*mcp.CallToolResult, profileDataOut, error) {
		path, err := core.Fetch(in.Target, in.Kind, in.Seconds, tmp, in.Task)
		if err != nil {
			return nil, profileDataOut{}, err
		}
		rep, err := core.Top(path, in.N, "")
		if err != nil {
			return nil, profileDataOut{}, err
		}
		out := profileDataOut{Path: path, Report: rep}
		if in.WithTree {
			tree, err := core.Tree(path)
			if err != nil {
				return nil, profileDataOut{}, err
			}
			b, err := json.Marshal(tree)
			if err != nil {
				return nil, profileDataOut{}, err
			}
			out.Tree = string(b)
		}
		return nil, out, nil
	}
}
