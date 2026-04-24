package mcp

import (
	"context"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/config"
)

// toolCreateAgent upserts an agent definition through the same path as POST
// /agents. Returns the canonical (normalized) form so callers see the agent
// the way the store actually persisted it. Empty names, unknown backends, and
// model/skill validation failures surface as tool errors via the store's
// *ErrValidation / *ErrConflict types.
func toolCreateAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		a := config.AgentDef{
			Name:          name,
			Backend:       req.GetString("backend", ""),
			Model:         req.GetString("model", ""),
			Prompt:        req.GetString("prompt", ""),
			Description:   req.GetString("description", ""),
			Skills:        req.GetStringSlice("skills", nil),
			CanDispatch:   req.GetStringSlice("can_dispatch", nil),
			AllowPRs:      req.GetBool("allow_prs", false),
			AllowDispatch: req.GetBool("allow_dispatch", false),
		}
		canonical, err := deps.AgentWrite.UpsertAgent(a)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create agent", err), nil
		}
		return jsonResult(agentJSON(canonical))
	}
}

// toolDeleteAgent removes an agent through the same path as DELETE
// /agents/{name}. cascade=true also drops repo bindings that reference the
// agent; without it, a referenced agent surfaces a *store.ErrConflict so
// callers can prompt for cascade explicitly rather than silently mutating
// repo bindings.
func toolDeleteAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cascade := req.GetBool("cascade", false)
		if err := deps.AgentWrite.DeleteAgent(config.NormalizeAgentName(name), cascade); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete agent", err), nil
		}
		return jsonResult(map[string]any{
			"status":  "deleted",
			"name":    config.NormalizeAgentName(name),
			"cascade": cascade,
		})
	}
}

// toolCreateSkill upserts a skill through the same path as POST /skills.
// Returns the canonical (normalized) form so callers see the skill the way the
// store actually persisted it. Empty names surface as *store.ErrValidation via
// Server.UpsertSkill, which storeErrStatus maps to a user-actionable error.
func toolCreateSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		sk := config.SkillDef{Prompt: req.GetString("prompt", "")}
		canonicalName, canonical, err := deps.SkillWrite.UpsertSkill(name, sk)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create skill", err), nil
		}
		return jsonResult(map[string]any{
			"name":   canonicalName,
			"prompt": canonical.Prompt,
		})
	}
}

// toolDeleteSkill removes a skill through the same path as DELETE
// /skills/{name}. If any agent still references the skill the store surfaces a
// *store.ErrConflict, which the caller sees as a user-actionable error.
func toolDeleteSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeSkillName(name)
		if err := deps.SkillWrite.DeleteSkill(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete skill", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// toolCreateBackend upserts a backend definition through the same path as
// POST /backends. Returns the canonical (normalized) form so callers see the
// backend the way the store actually persisted it — lowercased name, trimmed
// command, defaults applied for zero-value timeout/max-prompt fields. Empty
// names surface as *store.ErrValidation via Server.UpsertBackend, which
// storeErrStatus maps to a user-actionable error.
func toolCreateBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		b := config.AIBackendConfig{
			Command:          req.GetString("command", ""),
			Models:           req.GetStringSlice("models", nil),
			LocalModelURL:    req.GetString("local_model_url", ""),
			TimeoutSeconds:   req.GetInt("timeout_seconds", 0),
			MaxPromptChars:   req.GetInt("max_prompt_chars", 0),
			RedactionSaltEnv: req.GetString("redaction_salt_env", ""),
		}
		canonicalName, canonical, err := deps.BackendWrite.UpsertBackend(name, b)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create backend", err), nil
		}
		return jsonResult(backendJSON(canonicalName, canonical))
	}
}

// toolDeleteBackend removes a backend through the same path as DELETE
// /backends/{name}. If any agent still references the backend the store
// surfaces a *store.ErrConflict, which the caller sees as a user-actionable
// error.
func toolDeleteBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeBackendName(name)
		if err := deps.BackendWrite.DeleteBackend(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete backend", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// toolCreateRepo upserts a repo definition (and its bindings) through the
// same path as POST /repos. Returns the canonical (normalized) form so
// callers see the repo the way the store actually persisted it — lowercased
// owner/name, lowercased binding agents, trimmed cron, lowercased events.
// Empty names surface as *store.ErrValidation via Server.UpsertRepo, which
// storeErrStatus maps to a user-actionable error. Binding validation errors
// from the store (unknown agent, bad cron, trigger ambiguity) propagate as
// tool errors.
func toolCreateRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		bindings, bErr := parseBindings(req.GetArguments()["bindings"])
		if bErr != "" {
			return mcpgo.NewToolResultError(bErr), nil
		}
		r := config.RepoDef{
			Name:    name,
			Enabled: req.GetBool("enabled", false),
			Use:     bindings,
		}
		canonical, err := deps.RepoWrite.UpsertRepo(r)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create repo", err), nil
		}
		return jsonResult(repoJSON(canonical))
	}
}

