package ai

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewTraceStepParserUnknownBackendReturnsNil(t *testing.T) {
	t.Parallel()
	if got := NewTraceStepParser("qwen-7b"); got != nil {
		t.Fatalf("unknown backend parser = %v, want nil", got)
	}
}

func TestClaudeTraceStepParserToolUsePairing(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("claude")

	if got := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/x"}}]}}`)); got != nil {
		t.Fatalf("tool_use should wait for result, got %+v", got)
	}

	got := parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}}`))
	if len(got) != 1 {
		t.Fatalf("expected one tool step, got %+v", got)
	}
	if got[0].Kind != StepKindTool || got[0].ToolName != "Read" {
		t.Fatalf("tool identity mismatch: %+v", got[0])
	}
	if got[0].OutputSummary != "ok" {
		t.Errorf("output: want %q, got %q", "ok", got[0].OutputSummary)
	}
}

func TestClaudeTraceStepParserAssistantText(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("claude")
	got := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking out loud"}]}}`))
	if len(got) != 1 || got[0].Kind != StepKindThinking {
		t.Fatalf("expected one thinking step, got %+v", got)
	}
	if got[0].InputSummary != "thinking out loud" {
		t.Errorf("thinking text mismatch: got %q", got[0].InputSummary)
	}
}

func TestClaudeTraceStepParserSkipsUnclassifiedLines(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("claude")
	for _, line := range [][]byte{nil, []byte(""), []byte("not json"), []byte(`{"type":"unknown"}`)} {
		if got := parse(line); got != nil {
			t.Errorf("line %q: want no steps, got %+v", line, got)
		}
	}
}

func TestCodexTraceStepParserMCPToolCallLifecycle(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("codex")

	startedJSON := `{"type":"item.started","item":{"id":"item_8","type":"mcp_tool_call","server":"github","tool":"issue_read","arguments":{"owner":"eloylp","repo":"agents","issue_number":411},"result":null,"status":"in_progress"}}`
	if got := parse([]byte(startedJSON)); got != nil {
		t.Fatalf("item.started should wait for completion, got %+v", got)
	}

	completedJSON := `{"type":"item.completed","item":{"id":"item_8","type":"mcp_tool_call","server":"github","tool":"issue_read","arguments":{"owner":"eloylp","repo":"agents","issue_number":411},"result":"ok","status":"completed"}}`
	got := parse([]byte(completedJSON))
	if len(got) != 1 {
		t.Fatalf("expected one tool step on item.completed, got %+v", got)
	}
	if got[0].ToolName != "github.issue_read" {
		t.Errorf("tool: want github.issue_read, got %q", got[0].ToolName)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(got[0].InputSummary), &args); err != nil {
		t.Fatalf("input is not valid JSON: %q (%v)", got[0].InputSummary, err)
	}
	if got[0].OutputSummary != "ok" {
		t.Errorf("output: want %q, got %q", "ok", got[0].OutputSummary)
	}
}

func TestCodexTraceStepParserCommandExecutionLifecycle(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("codex")

	if got := parse([]byte(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"ls -la"}}`)); got != nil {
		t.Fatalf("command_execution start should not emit yet, got %+v", got)
	}
	got := parse([]byte(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"ls -la","aggregated_output":"file listing"}}`))
	if len(got) != 1 || got[0].ToolName != "bash" || got[0].InputSummary != "ls -la" || got[0].OutputSummary != "file listing" {
		t.Fatalf("command_execution completed: got %+v", got)
	}
}

func TestCodexTraceStepParserAgentMessageEmitsThinkingOnCompletion(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("codex")

	if got := parse([]byte(`{"type":"item.started","item":{"id":"msg_1","type":"agent_message"}}`)); got != nil {
		t.Errorf("agent_message start should not emit, got %+v", got)
	}
	got := parse([]byte(`{"type":"item.completed","item":{"id":"msg_1","type":"agent_message","text":"plan: do X"}}`))
	if len(got) != 1 || got[0].Kind != StepKindThinking || got[0].InputSummary != "plan: do X" {
		t.Fatalf("agent_message completion: got %+v", got)
	}
}

func TestCodexTraceStepParserToolResultPrefersResultOverOutput(t *testing.T) {
	t.Parallel()
	parse := NewTraceStepParser("codex")
	_ = parse([]byte(`{"type":"item.started","item":{"id":"x","type":"function_call","name":"f"}}`))
	got := parse([]byte(`{"type":"item.completed","item":{"id":"x","type":"function_call","name":"f","result":"R","output":"O"}}`))
	if len(got) != 1 || got[0].OutputSummary != "R" {
		t.Fatalf("want output preferred from result field, got %+v", got)
	}
}

func TestIncrementalTraceStepParsersMatchBatchParsers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		backend string
		lines   []timedLine
		batch   func([]timedLine) []TraceStep
	}{
		{
			name:    "claude",
			backend: "claude",
			lines: toTimedLines([]string{
				`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking"},{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/x"}}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}}`,
			}),
			batch: parseClaudeSteps,
		},
		{
			name:    "codex",
			backend: "codex",
			lines: toTimedLines([]string{
				`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"go test"}}`,
				`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"go test","aggregated_output":"ok"}}`,
				`{"type":"item.completed","item":{"id":"msg_1","type":"agent_message","text":"done"}}`,
			}),
			batch: parseCodexSteps,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := newTimedTraceStepParser(tt.backend)
			var got []TraceStep
			for _, line := range tt.lines {
				got = append(got, parser.process(line.data, line.at)...)
			}
			want := tt.batch(tt.lines)
			if len(got) != len(want) {
				t.Fatalf("len(got) = %d, want %d\ngot=%+v\nwant=%+v", len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("step[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

func toTimedLines(lines []string) []timedLine {
	start := time.Unix(0, 0)
	out := make([]timedLine, 0, len(lines))
	for i, line := range lines {
		out = append(out, timedLine{
			data: []byte(line),
			at:   start.Add(time.Duration(i) * time.Millisecond),
		})
	}
	return out
}
