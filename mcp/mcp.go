// Package mcp implements the profkit MCP server (stdio by default,
// Streamable HTTP via -http). Tools are thin shells over core.
package mcp

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"profkit/core"
)

// Run parses flags from args and blocks until the server stops.
// version is the build-injected version string (see Makefile).
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	httpAddr := fs.String("http", envOr("PPROF_MCP_ADDR", ""), "streamable HTTP listen addr (empty = stdio)")
	tmp := fs.String("t", envOr("PPROF_TMP_PATH", "/tmp/pprof-web"), "profile storage dir")
	maxSec := fs.Int("max-cpu-seconds", core.MaxCPUSeconds, "max CPU profile seconds")
	fs.Parse(args)
	core.MaxCPUSeconds = *maxSec

	server := mcp.NewServer(&mcp.Implementation{Name: "profkit", Version: version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_profile",
		Description: "Fetch a profile (cpu|heap|goroutine|allocs) from a Go service and save it locally. Returns the saved file path for use with top/flamegraph_data.",
	}, fetchTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "top",
		Description: "Flat top-N report (text) for a saved profile file path.",
	}, topTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "flamegraph_data",
		Description: "Call tree JSON (cumulative values) for a saved profile file path, for visualization or further analysis.",
	}, treeTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "goroutines",
		Description: "Fetch the goroutine profile from a Go service and return a top-N stack summary.",
	}, goroutinesTool(*tmp))

	if *httpAddr != "" {
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		fmt.Printf("profkit mcp (streamable http) on %s (storage %s)\n", *httpAddr, *tmp)
		return http.ListenAndServe(*httpAddr, h)
	}
	fmt.Fprintln(os.Stderr, "profkit mcp on stdio")
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type fetchIn struct {
	Target  string `json:"target" jsonschema:"host:port of the Go service"`
	Kind    string `json:"kind" jsonschema:"cpu, heap, goroutine or allocs"`
	Seconds int    `json:"seconds,omitempty" jsonschema:"cpu profile duration in seconds (default 30)"`
}

type fetchOut struct {
	Path string `json:"path"`
}

func fetchTool(ctx context.Context, req *mcp.CallToolRequest, in fetchIn) (*mcp.CallToolResult, fetchOut, error) {
	path, err := core.Fetch(in.Target, in.Kind, in.Seconds, envOr("PPROF_TMP_PATH", "/tmp/pprof-web"))
	if err != nil {
		return nil, fetchOut{}, err
	}
	return nil, fetchOut{Path: path}, nil
}

type topIn struct {
	Path string `json:"path" jsonschema:"profile file path returned by fetch_profile"`
	N    int    `json:"n,omitempty" jsonschema:"top N entries (default 20)"`
}

type topOut struct {
	Report string `json:"report"`
}

func topTool(ctx context.Context, req *mcp.CallToolRequest, in topIn) (*mcp.CallToolResult, topOut, error) {
	rep, err := core.Top(in.Path, in.N)
	if err != nil {
		return nil, topOut{}, err
	}
	return nil, topOut{Report: rep}, nil
}

type treeIn struct {
	Path string `json:"path" jsonschema:"profile file path returned by fetch_profile"`
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
		path, err := core.Fetch(in.Target, "goroutine", 0, tmp)
		if err != nil {
			return nil, goroutinesOut{}, err
		}
		rep, err := core.Top(path, in.N)
		if err != nil {
			return nil, goroutinesOut{}, err
		}
		return nil, goroutinesOut{Report: rep}, nil
	}
}
