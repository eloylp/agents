package ai

import (
	"encoding/json"
	"testing"
)

func TestNewStreamLineParserUnknownBackendEmitsRaw(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("qwen-7b")
	got := parse([]byte(`{"foo":"bar"}`))
	if len(got) != 1 || got[0].Kind != StreamKindRaw {
		t.Fatalf("want one raw event, got %+v", got)
	}
	if got[0].Raw == "" {
		t.Errorf("raw event missing original line")
	}
}

func TestNewStreamLineParserSkipsBlankRawLine(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("unknown")
	if got := parse([]byte("   ")); got != nil {
		t.Errorf("blank line should yield no events, got %+v", got)
	}
}

func TestClaudeStreamParserToolUsePairing(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")

	// assistant tool_use kicks off a tool_use event.
	got := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/x"}}]}}`))
	if len(got) != 1 || got[0].Kind != StreamKindToolUse {
		t.Fatalf("expected one tool_use event, got %+v", got)
	}
	if got[0].Tool != "Read" {
		t.Errorf("tool: want Read, got %q", got[0].Tool)
	}
	if got[0].Input == "" {
		t.Errorf("tool_use input was empty")
	}

	// matching user tool_result emits a tool_result with the same tool name
	// and a non-zero duration.
	got = parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}}`))
	if len(got) != 1 || got[0].Kind != StreamKindToolResult {
		t.Fatalf("expected one tool_result event, got %+v", got)
	}
	if got[0].Tool != "Read" {
		t.Errorf("tool_result tool: want Read (paired), got %q", got[0].Tool)
	}
	if got[0].Output != "ok" {
		t.Errorf("tool_result output: want %q, got %q", "ok", got[0].Output)
	}
}

func TestClaudeStreamParserOrphanToolResultFallsBackToRaw(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")
	got := parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"unknown","content":"x"}]}}`))
	if len(got) != 1 || got[0].Kind != StreamKindRaw {
		t.Errorf("orphan tool_result should yield a raw event, got %+v", got)
	}
}

func TestClaudeStreamParserAssistantText(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")
	got := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking out loud"}]}}`))
	if len(got) != 1 || got[0].Kind != StreamKindThinking {
		t.Fatalf("expected one thinking event, got %+v", got)
	}
	if got[0].Text != "thinking out loud" {
		t.Errorf("thinking text mismatch: got %q", got[0].Text)
	}
}

func TestClaudeStreamParserResultEmitsUsage(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")
	got := parse([]byte(`{"type":"result","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}`))
	if len(got) != 1 || got[0].Kind != StreamKindUsage {
		t.Fatalf("expected one usage event, got %+v", got)
	}
	u := got[0].Usage
	if u == nil || u.InputTokens != 10 || u.OutputTokens != 20 || u.CacheReadTokens != 5 || u.CacheWriteTokens != 3 {
		t.Errorf("usage breakdown mismatch: %+v", u)
	}
}

func TestClaudeStreamParserSkipsBlankLines(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")
	for _, line := range [][]byte{nil, []byte(""), []byte("   ")} {
		if got := parse(line); got != nil {
			t.Errorf("line %q: want no events, got %+v", line, got)
		}
	}
}

func TestClaudeStreamParserUnclassifiedLinesFallBackToRaw(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("claude")
	for _, line := range [][]byte{[]byte("not json"), []byte(`{"type":"unknown"}`)} {
		got := parse(line)
		if len(got) != 1 || got[0].Kind != StreamKindRaw {
			t.Errorf("line %q: want one raw event, got %+v", line, got)
		}
	}
}

func TestCodexStreamParserMCPToolCallLifecycle(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")

	// Real-world example surfaced by the bug report.
	startedJSON := `{"type":"item.started","item":{"id":"item_8","type":"mcp_tool_call","server":"github","tool":"issue_read","arguments":{"owner":"eloylp","repo":"agents","issue_number":411,"method":"get_comments","perPage":100},"result":null,"error":null,"status":"in_progress"}}`
	got := parse([]byte(startedJSON))
	if len(got) != 1 || got[0].Kind != StreamKindToolUse {
		t.Fatalf("expected one tool_use on item.started, got %+v", got)
	}
	if got[0].Tool != "issue_read" {
		t.Errorf("tool: want issue_read, got %q", got[0].Tool)
	}
	if got[0].Server != "github" {
		t.Errorf("server: want github, got %q", got[0].Server)
	}
	// arguments must be JSON-rendered, not stringified.
	var args map[string]any
	if err := json.Unmarshal([]byte(got[0].Input), &args); err != nil {
		t.Fatalf("input is not valid JSON: %q (%v)", got[0].Input, err)
	}
	if args["owner"] != "eloylp" {
		t.Errorf("arguments.owner mismatch: %v", args["owner"])
	}

	// Completion emits a tool_result with the same identifiers.
	completedJSON := `{"type":"item.completed","item":{"id":"item_8","type":"mcp_tool_call","server":"github","tool":"issue_read","arguments":{"owner":"eloylp","repo":"agents","issue_number":411},"result":"ok","error":null,"status":"completed"}}`
	got = parse([]byte(completedJSON))
	if len(got) != 1 || got[0].Kind != StreamKindToolResult {
		t.Fatalf("expected one tool_result on item.completed, got %+v", got)
	}
	if got[0].Tool != "issue_read" || got[0].Server != "github" {
		t.Errorf("identity mismatch on completion: tool=%q server=%q", got[0].Tool, got[0].Server)
	}
	if got[0].Output != "ok" {
		t.Errorf("output: want %q, got %q", "ok", got[0].Output)
	}
}

