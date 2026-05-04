package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// StreamEvent is the normalized representation of one tool-loop event in the
// live SSE feed. Per-backend streaming parsers convert each raw stdout line
// into zero or more StreamEvents before they are published into the per-span
// runStream. The frontend's runners and traces views consume this canonical
// shape directly and no longer need format-specific parsers.
//
// Lifecycle for a tool call: one ToolUse event when the call starts (Tool,
// Server, Input populated) followed by one ToolResult event when it
// completes (Tool, Server, Output or Error, DurationMs). This matches the
// two-card visual the persisted-step path already produces.
type StreamEvent struct {
	Kind       string            `json:"kind"`
	Tool       string            `json:"tool,omitempty"`
	Server     string            `json:"server,omitempty"`
	Input      string            `json:"input,omitempty"`
	Output     string            `json:"output,omitempty"`
	Text       string            `json:"text,omitempty"`
	Error      string            `json:"error,omitempty"`
	DurationMs int64             `json:"duration_ms,omitempty"`
	Usage      *StreamEventUsage `json:"usage,omitempty"`
	Raw        string            `json:"raw,omitempty"`
}

// StreamEventUsage is the token breakdown attached to a StreamEvent of kind
// "usage". Cache fields are zero on backends that do not report them.
type StreamEventUsage struct {
	InputTokens      int64 `json:"input_tokens,omitempty"`
	OutputTokens     int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// StreamEvent.Kind values.
const (
	StreamKindToolUse    = "tool_use"
	StreamKindToolResult = "tool_result"
	StreamKindThinking   = "thinking"
	StreamKindUsage      = "usage"
	StreamKindRaw        = "raw"
)

// StreamLineParser converts one raw CLI stdout line into zero or more
// normalized StreamEvents. Implementations are stateful (they pair tool_use
// with tool_result by ID, and pair item.started with item.completed by
// duration). The runner serializes calls to a parser per run, so
// implementations are not required to be safe for concurrent use.
type StreamLineParser func(line []byte) []StreamEvent

// NewStreamLineParser returns a fresh parser bound to the named backend.
// Each agent run gets its own parser instance so per-run state (pending
// tool calls, started timestamps) does not leak across runs.
func NewStreamLineParser(backendName string) StreamLineParser {
	switch {
	case strings.HasPrefix(backendName, "claude"):
		s := &claudeStreamParser{pending: map[string]claudeStreamPending{}}
		return s.process
	case strings.HasPrefix(backendName, "codex"):
		s := &codexStreamParser{started: map[string]codexStreamStarted{}}
		return s.process
	}
	return rawStreamLine
}

// rawStreamLine is the fallback parser for unrecognized backends. It wraps
// each line in a "raw" event so the frontend can still display something.
func rawStreamLine(line []byte) []StreamEvent {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	return []StreamEvent{{Kind: StreamKindRaw, Raw: string(line)}}
}

// ── Claude (stream-json) ─────────────────────────────────────────────────────

type claudeStreamParser struct {
	pending map[string]claudeStreamPending // tool_use_id → pending
}

type claudeStreamPending struct {
	name   string
	server string
	input  string
	seenAt time.Time
}

func (p *claudeStreamParser) process(line []byte) []StreamEvent {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	type contentBlock struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	type streamLine struct {
		Type    string `json:"type"`
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
		Usage json.RawMessage `json:"usage"`
	}

	var ev streamLine
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil
	}

	now := time.Now()
	var out []StreamEvent
	switch ev.Type {
	case "assistant":
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "text":
				text := strings.TrimSpace(b.Text)
				if text == "" {
					continue
				}
				out = append(out, StreamEvent{Kind: StreamKindThinking, Text: text})
			case "tool_use":
				if b.ID == "" {
					continue
				}
				input := strings.TrimSpace(string(b.Input))
				p.pending[b.ID] = claudeStreamPending{name: b.Name, input: input, seenAt: now}
				out = append(out, StreamEvent{
					Kind:  StreamKindToolUse,
					Tool:  b.Name,
					Input: input,
				})
			}
		}
	case "user":
		for _, b := range ev.Message.Content {
			if b.Type != "tool_result" || b.ToolUseID == "" {
				continue
			}
			pend, ok := p.pending[b.ToolUseID]
			if !ok {
				continue
			}
			delete(p.pending, b.ToolUseID)
			output := extractToolResultText(b.Content)
			out = append(out, StreamEvent{
				Kind:       StreamKindToolResult,
				Tool:       pend.name,
				Output:     output,
				DurationMs: now.Sub(pend.seenAt).Milliseconds(),
			})
		}
	case "result":
		if u, ok := parseClaudeUsage(ev.Usage); ok {
			out = append(out, StreamEvent{Kind: StreamKindUsage, Usage: &u})
		}
	}
	return out
}

