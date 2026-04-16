package ai

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
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
	EventKind  string         // e.g. "issues.labeled", "push" — empty for autonomous runs
	Actor      string         // GitHub login that triggered the event; empty for autonomous runs
	Payload    map[string]any // kind-specific event fields; nil for autonomous runs
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
	if ctx.EventKind != "" {
		fmt.Fprintf(&b, "Event: %s\n", ctx.EventKind)
	}
	if ctx.Actor != "" {
		fmt.Fprintf(&b, "Actor: %s\n", ctx.Actor)
	}
	if len(ctx.Payload) > 0 {
		// Sort keys for deterministic output.
		for _, k := range slices.Sorted(maps.Keys(ctx.Payload)) {
			v := ctx.Payload[k]
			if s, ok := v.(string); ok && strings.Contains(s, "\n") {
				fmt.Fprintf(&b, "%s:\n", k)
				for _, line := range strings.Split(s, "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			} else {
				fmt.Fprintf(&b, "%s: %v\n", k, v)
			}
		}
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