func TestCodexStreamParserCommandExecutionLifecycle(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")

	got := parse([]byte(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"ls -la"}}`))
	if len(got) != 1 || got[0].Kind != StreamKindToolUse || got[0].Tool != "bash" || got[0].Input != "ls -la" {
		t.Fatalf("command_execution started: got %+v", got)
	}

	got = parse([]byte(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","aggregated_output":"file listing"}}`))
	if len(got) != 1 || got[0].Kind != StreamKindToolResult || got[0].Tool != "bash" || got[0].Output != "file listing" {
		t.Fatalf("command_execution completed: got %+v", got)
	}
}

func TestCodexStreamParserAgentMessageEmitsThinkingOnCompletion(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")

	// item.started with agent_message is suppressed (the message text only
	// arrives on completion).
	if got := parse([]byte(`{"type":"item.started","item":{"id":"msg_1","type":"agent_message"}}`)); got != nil {
		t.Errorf("agent_message item.started should not emit an event, got %+v", got)
	}

	got := parse([]byte(`{"type":"item.completed","item":{"id":"msg_1","type":"agent_message","text":"plan: do X"}}`))
	if len(got) != 1 || got[0].Kind != StreamKindThinking || got[0].Text != "plan: do X" {
		t.Fatalf("agent_message completion: got %+v", got)
	}
}

func TestCodexStreamParserFunctionCallFallback(t *testing.T) {
	t.Parallel()
	// function_call uses `name` rather than `tool`; the parser should pick
	// it up via the fallback path.
	parse := NewStreamLineParser("codex")
	got := parse([]byte(`{"type":"item.started","item":{"id":"fn_1","type":"function_call","name":"compute","arguments":{"x":1}}}`))
	if len(got) != 1 || got[0].Kind != StreamKindToolUse || got[0].Tool != "compute" {
		t.Fatalf("function_call started: got %+v", got)
	}
}

func TestCodexStreamParserTurnCompletedEmitsUsage(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")
	got := parse([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":200,"cached_input_tokens":50}}`))
	if len(got) != 1 || got[0].Kind != StreamKindUsage {
		t.Fatalf("expected one usage event, got %+v", got)
	}
	u := got[0].Usage
	if u == nil || u.InputTokens != 100 || u.OutputTokens != 200 || u.CacheReadTokens != 50 {
		t.Errorf("codex usage mismatch: %+v", u)
	}
}

func TestCodexStreamParserToolResultPrefersResultOverOutput(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")
	// register the start so the completion has a duration and pairs cleanly.
	_ = parse([]byte(`{"type":"item.started","item":{"id":"x","type":"function_call","name":"f"}}`))
	got := parse([]byte(`{"type":"item.completed","item":{"id":"x","type":"function_call","name":"f","result":"R","output":"O"}}`))
	if len(got) != 1 || got[0].Output != "R" {
		t.Fatalf("want output preferred from result field, got %+v", got)
	}
}

func TestCodexStreamParserDurationsBetweenStartAndComplete(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")
	_ = parse([]byte(`{"type":"item.started","item":{"id":"d","type":"command_execution","command":"sleep 0"}}`))
	got := parse([]byte(`{"type":"item.completed","item":{"id":"d","type":"command_execution","aggregated_output":""}}`))
	if len(got) != 1 || got[0].DurationMs < 0 {
		t.Fatalf("duration must be non-negative when start was observed, got %+v", got)
	}
}

func TestCodexStreamParserUnclassifiedLinesFallBackToRaw(t *testing.T) {
	t.Parallel()
	parse := NewStreamLineParser("codex")
	for _, line := range [][]byte{[]byte("not json"), []byte(`{"type":"thread.started"}`), []byte(`{"type":"item.started","item":{"id":"x","type":"future_tool"}}`)} {
		got := parse(line)
		if len(got) != 1 || got[0].Kind != StreamKindRaw {
			t.Errorf("line %q: want one raw event, got %+v", line, got)
		}
	}
}
