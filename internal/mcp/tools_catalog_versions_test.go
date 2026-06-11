package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestToolPromptCatalogVersionLifecycleTracksCurrent(t *testing.T) {
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
	updateReq.Params.Arguments = map[string]any{"name": "coder", "content": "published code"}
	res, err = toolUpdatePrompt(deps)(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update prompt: %v", err)
	}
	var prompt map[string]any
	decodeText(t, res, &prompt)
	v2, _ := prompt["version_id"].(string)
	if v2 == "" || prompt["version"] != float64(2) {
		t.Fatalf("updated prompt version = (%q, %v), want v2: %+v", v2, prompt["version"], prompt)
	}

	refsReq := mcpgo.CallToolRequest{}
	refsReq.Params.Arguments = map[string]any{"name": "coder", "version_id": v1}
	res, err = toolListPromptVersionReferences(deps)(context.Background(), refsReq)
	if err != nil {
		t.Fatalf("list old prompt version refs: %v", err)
	}
	var refs []map[string]any
	decodeText(t, res, &refs)
	if len(refs) != 0 {
		t.Fatalf("old prompt refs = %+v, want none", refs)
	}

	refsReq.Params.Arguments = map[string]any{"name": "coder", "version_id": v2}
	res, err = toolListPromptVersionReferences(deps)(context.Background(), refsReq)
	if err != nil {
		t.Fatalf("list current prompt version refs: %v", err)
	}
	decodeText(t, res, &refs)
	if len(refs) != 1 || refs[0]["tracking"] != true || refs[0]["version_id"] != v2 {
		t.Fatalf("current prompt refs = %+v, want one tracking v2 ref", refs)
	}

	listReq.Params.Arguments = map[string]any{"name": "coder"}
	res, err = toolListPromptVersions(deps)(context.Background(), listReq)
	if err != nil {
		t.Fatalf("list prompt versions after update: %v", err)
	}
	decodeText(t, res, &versions)
	if len(versions) != 2 || versions[0]["id"] != v2 || versions[0]["state"] != "published" {
		t.Fatalf("prompt versions = %+v, want current published v2 first", versions)
	}
}

func TestToolSkillAndGuardrailUpdatesPublishImmediately(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	skillReq := mcpgo.CallToolRequest{}
	skillReq.Params.Arguments = map[string]any{"id": "testing", "prompt": "published tests"}
	res, err := toolUpdateSkill(deps)(context.Background(), skillReq)
	if err != nil {
		t.Fatalf("update skill: %v", err)
	}
	var skill map[string]any
	decodeText(t, res, &skill)
	skillV2, _ := skill["version_id"].(string)
	if skillV2 == "" || skill["prompt"] != "published tests" {
		t.Fatalf("updated skill = %+v, want current published skill", skill)
	}

	listSkillReq := mcpgo.CallToolRequest{}
	listSkillReq.Params.Arguments = map[string]any{"id": "testing"}
	res, err = toolListSkillVersions(deps)(context.Background(), listSkillReq)
	if err != nil {
		t.Fatalf("list skill versions: %v", err)
	}
	var skillVersions []map[string]any
	decodeText(t, res, &skillVersions)
	if len(skillVersions) == 0 || skillVersions[0]["id"] != skillV2 || skillVersions[0]["state"] != "published" {
		t.Fatalf("skill versions = %+v, want current published version first", skillVersions)
	}

	guardrailReq := mcpgo.CallToolRequest{}
	guardrailReq.Params.Arguments = map[string]any{"name": "security", "content": "published guardrail"}
	res, err = toolUpdateGuardrail(deps)(context.Background(), guardrailReq)
	if err != nil {
		t.Fatalf("update guardrail: %v", err)
	}
	var guardrail map[string]any
	decodeText(t, res, &guardrail)
	guardrailV2, _ := guardrail["version_id"].(string)
	if guardrailV2 == "" || guardrail["content"] != "published guardrail" {
		t.Fatalf("updated guardrail = %+v, want current published guardrail", guardrail)
	}

	listGuardrailReq := mcpgo.CallToolRequest{}
	listGuardrailReq.Params.Arguments = map[string]any{"name": "security"}
	res, err = toolListGuardrailVersions(deps)(context.Background(), listGuardrailReq)
	if err != nil {
		t.Fatalf("list guardrail versions: %v", err)
	}
	var guardrailVersions []map[string]any
	decodeText(t, res, &guardrailVersions)
	if len(guardrailVersions) == 0 || guardrailVersions[0]["id"] != guardrailV2 || guardrailVersions[0]["state"] != "published" {
		t.Fatalf("guardrail versions = %+v, want current published version first", guardrailVersions)
	}
}
