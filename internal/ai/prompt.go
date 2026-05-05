package ai

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
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
// Fields are optional, zero values are simply omitted from the rendered
// output.
type PromptContext struct {
	Repo        string // "owner/repo"
	Number      int    // issue or PR number, 0 for runs with no GitHub item
	Backend     string // resolved backend name (claude, codex, ...)
	Memory      string // existing memory snapshot injected before each autonomous run
	HasMemory   bool   // true when the caller is loading memory for this run; enables the memory section
	EventKind   string         // e.g. "issues.labeled", "push", empty for autonomous runs
	Actor       string         // GitHub login that triggered the event; empty for autonomous runs
	Payload     map[string]any // kind-specific event fields; nil for autonomous runs

	// Roster is the list of peer agents available for dispatch on the same repo.
	// The current agent is excluded. Only populated when dispatch is configured.
	Roster []RosterEntry

	// Dispatch context, populated when this agent was invoked via agent.dispatch.
	InvokedBy     string // name of the agent that dispatched this run
	Reason        string // reason provided by the dispatching agent
	RootEventID   string // ID of the root (non-dispatch) event that started the chain
	DispatchDepth int    // 0 for direct triggers; increments with each dispatch hop
}

// RenderAgentPrompt composes the prompt for an agent and returns it as a
// RenderedPrompt with two parts:
//
//   - System: stable content that is identical across every run of the same
//     agent on the same repo, operator-set guardrails (prepended at the very
//     top so they precede everything else the model reads), the no-PR guard
//     (when allow_prs=false), concatenated skill guidance, the agent's own
//     prompt body, and the available-experts roster. Backends that support a
//     native system channel (e.g. Claude's --append-system-prompt) can deliver
//     this part separately to benefit from prompt caching.
//
//   - User: per-run content, the ## Runtime context block containing the
//     repo, event, actor, payload, and memory. This changes every run and must
//     travel as the user turn.
//
// Guardrails are passed in render order (caller selects only enabled rows
// and orders them by position ASC, name ASC); each entry's Content is
// emitted verbatim, separated by a blank line. The renderer does not
// inspect the Enabled or Position fields itself, those are the caller's
// gate, so the renderer stays a pure text composer.
//
// No Go templates, no {{.Field}} substitution, just text composition. The
// agent's prompt is expected to be self-contained.
func RenderAgentPrompt(agent fleet.Agent, skills map[string]fleet.Skill, guardrails []fleet.Guardrail, ctx PromptContext) (RenderedPrompt, error) {
	var sys strings.Builder

	for _, g := range guardrails {
		content := strings.TrimSpace(g.Content)
		if content == "" {
			continue
		}
		sys.WriteString(content)
		sys.WriteString("\n\n")
	}

	if !agent.AllowPRs {
		sys.WriteString("Do not open or create pull requests under any circumstances.\n")
	}

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

	if roster := renderRoster(ctx.Roster); roster != "" {
		if sys.Len() > 0 {
			sys.WriteString("\n\n")
		}
		sys.WriteString(roster)
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

func renderRoster(roster []RosterEntry) string {
	if len(roster) == 0 {
		return ""
	}
	sorted := slices.Clone(roster)
	slices.SortFunc(sorted, func(a, b RosterEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	var b strings.Builder
	b.WriteString("## Available experts\n\n")
	for _, r := range sorted {
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
	return b.String()
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
				for line := range strings.SplitSeq(s, "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			} else {
				fmt.Fprintf(&b, "%s: %v\n", k, v)
			}
		}
	}
	if ctx.HasMemory {
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
