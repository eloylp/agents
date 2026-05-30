package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestToolPromptCatalogVersionLifecycleAndAgentPin(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	listReq := mcpgo.CallToolRequest{}
	listReq.Params.Arguments = map[string]any{"name": "coder"}
	res, err := toolListPromptVersions(deps)(context.Background(), listReq)
	if err != nil {
		t.Fatalf("list prompt versions: %v", err)
	}
	var versions []map[string]any
	decodeText(t, res, &versions)
	if len(versions) != 1 {
		t.Fatalf("initial prompt versions len = %d, want 1: %+v", len(versions), versions)
	}
	v1, _ := versions[0]["id"].(string)
	if v1 == "" {
		t.Fatalf("initial prompt version missing id: %+v", versions[0])
	}

	updateReq := mcpgo.CallToolRequest{}
	updateReq.Params.Arguments = map[string]any{"name": "coder", "content": "draft code", "publish": false}
	res, err = toolUpdatePrompt(deps)(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update prompt draft: %v", err)
	}
	var draftPrompt map[string]any
	decodeText(t, res, &draftPrompt)
	v2, _ := draftPrompt["version_id"].(string)
	if v2 == "" || draftPrompt["version"] != float64(2) {
		t.Fatalf("draft prompt version = (%q, %v), want v2: %+v", v2, draftPrompt["version"], draftPrompt)
	}

	pinReq := mcpgo.CallToolRequest{}
	pinReq.Params.Arguments = map[string]any{"name": "coder", "prompt_version_id": v1}
	res, err = toolUpdateAgent(deps)(context.Background(), pinReq)
	if err != nil {
		t.Fatalf("pin agent prompt version: %v", err)
	}
	var agent map[string]any
	decodeText(t, res, &agent)
	if got := agent["prompt_version_id"]; got != v1 {
		t.Fatalf("agent prompt_version_id = %v, want %s", got, v1)
	}

	refsReq := mcpgo.CallToolRequest{}
	refsReq.Params.Arguments = map[string]any{"name": "coder", "version_id": v1}
	res, err = toolListPromptVersionReferences(deps)(context.Background(), refsReq)
	if err != nil {
		t.Fatalf("list prompt version refs: %v", err)
	}
	var refs []map[string]any
	decodeText(t, res, &refs)
	if len(refs) != 1 || refs[0]["tracking"] != false || refs[0]["version_id"] != v1 {
		t.Fatalf("prompt refs = %+v, want one exact v1 ref", refs)
	}

	publishReq := mcpgo.CallToolRequest{}
	publishReq.Params.Arguments = map[string]any{"name": "coder", "version_id": v2}
	res, err = toolPublishPromptVersion(deps)(context.Background(), publishReq)
	if err != nil {
		t.Fatalf("publish prompt version: %v", err)
	}
	var published map[string]any
	decodeText(t, res, &published)
	if got := published["version_id"]; got != v2 {
		t.Fatalf("published prompt version_id = %v, want %s", got, v2)
	}

	rolloutReq := mcpgo.CallToolRequest{}
	rolloutReq.Params.Arguments = map[string]any{"name": "coder", "from_version_id": v1, "to_version_id": v2}
	res, err = toolRolloutPromptVersionRefs(deps)(context.Background(), rolloutReq)
	if err != nil {
		t.Fatalf("rollout prompt version refs: %v", err)
	}
	var rollout map[string]any
	decodeText(t, res, &rollout)
	if got := rollout["updated"]; got != float64(1) {
		t.Fatalf("rollout updated = %v, want 1", got)
	}
}

