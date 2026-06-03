package mcp

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/selfimprovement"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
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
		workspace, _ := trimmedStringOptional(req, "workspace")
		return jsonResult(nilSafe(deps.Observe.ListEventsForWorkspace(workspace, since)))
	}
}

func toolListImprovementFeedback(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok {
			workspace = store.SelfImprovementAllWorkspaces
		}
		status, _ := trimmedStringOptional(req, "status")
		rows, err := deps.Store.ListSelfImprovementFeedback(workspace, status, 100)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list improvement feedback", err), nil
		}
		return jsonResult(nilSafe(rows))
	}
}

func toolListImprovementRecommendations(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok {
			workspace = store.SelfImprovementAllWorkspaces
		}
		status, _ := trimmedStringOptional(req, "status")
		rows, err := deps.Improvements.ListRecommendations(workspace, status, 100)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list improvement recommendations", err), nil
		}
		return jsonResult(nilSafe(rows))
	}
}

func toolGetImprovementRecommendation(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		rec, err := deps.Improvements.GetRecommendation(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get improvement recommendation", err), nil
		}
		return jsonResult(rec)
	}
}

func toolAnalyzeImprovementFeedback(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := mcpRequiredInt64(req, "feedback_event_id")
		if !ok {
			return mcpgo.NewToolResultError("feedback_event_id is required"), nil
		}
		feedback, err := deps.Store.GetSelfImprovementFeedback(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get improvement feedback", err), nil
		}
		if deps.Engine == nil {
			return mcpgo.NewToolResultError("self-improvement analyzer is not configured"), nil
		}
		rec, err := deps.Engine.AnalyzeSelfImprovementFeedback(ctx, feedback)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("analyze improvement feedback", err), nil
		}
		return jsonResult(rec)
	}
}

func toolUpdateImprovementRecommendationStatus(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		status, ok := trimmedString(req, "status")
		if !ok {
			return mcpgo.NewToolResultError("status is required"), nil
		}
		rec, err := deps.Improvements.UpdateRecommendationStatus(id, status)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update improvement recommendation status", err), nil
		}
		return jsonResult(rec)
	}
}

func toolClarifyImprovementRecommendation(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		body, ok := trimmedString(req, "body")
		if !ok {
			return mcpgo.NewToolResultError("body is required"), nil
		}
		author, _ := trimmedStringOptional(req, "author")
		if author == "" {
			author = "mcp"
		}
		rec, err := deps.Improvements.UpsertClarification(id, author, body)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("clarify improvement recommendation", err), nil
		}
		if deps.Channels == nil {
			return mcpgo.NewToolResultError("self-improvement queue is not configured"), nil
		}
		if _, err := deps.Channels.PushEvent(ctx, mcpClarificationImprovementEvent(rec)); err != nil {
			return mcpgo.NewToolResultErrorFromErr("enqueue improvement analysis", err), nil
		}
		return jsonResult(rec)
	}
}

func mcpClarificationImprovementEvent(rec selfimprovement.SelfImprovementRecommendation) workflow.Event {
	feedback := rec.Feedback
	repo := ""
	number := 0
	actor := "mcp"
	if feedback != nil {
		repo = strings.Trim(feedback.RepoOwner+"/"+feedback.RepoName, "/")
		number = mcpFirstNonZero(feedback.PRNumber, feedback.IssueNumber)
		actor = feedback.AuthorLogin
	}
	return workflow.Event{
		ID:          workflow.GenEventID(),
		WorkspaceID: fleet.NormalizeWorkspaceID(rec.WorkspaceID),
		Repo:        workflow.RepoRef{FullName: repo, Enabled: true},
		Kind:        "agents.improvement",
		Number:      number,
		Actor:       actor,
		Payload: map[string]any{
			"feedback_event_id": rec.FeedbackEventID,
			"recommendation_id": rec.ID,
			"clarification":     true,
		},
	}
}

func mcpFirstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func toolCreateImprovementProposal(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "recommendation_id")
		if !ok {
			return mcpgo.NewToolResultError("recommendation_id is required"), nil
		}
		proposal, err := deps.Improvements.CreateProposal(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create improvement proposal", err), nil
		}
		return jsonResult(proposal)
	}
}

