package ai

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Runner interface {
	Run(ctx context.Context, req Request) (Response, error)
}

type CommandInterruptionKind string

const (
	CommandInterruptedTimeout  CommandInterruptionKind = "timeout"
	CommandInterruptedCanceled CommandInterruptionKind = "canceled"
)

// CommandInterruptedError marks backend command cancellation/timeout as a hard
// run failure even when the CLI emitted a parseable partial response first.
type CommandInterruptedError struct {
	Backend string
	Kind    CommandInterruptionKind
	Timeout time.Duration
	Err     error
}

func (e CommandInterruptedError) Error() string {
	switch e.Kind {
	case CommandInterruptedTimeout:
		if e.Timeout > 0 {
			return fmt.Sprintf("%s command timed out after %s", e.Backend, e.Timeout)
		}
		return fmt.Sprintf("%s command timed out", e.Backend)
	case CommandInterruptedCanceled:
		return fmt.Sprintf("%s command canceled", e.Backend)
	default:
		return fmt.Sprintf("%s command interrupted", e.Backend)
	}
}

func (e CommandInterruptedError) Unwrap() error {
	return e.Err
}

// RenderedPrompt is the result of RenderAgentPrompt: stable system-level
// content (skills + agent body) separated from per-run user content (runtime
// context). Keeping them split lets backends that support a native system
// channel (e.g. Claude's --append-system-prompt) benefit from prompt caching
// without any behavioural change on backends that do not (codex: concatenated).
type RenderedPrompt struct {
	System string // stable across runs: skills + agent prompt body
	User   string // per-run: runtime context, memory, event payload
}

type Request struct {
	Workflow string
	Repo     string
	Number   int
	Model    string // optional per-agent model override
	System   string // stable system-level content (from RenderedPrompt.System)
	User     string // per-run user content (from RenderedPrompt.User)

	// OnLine, when non-nil, is invoked synchronously for every line the
	// AI CLI writes to stdout, with the trailing newline stripped. Used
	// by the engine to publish lines into observe.RunRegistry's per-span
	// stream hub for live UI streaming. Must not block, the runner
	// reads stdout in a tight loop and the callback runs on that
	// goroutine.
	OnLine func(line []byte)
}

type Artifact struct {
	Type     string  `json:"type"`
	PartKey  string  `json:"part_key"`
	GitHubID string  `json:"github_id"`
	URL      *string `json:"url"`
}

// DispatchRequest is a request from an agent to dispatch another agent on the
// same repo. The daemon validates these requests against whitelist and safety
// limits before enqueuing a synthetic "agent.dispatch" event.
type DispatchRequest struct {
	Agent  string `json:"agent"`
	Number int    `json:"number,omitempty"`
	Reason string `json:"reason"`
}

// TraceStep records one event in the agent's tool loop. Two kinds are
// emitted: "tool" (paired tool_use + tool_result) and "thinking" (a
// text block the assistant produced between tool calls). Populated by
// the runner from stream-json output and never part of the agent's
// JSON schema, agents do not return steps themselves.
type TraceStep struct {
	Kind          string `json:"kind"`
	ToolName      string `json:"tool_name,omitempty"`
	InputSummary  string `json:"input_summary,omitempty"`
	OutputSummary string `json:"output_summary,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
}

// Step kind constants.
const (
	StepKindTool     = "tool"
	StepKindThinking = "thinking"
)

type Response struct {
	Artifacts []Artifact        `json:"artifacts"`
	Summary   string            `json:"summary"`
	Dispatch  []DispatchRequest `json:"dispatch,omitempty"`
	// Memory is the agent's full updated memory state returned at the end of
	// each run. The daemon writes this value back to the memory store (SQLite or
	// filesystem) after the run completes. An empty string clears the memory.
	Memory string `json:"memory"`
	// Steps holds the tool-loop transcript extracted from stream-json CLI
	// output. It is populated by the runner and not part of the agent schema.
	Steps []TraceStep `json:"-"`
	// Usage holds the token counts the AI CLI reported for this run.
	// Populated by the runner from the CLI's streaming output (Anthropic's
	// `result` event for Claude Code, OpenAI's `usage` event for Codex).
	// Not part of the agent schema. Cache fields are zero on backends that
	// do not report them (e.g. Codex emits only input/output).
	Usage Usage `json:"-"`
}

type FailureKind string

const (
	FailureKindBackendAuth  FailureKind = "backend_auth"
	FailureKindBackendError FailureKind = "backend_error"
	FailureKindRunnerError  FailureKind = "runner_error"
	FailureKindTimeout      FailureKind = "timeout"
	FailureKindCanceled     FailureKind = "canceled"
	FailureKindParseError   FailureKind = "parse_error"
	FailureKindUnknown      FailureKind = "unknown"
)

// RunFailureError carries sanitized, operator-facing failure metadata alongside
// the strict runner error. The daemon persists these fields on traces/runners.
type RunFailureError struct {
	Backend string
	Kind    FailureKind
	Detail  string
	Err     error
}

func (e RunFailureError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Detail != "" {
		return e.Detail
	}
	return string(e.Kind)
}

func (e RunFailureError) Unwrap() error {
	return e.Err
}

func FailureMetadata(err error) (FailureKind, string, bool) {
	var failure RunFailureError
	if errors.As(err, &failure) {
		return failure.Kind, failure.Detail, true
	}
	return "", "", false
}

// Usage is the per-run token consumption reported by the AI CLI. Total
// tokens billed = InputTokens + OutputTokens + CacheWriteTokens; cache
// reads are billed at a discount. Surfaced on traces so operators can
// spot agents that burst the cache and tune accordingly.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}