// toolDeleteRepo removes a repo (and cascades its bindings) through the same
// path as DELETE /repos/{owner}/{repo}. The underlying store delete is
// idempotent for unknown names; a *store.ErrConflict surfaces if deleting
// would leave the fleet with zero enabled repos, which the caller sees as a
// user-actionable error.
func toolDeleteRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeRepoName(name)
		if err := deps.RepoWrite.DeleteRepo(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete repo", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// toolCreateBinding inserts a new binding row for the named repo through the
// same path as POST /repos/{owner}/{repo}/bindings. Returns the persisted
// binding with its generated ID. Trigger validation and agent-reference
// checks happen in the store layer, surfacing as *ErrValidation (user error)
// or *ErrNotFound (user error).
func toolCreateBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		b := config.Binding{
			Agent:  agent,
			Labels: req.GetStringSlice("labels", nil),
			Events: req.GetStringSlice("events", nil),
			Cron:   req.GetString("cron", ""),
		}
		if v, ok := req.GetArguments()["enabled"]; ok && v != nil {
			enabled, ok := v.(bool)
			if !ok {
				return mcpgo.NewToolResultError("enabled must be a boolean"), nil
			}
			b.Enabled = &enabled
		}
		persisted, err := deps.BindingWrite.CreateBinding(repo, b)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create binding", err), nil
		}
		return jsonResult(bindingJSON(persisted))
	}
}

// toolUpdateBinding replaces all fields of an existing binding by ID through
// the same path as PATCH /repos/{owner}/{repo}/bindings/{id}. The repo path
// parameter is cross-checked against the stored binding's repo — mismatches
// surface as not-found.
func toolUpdateBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := req.RequireInt("id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		if id <= 0 {
			return mcpgo.NewToolResultError("id must be a positive integer"), nil
		}
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		b := config.Binding{
			Agent:  agent,
			Labels: req.GetStringSlice("labels", nil),
			Events: req.GetStringSlice("events", nil),
			Cron:   req.GetString("cron", ""),
		}
		if v, ok := req.GetArguments()["enabled"]; ok && v != nil {
			enabled, ok := v.(bool)
			if !ok {
				return mcpgo.NewToolResultError("enabled must be a boolean"), nil
			}
			b.Enabled = &enabled
		}
		updated, uerr := deps.BindingWrite.UpdateBinding(repo, int64(id), b)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update binding", uerr), nil
		}
		return jsonResult(bindingJSON(updated))
	}
}

// toolDeleteBinding removes a binding by ID through the same path as DELETE
// /repos/{owner}/{repo}/bindings/{id}.
func toolDeleteBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, err := req.RequireInt("id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		if id <= 0 {
			return mcpgo.NewToolResultError("id must be a positive integer"), nil
		}
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		if err := deps.BindingWrite.DeleteBinding(repo, int64(id)); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete binding", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"id":     id,
			"repo":   config.NormalizeRepoName(repo),
		})
	}
}

// repoJSON renders a RepoDef in the same wire shape as an element of the
// list_repos / get_repo responses, so create_repo/delete_repo callers consume
// one schema regardless of whether they are reading or writing.
func repoJSON(r config.RepoDef) map[string]any {
	bindings := make([]map[string]any, 0, len(r.Use))
	for _, b := range r.Use {
		bindings = append(bindings, bindingJSON(b))
	}
	return map[string]any{
		"name":     r.Name,
		"enabled":  r.Enabled,
		"bindings": bindings,
	}
}

// parseBindings decodes the create_repo "bindings" argument into a slice of
// config.Binding. The MCP-go request helpers expose string/bool/number
// primitives directly but not nested objects, so we read the raw argument and
// destructure it here. A nil/missing value yields an empty binding list.
//
// Type mismatches are rejected with explicit user errors instead of being
// silently dropped — REST decodes this payload through json.Unmarshal into
// storeBindingJSON, which refuses wrong JSON types. Matching that strictness
// here is what keeps a payload like `{"enabled":"false"}` from being treated
// as omitted (and therefore default-enabled), which would silently flip the
// caller's intended disablement.
//
// Binding.Enabled stays nil when the caller omits the key (the "default
// enabled" case config.Binding.IsEnabled relies on). A literal false/true sets
// the pointer so downstream validation sees the user's intent preserved.
func parseBindings(v any) ([]config.Binding, string) {
	if v == nil {
		return nil, ""
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, "bindings must be an array"
	}
	out := make([]config.Binding, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("bindings[%d]: must be an object", i)
		}
		var b config.Binding
		if v, ok := m["agent"]; ok && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].agent must be a string", i)
			}
			b.Agent = s
		}
		if v, ok := m["cron"]; ok && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].cron must be a string", i)
			}
			b.Cron = s
		}
		labels, lErr := stringSliceFromAny(m["labels"], fmt.Sprintf("bindings[%d].labels", i))
		if lErr != "" {
			return nil, lErr
		}
		b.Labels = labels
		events, eErr := stringSliceFromAny(m["events"], fmt.Sprintf("bindings[%d].events", i))
		if eErr != "" {
			return nil, eErr
		}
		b.Events = events
		if v, ok := m["enabled"]; ok && v != nil {
			enabled, ok := v.(bool)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].enabled must be a boolean", i)
			}
			b.Enabled = &enabled
		}
		out = append(out, b)
	}
	return out, ""
}

// stringSliceFromAny decodes a JSON array of strings for the given field
// path. A nil/missing value yields nil. A non-array argument or a non-string
// element is reported via the returned error string using the supplied path
// prefix so callers get `bindings[i].labels[j] must be a string` rather than
// a silent drop.
func stringSliceFromAny(v any, path string) ([]string, string) {
	if v == nil {
		return nil, ""
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Sprintf("%s must be an array", path)
	}
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Sprintf("%s[%d] must be a string", path, i)
		}
		out = append(out, s)
	}
	return out, ""
}
