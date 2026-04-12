package ai

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/eloylp/agents/internal/config"
)

// PromptContext is the runtime data passed when rendering an agent's prompt.
// Fields are optional — zero values are simply omitted from the rendered
// output.
type PromptContext struct {
	Repo       string // "owner/repo"
	Number     int    // issue or PR number, 0 for runs with no GitHub item
	Backend    string // resolved backend name (claude, codex, ...)
	Memory     string // existing memory snapshot for autonomous runs
	MemoryPath string // path the agent should update after the run
}

// RenderAgentPrompt composes the final prompt text for an agent. The result
// is the concatenation of:
//
//  1. Each referenced skill's guidance (in the order listed on the agent)
//  2. The agent's own prompt
//  3. A short runtime-context block describing the repo, item number, and
//     memory path (for autonomous runs)
//
// No Go templates, no {{.Field}} substitution — just text composition. The
// agent's prompt is expected to be self-contained.
func RenderAgentPrompt(agent config.AgentDef, skills map[string]config.SkillDef, ctx PromptContext) (string, error) {
	var b strings.Builder

	for _, skillName := range agent.Skills {
		skill, ok := skills[skillName]
		if !ok {
			return "", fmt.Errorf("agent %q references unknown skill %q", agent.Name, skillName)
		}
		guidance := strings.TrimSpace(skill.Prompt)
		if guidance != "" {
			b.WriteString(guidance)
			b.WriteString("\n\n")
		}
	}

	agentPrompt := strings.TrimSpace(agent.Prompt)
	if agentPrompt != "" {
		b.WriteString(agentPrompt)
		b.WriteString("\n\n")
	}

	runtime := renderRuntimeContext(ctx)
	if runtime != "" {
		b.WriteString("## Runtime context\n\n")
		b.WriteString(runtime)
	}

	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func renderRuntimeContext(ctx PromptContext) string {
	var b strings.Builder
	if ctx.Repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", ctx.Repo)
	}
	if ctx.Number > 0 {
		fmt.Fprintf(&b, "Issue/PR number: %d\n", ctx.Number)
	}
	if ctx.Backend != "" {
		fmt.Fprintf(&b, "Backend: %s\n", ctx.Backend)
	}
	if ctx.MemoryPath != "" {
		fmt.Fprintf(&b, "Memory file: %s\n", ctx.MemoryPath)
		mem := strings.TrimSpace(ctx.Memory)
		if mem == "" {
			b.WriteString("Existing memory: (empty)\n")
		} else {
			fmt.Fprintf(&b, "Existing memory:\n%s\n", mem)
		}
	}
	return b.String()
}

// NormalizeToken canonicalises user-provided identifiers for safe use as map
// keys and filesystem fragments. Used by the memory store to derive per-agent,
// per-repo paths from free-form names.
func NormalizeToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	token = filepath.Clean(token)
	token = strings.TrimLeft(token, string(filepath.Separator)+".")
	token = strings.ReplaceAll(token, "..", "_")
	token = strings.ReplaceAll(token, string(filepath.Separator), "_")
	token = strings.ReplaceAll(token, "\\", "_")
	token = strings.ReplaceAll(token, "\x00", "_")
	return token
}