func toolGetImprovementProposal(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "recommendation_id")
		if !ok {
			return mcpgo.NewToolResultError("recommendation_id is required"), nil
		}
		proposals, err := deps.Improvements.ListProposals(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get improvement proposal", err), nil
		}
		return jsonResult(nilSafe(proposals))
	}
}

func toolListImprovementRecommendationsWithProposals(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok {
			workspace = store.SelfImprovementAllWorkspaces
		}
		rows, err := deps.Improvements.ListRecommendationsWithProposals(workspace, 100)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list improvement recommendations with proposals", err), nil
		}
		return jsonResult(nilSafe(rows))
	}
}

func toolCreateImprovementProposalBundle(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "recommendation_id")
		if !ok {
			return mcpgo.NewToolResultError("recommendation_id is required"), nil
		}
		bundle, err := deps.Improvements.CreateProposalBundle(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create improvement proposal bundle", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolGetImprovementProposalBundle(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "recommendation_id")
		if !ok {
			id, ok = trimmedString(req, "bundle_id")
		}
		if !ok {
			return mcpgo.NewToolResultError("recommendation_id or bundle_id is required"), nil
		}
		bundle, err := deps.Improvements.GetProposalBundle(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get improvement proposal bundle", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolEditImprovementProposalBundleItem(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		bundleID, ok := trimmedString(req, "bundle_id")
		if !ok {
			return mcpgo.NewToolResultError("bundle_id is required"), nil
		}
		itemID, ok := trimmedString(req, "item_id")
		if !ok {
			return mcpgo.NewToolResultError("item_id is required"), nil
		}
		body, ok := trimmedString(req, "proposed_body")
		if !ok {
			return mcpgo.NewToolResultError("proposed_body is required"), nil
		}
		args := req.GetArguments()
		update := selfimprovement.SelfImprovementBundleItemUpdate{ProposedBody: body}
		if v, ok := stringPtrArg(args, "proposed_ref"); ok {
			next := strings.TrimSpace(*v)
			update.ProposedRef = &next
		}
		if v, ok := stringPtrArg(args, "proposed_name"); ok {
			next := strings.TrimSpace(*v)
			update.ProposedName = &next
		}
		if v, ok := stringPtrArg(args, "proposed_scope"); ok {
			next := strings.TrimSpace(*v)
			update.ProposedScope = &next
		}
		if v, ok := stringPtrArg(args, "proposed_description"); ok {
			next := strings.TrimSpace(*v)
			update.ProposedDescription = &next
		}
		if v, _, errMsg := boolPtrArg(args, "proposed_enabled"); errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		} else {
			update.ProposedEnabled = v
		}
		if v, _, errMsg := intPtrArg(args, "proposed_position"); errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		} else {
			update.ProposedPosition = v
		}
		bundle, err := deps.Improvements.UpdateProposalBundleItem(bundleID, itemID, update, "mcp")
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("edit improvement proposal bundle item", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolRejectImprovementProposalBundleItem(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		bundleID, ok := trimmedString(req, "bundle_id")
		if !ok {
			return mcpgo.NewToolResultError("bundle_id is required"), nil
		}
		itemID, ok := trimmedString(req, "item_id")
		if !ok {
			return mcpgo.NewToolResultError("item_id is required"), nil
		}
		reason, _ := trimmedStringOptional(req, "reason")
		bundle, err := deps.Improvements.RejectProposalBundleItem(bundleID, itemID, reason, "mcp")
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("reject improvement proposal bundle item", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolLinkImprovementProposalBundleItem(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		bundleID, ok := trimmedString(req, "bundle_id")
		if !ok {
			return mcpgo.NewToolResultError("bundle_id is required"), nil
		}
		itemID, ok := trimmedString(req, "item_id")
		if !ok {
			return mcpgo.NewToolResultError("item_id is required"), nil
		}
		assetID, ok := trimmedString(req, "asset_id")
		if !ok {
			return mcpgo.NewToolResultError("asset_id is required"), nil
		}
		reason, _ := trimmedStringOptional(req, "reason")
		bundle, err := deps.Improvements.LinkProposalBundleItem(bundleID, itemID, assetID, reason, "mcp")
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("link improvement proposal bundle item", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolPublishImprovementProposalBundle(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "bundle_id")
		if !ok {
			return mcpgo.NewToolResultError("bundle_id is required"), nil
		}
		bundle, err := deps.Improvements.PublishProposalBundle(id, "mcp")
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish improvement proposal bundle", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolDiscardImprovementProposalBundle(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "bundle_id")
		if !ok {
			return mcpgo.NewToolResultError("bundle_id is required"), nil
		}
		bundle, err := deps.Improvements.DiscardProposalBundle(id, "mcp")
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("discard improvement proposal bundle", err), nil
		}
		return jsonResult(bundle)
	}
}

func toolListImprovementRecommendationsWithBundles(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok {
			workspace = store.SelfImprovementAllWorkspaces
		}
		rows, err := deps.Improvements.ListRecommendationsWithBundles(workspace, 100)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list improvement recommendations with bundles", err), nil
		}
		return jsonResult(nilSafe(rows))
	}
}

func toolListImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		status, _ := trimmedStringOptional(req, "status")
		rows, err := deps.Improvements.ListMemory("", status, 200)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list improvement memory", err), nil
		}
		return jsonResult(nilSafe(rows))
	}
}

func toolCreateImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		key, ok := trimmedString(req, "key")
		if !ok {
			return mcpgo.NewToolResultError("key is required"), nil
		}
		value, ok := trimmedString(req, "value")
		if !ok {
			return mcpgo.NewToolResultError("value is required"), nil
		}
		status, _ := trimmedStringOptional(req, "status")
		evidenceType, _ := trimmedStringOptional(req, "evidence_type")
		evidenceID, _ := trimmedStringOptional(req, "evidence_id")
		evidenceURL, _ := trimmedStringOptional(req, "evidence_url")
		confidence, _ := trimmedStringOptional(req, "confidence")
		row, err := deps.Improvements.CreateMemory(selfimprovement.AssistantMemoryInput{
			Key:          key,
			Value:        value,
			Status:       status,
			EvidenceType: evidenceType,
			EvidenceID:   evidenceID,
			EvidenceURL:  evidenceURL,
			Confidence:   confidence,
			ProposedBy:   "mcp",
		})
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create improvement memory", err), nil
		}
		return jsonResult(row)
	}
}

func toolUpdateImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		args := req.GetArguments()
		update := selfimprovement.AssistantMemoryUpdate{}
		if v, ok := stringPtrArg(args, "key"); ok {
			next := strings.TrimSpace(*v)
			update.Key = &next
		}
		if v, ok := stringPtrArg(args, "value"); ok {
			next := strings.TrimSpace(*v)
			update.Value = &next
		}
		if v, ok := stringPtrArg(args, "confidence"); ok {
			next := strings.TrimSpace(*v)
			update.Confidence = &next
		}
		row, err := deps.Improvements.UpdateMemory(id, update)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update improvement memory", err), nil
		}
		return jsonResult(row)
	}
}

func toolApproveImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		row, err := deps.Improvements.ApproveMemory(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("approve improvement memory", err), nil
		}
		return jsonResult(row)
	}
}

func toolRejectImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		reason, _ := trimmedStringOptional(req, "reason")
		row, err := deps.Improvements.RejectMemory(id, reason)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("reject improvement memory", err), nil
		}
		return jsonResult(row)
	}
}

func toolArchiveImprovementMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		row, err := deps.Improvements.ArchiveMemory(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("archive improvement memory", err), nil
		}
		return jsonResult(row)
	}
}

// toolListTraces returns the 200 most recent spans verbatim. The Span JSON
// shape already matches GET /traces so clients can parse both surfaces with
// the same decoder.
func toolListTraces(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, _ := trimmedStringOptional(req, "workspace")
		return jsonResult(nilSafe(deps.Observe.ListTracesForWorkspace(workspace)))
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
		workspace, _ := trimmedStringOptional(req, "workspace")
		spans := deps.Observe.TracesByRootEventIDForWorkspace(workspace, id)
		if len(spans) == 0 {
			return mcpgo.NewToolResultErrorf("trace %q not found", id), nil
		}
		return jsonResult(spans)
	}
}

