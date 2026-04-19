package ai

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
)

// RosterEntry describes a peer agent visible to the current agent via the
// ## Available experts section of the rendered prompt.
type RosterEntry struct {
	Name          string
	Description   string
	Skills        []string
	AllowDispatch bool
}

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

	// Roster is the list of peer agents available for dispatch on the same repo.
	// The current agent is excluded. Only populated when dispatch is configured.
	Roster []RosterEntry

	// Dispatch context — populated when this agent was invoked via agent.dispatch.
	InvokedBy     string // name of the agent that dispatched this run
	Reason        string // reason provided by the dispatching agent
	RootEventID   string // ID of the root (non-dispatch) event that started the chain
	DispatchDepth int    // 0 for direct triggers; increments with each dispatch hop
}

// RenderAgentPrompt composes the prompt for an agent and returns it as a
// RenderedPrompt with two parts:
//
//   - System: stable content that is identical across every run of the same
//     agent — concatenated skill guidance followed by the agent's own prompt
//     body. Backends that support a native system channel (e.g. Claude's
//     --append-system-prompt) can deliver this part separately to benefit from
//     prompt caching.
//
//   - User: per-run content — the ## Runtime context block containing the
//     repo, event, actor, payload, memory, and roster. This changes every run
//     and must travel as the user turn.
//
// No Go templates, no {{.Field}} substitution — just text composition. The
// agent's prompt is expected to be self-contained.
func RenderAgentPrompt(agent config.AgentDef, skills map[string]config.SkillDef, ctx PromptContext) (RenderedPrompt, error) {
	var sys strings.Builder

	for _, skillName := range agent.Skills {
		skill, ok := skills[skillName]
		if !ok {
			return RenderedPrompt{}, fmt.Errorf("agent %q references unknown skill %q", agent.Name, skillName)
		}
		guidance := strings.TrimSpace(skill.Prompt)
		if guidance != "" {
			sys.WriteString(guidance)
			sys.WriteString("\n\n")
		}
	}

	agentPrompt := strings.TrimSpace(agent.Prompt)
	if agentPrompt != "" {
		sys.WriteString(agentPrompt)
	}

	var usr strings.Builder
	runtime := renderRuntimeContext(ctx)
	if runtime != "" {
		usr.WriteString("## Runtime context\n\n")
		usr.WriteString(runtime)
	}

	return RenderedPrompt{
		System: strings.TrimRight(sys.String(), "\n"),
		User:   strings.TrimRight(usr.String(), "\n"),
	}, nil
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
	if ctx.InvokedBy != "" {
		fmt.Fprintf(&b, "Invoked by: %s\n", ctx.InvokedBy)
		fmt.Fprintf(&b, "Dispatch reason: %s\n", ctx.Reason)
		if ctx.RootEventID != "" {
			fmt.Fprintf(&b, "Root event ID: %s\n", ctx.RootEventID)
		}
		fmt.Fprintf(&b, "Dispatch depth: %d\n", ctx.DispatchDepth)
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
	if len(ctx.Roster) > 0 {
		b.WriteString("\n## Available experts\n\n")
		// Sort roster by name for deterministic output.
		roster := make([]RosterEntry, len(ctx.Roster))
		copy(roster, ctx.Roster)
		slices.SortFunc(roster, func(a, b RosterEntry) int {
			return strings.Compare(a.Name, b.Name)
		})
		for _, r := range roster {
			fmt.Fprintf(&b, "- **%s**", r.Name)
			if r.Description != "" {
				fmt.Fprintf(&b, ": %s", r.Description)
			}
			if len(r.Skills) > 0 {
				fmt.Fprintf(&b, " (skills: %s)", strings.Join(r.Skills, ", "))
			}
			if r.AllowDispatch {
				b.WriteString(" [dispatchable]")
			}
			b.WriteString("\n")
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