// parseClaudeUsage extracts an Anthropic-style usage object. Returns ok=false
// when the field is missing or empty so the caller can skip emission.
func parseClaudeUsage(raw json.RawMessage) (StreamEventUsage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return StreamEventUsage{}, false
	}
	var u struct {
		InputTokens             int64 `json:"input_tokens"`
		OutputTokens            int64 `json:"output_tokens"`
		CacheReadInputTokens    int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return StreamEventUsage{}, false
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return StreamEventUsage{}, false
	}
	return StreamEventUsage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}, true
}

// ── Codex (--json) ───────────────────────────────────────────────────────────

type codexStreamParser struct {
	started map[string]codexStreamStarted // item.id → started
}

type codexStreamStarted struct {
	seenAt time.Time
}

func (p *codexStreamParser) process(line []byte) []StreamEvent {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	type item struct {
		ID               string          `json:"id"`
		Type             string          `json:"type"`
		Text             string          `json:"text"`
		Command          string          `json:"command"`
		AggregatedOutput string          `json:"aggregated_output"`
		Name             string          `json:"name"`
		Tool             string          `json:"tool"`
		Server           string          `json:"server"`
		Arguments        json.RawMessage `json:"arguments"`
		Output           json.RawMessage `json:"output"`
		Result           json.RawMessage `json:"result"`
		Error            json.RawMessage `json:"error"`
	}
	type streamLine struct {
		Type  string          `json:"type"`
		Item  item            `json:"item"`
		Usage json.RawMessage `json:"usage"`
	}

	var ev streamLine
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil
	}

	now := time.Now()
	var out []StreamEvent
	switch ev.Type {
	case "item.started":
		if ev.Item.ID != "" {
			p.started[ev.Item.ID] = codexStreamStarted{seenAt: now}
		}
		switch ev.Item.Type {
		case "command_execution":
			out = append(out, StreamEvent{Kind: StreamKindToolUse, Tool: "bash", Input: ev.Item.Command})
		case "agent_message":
			// noise, agent_message becomes thinking on completion
		default:
			tool, server := codexToolIdentity(ev.Item.Tool, ev.Item.Name, ev.Item.Server)
			if tool == "" {
				return nil
			}
			out = append(out, StreamEvent{
				Kind:   StreamKindToolUse,
				Tool:   tool,
				Server: server,
				Input:  rawJSONString(ev.Item.Arguments),
			})
		}
	case "item.completed":
		dur := int64(0)
		if started, ok := p.started[ev.Item.ID]; ok {
			dur = now.Sub(started.seenAt).Milliseconds()
			delete(p.started, ev.Item.ID)
		}
		switch ev.Item.Type {
		case "agent_message":
			text := strings.TrimSpace(ev.Item.Text)
			if text == "" {
				return nil
			}
			out = append(out, StreamEvent{Kind: StreamKindThinking, Text: text})
		case "command_execution":
			out = append(out, StreamEvent{
				Kind:       StreamKindToolResult,
				Tool:       "bash",
				Output:     ev.Item.AggregatedOutput,
				DurationMs: dur,
			})
		default:
			tool, server := codexToolIdentity(ev.Item.Tool, ev.Item.Name, ev.Item.Server)
			if tool == "" {
				return nil
			}
			out = append(out, StreamEvent{
				Kind:       StreamKindToolResult,
				Tool:       tool,
				Server:     server,
				Output:     codexOutput(ev.Item.Result, ev.Item.Output),
				Error:      rawJSONString(ev.Item.Error),
				DurationMs: dur,
			})
		}
	case "turn.completed":
		if u, ok := parseCodexUsage(ev.Usage); ok {
			out = append(out, StreamEvent{Kind: StreamKindUsage, Usage: &u})
		}
	}
	return out
}

// codexToolIdentity picks the tool name (preferring `tool` over `name`) and
// returns the server prefix when present. Returns ("", "") when neither is set.
func codexToolIdentity(tool, name, server string) (string, string) {
	t := tool
	if t == "" {
		t = name
	}
	return t, server
}

// codexOutput prefers `result` (mcp_tool_call) and falls back to `output`
// (function_call). Returns an empty string when neither carries content.
func codexOutput(result, output json.RawMessage) string {
	if s := rawJSONString(result); s != "" {
		return s
	}
	return rawJSONString(output)
}

// rawJSONString returns the trimmed string form of a JSON value, unwrapping
// quoted strings to their textual content. Empty / null values become "".
func rawJSONString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(trimmed)
}

// parseCodexUsage extracts an OpenAI-style usage object from a turn.completed
// line. Codex reports input/output tokens and a cached_input_tokens field.
func parseCodexUsage(raw json.RawMessage) (StreamEventUsage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return StreamEventUsage{}, false
	}
	var u struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		CachedInputTokens  int64 `json:"cached_input_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return StreamEventUsage{}, false
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CachedInputTokens == 0 {
		return StreamEventUsage{}, false
	}
	return StreamEventUsage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CachedInputTokens,
	}, true
}