// toolGetTraceSteps returns the tool-loop transcript for one span. A span
// with no recorded steps (non-claude backend, or span still in flight)
// yields an empty array rather than an error, the span itself is valid,
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

// toolGetTracePrompt returns the composed prompt the daemon sent to the
// AI CLI for one span, the operator's "what did the agent see" debug
// artefact. Stored gzipped on the trace row; the store decompresses on
// the fly. Mirrors GET /traces/{span_id}/prompt; returns a tool error
// when no prompt is recorded (pre-009-migration spans, or runs the
// engine never reached).
func toolGetTracePrompt(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "span_id")
		if !ok {
			return mcpgo.NewToolResultError("span_id is required"), nil
		}
		prompt, err := deps.Observe.PromptForSpan(id)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get trace prompt", err), nil
		}
		if prompt == "" {
			return mcpgo.NewToolResultErrorf("no prompt recorded for span %q", id), nil
		}
		return mcpgo.NewToolResultText(prompt), nil
	}
}

// toolGetGraph returns the dispatch interaction graph. Nodes are seeded from
// the configured fleet so agents with no dispatch history still show up; any
// edge endpoints not in the current config (e.g. agents removed after they
// dispatched) are added so the graph stays self-consistent.
func toolGetGraph(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, _ := trimmedStringOptional(req, "workspace")
		workspaceID := fleet.NormalizeWorkspaceID(workspace)
		edges := deps.Observe.ListEdgesForWorkspace(workspaceID)

		agents, err := deps.Store.ReadAgents()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get graph", err), nil
		}

		seen := make(map[string]struct{})
		agentNames := make(map[string]struct{})
		configuredEdges := make(map[string]mcpGraphEdge)
		for _, a := range agents {
			if fleet.NormalizeWorkspaceID(a.WorkspaceID) != workspaceID {
				continue
			}
			seen[a.Name] = struct{}{}
			agentNames[a.Name] = struct{}{}
		}
		for _, a := range agents {
			if fleet.NormalizeWorkspaceID(a.WorkspaceID) != workspaceID {
				continue
			}
			for _, target := range a.CanDispatch {
				if _, ok := agentNames[target]; !ok {
					continue
				}
				configuredEdges[mcpGraphEdgeKey(a.Name, target)] = mcpGraphEdge{
					From:       a.Name,
					To:         target,
					Dispatches: []mcpGraphDispatch{},
				}
			}
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

		edgeByKey := maps.Clone(configuredEdges)
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
			edgeByKey[mcpGraphEdgeKey(e.From, e.To)] = mcpGraphEdge{
				From:       e.From,
				To:         e.To,
				Count:      e.Count,
				Dispatches: recs,
			}
		}
		wireEdges := make([]mcpGraphEdge, 0, len(edgeByKey))
		for _, e := range edgeByKey {
			if e.Dispatches == nil {
				e.Dispatches = []mcpGraphDispatch{}
			}
			wireEdges = append(wireEdges, e)
		}
		slices.SortFunc(wireEdges, func(a, b mcpGraphEdge) int {
			return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
		})

		return jsonResult(map[string]any{
			"nodes": nodes,
			"edges": wireEdges,
		})
	}
}

func mcpGraphEdgeKey(from, to string) string {
	return from + "\x00" + to
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
		workspace, _ := trimmedString(req, "workspace")
		content, found, mtime, err := deps.Store.ReadWorkspaceMemoryRaw(workspace, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("read memory", err), nil
		}
		if !found {
			return mcpgo.NewToolResultErrorf("memory for %s/%s not found", agent, repo), nil
		}
		out := map[string]any{
			"workspace": fleet.NormalizeWorkspaceID(workspace),
			"agent":     agent,
			"repo":      repo,
			"content":   content,
		}
		if !mtime.IsZero() {
			out["mtime"] = mtime.UTC().Format(time.RFC3339)
		}
		return jsonResult(out)
	}
}
