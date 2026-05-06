package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// TraceStepParser converts one raw CLI stdout line into zero or more durable
// TraceStep rows. Implementations are stateful: they pair tool starts with
// tool completions to emit one complete "tool" step. The runner serializes
// calls to a parser per run, so implementations are not safe for concurrent
// use.
type TraceStepParser func(line []byte) []TraceStep

type timedTraceStepParser interface {
	process(line []byte, at time.Time) []TraceStep
}

// NewTraceStepParser returns a fresh incremental transcript parser bound to
// the named backend. Unknown backends return nil because raw stdout is not a
// durable TraceStep.
func NewTraceStepParser(backendName string) TraceStepParser {
	p := newTimedTraceStepParser(backendName)
	if p == nil {
		return nil
	}
	return func(line []byte) []TraceStep {
		return p.process(line, time.Now())
	}
}

func newTimedTraceStepParser(backendName string) timedTraceStepParser {
	switch {
	case strings.HasPrefix(backendName, "claude"):
		return &claudeTraceStepParser{pending: map[string]claudeTraceStepPending{}}
	case strings.HasPrefix(backendName, "codex"):
		return &codexTraceStepParser{started: map[string]codexTraceStepStarted{}}
	default:
		return nil
	}
}

// -- Claude (stream-json) -----------------------------------------------------

type claudeTraceStepParser struct {
	pending map[string]claudeTraceStepPending // tool_use_id -> pending
	count   int
}

type claudeTraceStepPending struct {
	name   string
	input  string
	seenAt time.Time
}

func (p *claudeTraceStepParser) process(line []byte, at time.Time) []TraceStep {
	if p.count >= 100 {
		return nil
	}
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
	}

	var ev streamLine
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil
	}

	var out []TraceStep
	switch ev.Type {
	case "assistant":
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "text":
				text := strings.TrimSpace(b.Text)
				if text == "" {
					continue
				}
				out = append(out, TraceStep{
					Kind:         StepKindThinking,
					InputSummary: capStepContent(text),
				})
				p.count++
				if p.count >= 100 {
					return out
				}
			case "tool_use":
				if b.ID == "" {
					continue
				}
				p.pending[b.ID] = claudeTraceStepPending{
					name:   b.Name,
					input:  capStepContent(string(b.Input)),
					seenAt: at,
				}
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
			out = append(out, TraceStep{
				Kind:          StepKindTool,
				ToolName:      pend.name,
				InputSummary:  pend.input,
				OutputSummary: capStepContent(extractToolResultText(b.Content)),
				DurationMs:    at.Sub(pend.seenAt).Milliseconds(),
			})
			p.count++
			if p.count >= 100 {
				return out
			}
		}
	}
	return out
}

// -- Codex (--json) -----------------------------------------------------------

type codexTraceStepParser struct {
	started map[string]codexTraceStepStarted // item.id -> started
	count   int
}

type codexTraceStepStarted struct {
	seenAt time.Time
}

type codexTraceStepItem struct {
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
}

func (p *codexTraceStepParser) process(line []byte, at time.Time) []TraceStep {
	if p.count >= 100 {
		return nil
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	type streamLine struct {
		Type string             `json:"type"`
		Item codexTraceStepItem `json:"item"`
	}

	var ev streamLine
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil
	}

	switch ev.Type {
	case "item.started":
		if ev.Item.ID != "" {
			p.started[ev.Item.ID] = codexTraceStepStarted{seenAt: at}
		}
		return nil
	case "item.completed":
		step, ok := p.codexStep(ev.Item, at)
		if !ok {
			return nil
		}
		p.count++
		return []TraceStep{step}
	default:
		return nil
	}
}

func (p *codexTraceStepParser) codexStep(it codexTraceStepItem, at time.Time) (TraceStep, bool) {
	switch it.Type {
	case "agent_message":
		text := strings.TrimSpace(it.Text)
		if text == "" {
			return TraceStep{}, false
		}
		return TraceStep{
			Kind:         StepKindThinking,
			InputSummary: capStepContent(text),
		}, true
	case "command_execution":
		return TraceStep{
			Kind:          StepKindTool,
			ToolName:      "bash",
			InputSummary:  capStepContent(it.Command),
			OutputSummary: capStepContent(it.AggregatedOutput),
			DurationMs:    p.duration(it.ID, at),
		}, true
	default:
		name := it.Tool
		if name == "" {
			name = it.Name
		}
		if name == "" {
			return TraceStep{}, false
		}
		toolName := name
		if it.Server != "" {
			toolName = it.Server + "." + name
		}
		input := rawJSONString(it.Arguments)
		output := codexOutput(it.Result, it.Output)
		if input == "" && output == "" {
			return TraceStep{}, false
		}
		return TraceStep{
			Kind:          StepKindTool,
			ToolName:      toolName,
			InputSummary:  capStepContent(input),
			OutputSummary: capStepContent(output),
			DurationMs:    p.duration(it.ID, at),
		}, true
	}
}

func (p *codexTraceStepParser) duration(id string, at time.Time) int64 {
	if started, ok := p.started[id]; ok {
		delete(p.started, id)
		return at.Sub(started.seenAt).Milliseconds()
	}
	return 0
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