func TestToolSkillAndGuardrailDraftPublishParity(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	skillReq := mcpgo.CallToolRequest{}
	skillReq.Params.Arguments = map[string]any{"id": "testing", "prompt": "draft tests", "publish": false}
	res, err := toolUpdateSkill(deps)(context.Background(), skillReq)
	if err != nil {
		t.Fatalf("update skill draft: %v", err)
	}
	var skill map[string]any
	decodeText(t, res, &skill)
	skillV2, _ := skill["version_id"].(string)
	if skillV2 == "" || skill["version"] != float64(2) {
		t.Fatalf("draft skill = %+v, want v2", skill)
	}
	publishSkillReq := mcpgo.CallToolRequest{}
	publishSkillReq.Params.Arguments = map[string]any{"id": "testing", "version_id": skillV2}
	res, err = toolPublishSkillVersion(deps)(context.Background(), publishSkillReq)
	if err != nil {
		t.Fatalf("publish skill version: %v", err)
	}
	decodeText(t, res, &skill)
	if skill["version_id"] != skillV2 || skill["version"] != float64(2) {
		t.Fatalf("published skill = %+v, want version %s", skill, skillV2)
	}

	guardrailReq := mcpgo.CallToolRequest{}
	guardrailReq.Params.Arguments = map[string]any{"name": "security", "content": "draft guardrail", "publish": false}
	res, err = toolUpdateGuardrail(deps)(context.Background(), guardrailReq)
	if err != nil {
		t.Fatalf("update guardrail draft: %v", err)
	}
	var guardrail map[string]any
	decodeText(t, res, &guardrail)
	guardrailV2, _ := guardrail["version_id"].(string)
	if guardrailV2 == "" || guardrail["version"] != float64(2) {
		t.Fatalf("draft guardrail = %+v, want v2", guardrail)
	}
	publishGuardrailReq := mcpgo.CallToolRequest{}
	publishGuardrailReq.Params.Arguments = map[string]any{"name": "security", "version_id": guardrailV2}
	res, err = toolPublishGuardrailVersion(deps)(context.Background(), publishGuardrailReq)
	if err != nil {
		t.Fatalf("publish guardrail version: %v", err)
	}
	decodeText(t, res, &guardrail)
	if guardrail["version_id"] != guardrailV2 || guardrail["version"] != float64(2) {
		t.Fatalf("published guardrail = %+v, want version %s", guardrail, guardrailV2)
	}
}

func TestToolUpdateCatalogAssetsPersistVersionMetadata(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	createGuardrailReq := mcpgo.CallToolRequest{}
	createGuardrailReq.Params.Arguments = map[string]any{
		"name": "security", "description": "security checks", "content": "guardrail v1", "enabled": true, "position": 10,
	}
	if res, err := toolCreateGuardrail(deps)(context.Background(), createGuardrailReq); err != nil || res.IsError {
		t.Fatalf("create guardrail: err=%v body=%s", err, textOf(t, res))
	}

	tests := []struct {
		name       string
		update     func(Deps) server.ToolHandlerFunc
		list       func(Deps) server.ToolHandlerFunc
		updateArgs map[string]any
		listArgs   map[string]any
	}{
		{
			name:   "prompt",
			update: toolUpdatePrompt,
			list:   toolListPromptVersions,
			updateArgs: map[string]any{
				"name": "coder", "content": "prompt v2",
			},
			listArgs: map[string]any{"name": "coder"},
		},
		{
			name:   "skill",
			update: toolUpdateSkill,
			list:   toolListSkillVersions,
			updateArgs: map[string]any{
				"id": "testing", "prompt": "skill v2",
			},
			listArgs: map[string]any{"id": "testing"},
		},
		{
			name:   "guardrail",
			update: toolUpdateGuardrail,
			list:   toolListGuardrailVersions,
			updateArgs: map[string]any{
				"name": "security", "content": "guardrail v2",
			},
			listArgs: map[string]any{"name": "security"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range map[string]any{
				"publish":     false,
				"state":       "proposal",
				"source_type": "feedback_recommendation",
				"source_ref":  "rec_123",
				"author":      "assistant",
				"changelog":   "tighten guidance from review feedback",
			} {
				tt.updateArgs[k] = v
			}
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = tt.updateArgs
			res, err := tt.update(deps)(context.Background(), req)
			if err != nil || res.IsError {
				t.Fatalf("update %s: err=%v body=%s", tt.name, err, textOf(t, res))
			}

			req = mcpgo.CallToolRequest{}
			req.Params.Arguments = tt.listArgs
			res, err = tt.list(deps)(context.Background(), req)
			if err != nil || res.IsError {
				t.Fatalf("list %s versions: err=%v body=%s", tt.name, err, textOf(t, res))
			}
			var versions []map[string]any
			decodeText(t, res, &versions)
			if len(versions) == 0 {
				t.Fatalf("%s versions empty", tt.name)
			}
			got := versions[0]
			if got["state"] != "proposal" || got["source_type"] != "feedback_recommendation" || got["source_ref"] != "rec_123" ||
				got["author"] != "assistant" || got["changelog"] != "tighten guidance from review feedback" {
				t.Fatalf("%s version metadata = %+v", tt.name, got)
			}
		})
	}
}
