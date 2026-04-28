package mcp

import (
	"context"
	"maps"
	"slices"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/store"
)

// mcpGraphNode mirrors the node payload used by GET /graph so consumers
// parsing both surfaces share a decoder. Status is intentionally omitted
// here: runtime-state wiring belongs in a follow-up; callers can resolve
// per-agent health via list_agents.
type mcpGraphNode struct {
	ID string `json:"id"`
}

// mcpGraphEdge mirrors the edge payload used by GET /graph, with timestamps
// normalised to RFC3339 for wire parity.
type mcpGraphEdge struct {
	From       string             `json:"from"`
	To         string             `json:"to"`
	Count      int                `json:"count"`
	Dispatches []mcpGraphDispatch `json:"dispatches"`
}

type mcpGraphDispatch struct {
	At     string `json:"at"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Reason string `json:"reason"`
}

// toolListEvents enumerates recent events. The optional `since` argument
// parses as RFC3339; a blank or unparseable value behaves like no filter,
// matching GET /events so the two surfaces stay aligned.
func toolListEvents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		var since time.Time
		if raw, _ := trimmedStringOptional(req, "since"); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				since = t
			}
		}
		return jsonResult(nilSafe(deps.Observe.ListEvents(since)))
	}
}

// toolListTraces returns the 200 most recent spans verbatim. The Span JSON
// shape already matches GET /traces so clients can parse both surfaces with
// the same decoder.
func toolListTraces(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return jsonResult(nilSafe(deps.Observe.ListTraces()))
	}
}

// toolGetTrace returns every span for a given root event ID. An empty result
// is reported as a tool-level error so clients surface a clear "not found"
// message rather than an empty array that callers might mistake for success.
func toolGetTrace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "root_event_id")
		if !ok {
			return mcpgo.NewToolResultError("root_event_id is required"), nil
		}
		spans := deps.Observe.TracesByRootEventID(id)
		if len(spans) == 0 {
			return mcpgo.NewToolResultErrorf("trace %q not found", id), nil
		}
		return jsonResult(spans)
	}
}

// toolGetTraceSteps returns the tool-loop transcript for one span. A span
// with no recorded steps (non-claude backend, or span still in flight)
// yields an empty array rather than an error — the span itself is valid,
// it just has no transcript.
func toolGetTraceSteps(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "span_id")
		if !ok {
			return mcpgo.NewToolResultError("span_id is required"), nil
		}
		return jsonResult(nilSafe(deps.Observe.ListSteps(id)))
	}
}

// toolGetGraph returns the dispatch interaction graph. Nodes are seeded from
// the configured fleet so agents with no dispatch history still show up; any
// edge endpoints not in the current config (e.g. agents removed after they
// dispatched) are added so the graph stays self-consistent.
func toolGetGraph(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		edges := deps.Observe.ListEdges()

		agents, err := store.ReadAgents(deps.DB)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get graph", err), nil
		}

		seen := make(map[string]struct{})
		for _, a := range agents {
			seen[a.Name] = struct{}{}
		}
		for _, e := range edges {
			seen[e.From] = struct{}{}
			seen[e.To] = struct{}{}
		}
		names := slices.Sorted(maps.Keys(seen))
		nodes := make([]mcpGraphNode, 0, len(names))
		for _, name := range names {
			nodes = append(nodes, mcpGraphNode{ID: name})
		}

		wireEdges := make([]mcpGraphEdge, 0, len(edges))
		for _, e := range edges {
			recs := make([]mcpGraphDispatch, 0, len(e.Dispatches))
			for _, d := range e.Dispatches {
				recs = append(recs, mcpGraphDispatch{
					At:     d.At.UTC().Format(time.RFC3339),
					Repo:   d.Repo,
					Number: d.Number,
					Reason: d.Reason,
				})
			}
			wireEdges = append(wireEdges, mcpGraphEdge{
				From:       e.From,
				To:         e.To,
				Count:      e.Count,
				Dispatches: recs,
			})
		}

		return jsonResult(map[string]any{
			"nodes": nodes,
			"edges": wireEdges,
		})
	}
}

// toolGetDispatches returns the current dispatch counters.
func toolGetDispatches(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return jsonResult(deps.Engine.DispatchStats())
	}
}

// toolGetMemory returns the markdown memory for an (agent, repo) pair.
// A defensive filepath.Clean-based traversal check rejects blatant attempts
// (".", "..") before the call reaches the memory reader, which already
// canonicalises identifiers via ai.NormalizeToken.
func toolGetMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		if isTraversalComponent(agent) || isTraversalComponent(repo) {
			return mcpgo.NewToolResultError("invalid agent or repo path"), nil
		}
		content, found, mtime, err := store.ReadMemory(deps.DB, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("read memory", err), nil
		}
		if !found {
			return mcpgo.NewToolResultErrorf("memory for %s/%s not found", agent, repo), nil
		}
		out := map[string]any{
			"agent":   agent,
			"repo":    repo,
			"content": content,
		}
		if !mtime.IsZero() {
			out["mtime"] = mtime.UTC().Format(time.RFC3339)
		}
		return jsonResult(out)
	}
}
