package anthropicproxy

import (
	"encoding/json"
	"testing"
)

// ── ToOpenAI ──────────────────────────────────────────────────────────────────

func TestToOpenAI_TextOnly(t *testing.T) {
	t.Parallel()
	req := MessagesRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: jsonStr("Hello!")},
			{Role: "assistant", Content: jsonStr("Hi there!")},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if got.Model != "qwen" {
		t.Errorf("model: got %q, want %q", got.Model, "qwen")
	}
	if got.MaxTokens != 100 {
		t.Errorf("max_tokens: got %d, want 100", got.MaxTokens)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count: got %d, want 2", len(got.Messages))
	}
	assertMsg(t, got.Messages[0], "user", "Hello!", nil)
	assertMsg(t, got.Messages[1], "assistant", "Hi there!", nil)
}

func TestToOpenAI_SystemMessage(t *testing.T) {
	t.Parallel()
	req := MessagesRequest{
		Model:     "claude",
		MaxTokens: 50,
		System:    "You are a helpful assistant.",
		Messages: []AnthropicMessage{
			{Role: "user", Content: jsonStr("Hi")},
		},
	}

	got, err := ToOpenAI(req, "llama")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count: got %d, want 2", len(got.Messages))
	}
	assertMsg(t, got.Messages[0], "system", "You are a helpful assistant.", nil)
	assertMsg(t, got.Messages[1], "user", "Hi", nil)
}

func TestToOpenAI_ToolUse(t *testing.T) {
	t.Parallel()
	toolInput := json.RawMessage(`{"location":"Paris","unit":"celsius"}`)
	req := MessagesRequest{
		MaxTokens: 200,
		Messages: []AnthropicMessage{
			{Role: "user", Content: jsonStr("What's the weather in Paris?")},
			{
				Role: "assistant",
				Content: jsonBlocks([]ContentBlock{
					{Type: "tool_use", ID: "tu_001", Name: "get_weather", Input: toolInput},
				}),
			},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count: got %d, want 2", len(got.Messages))
	}
	asst := got.Messages[1]
	if asst.Role != "assistant" {
		t.Errorf("role: got %q, want %q", asst.Role, "assistant")
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("tool_calls count: got %d, want 1", len(asst.ToolCalls))
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "tu_001" {
		t.Errorf("tool_call id: got %q, want %q", tc.ID, "tu_001")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("function name: got %q, want %q", tc.Function.Name, "get_weather")
	}
	if tc.Function.Arguments != `{"location":"Paris","unit":"celsius"}` {
		t.Errorf("function arguments: got %q, want canonical JSON", tc.Function.Arguments)
	}
}

func TestToOpenAI_ToolResult(t *testing.T) {
	t.Parallel()
	req := MessagesRequest{
		MaxTokens: 200,
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: jsonBlocks([]ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: "tu_001",
						Content:   json.RawMessage(`"22°C, partly cloudy"`),
					},
				}),
			},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("message count: got %d, want 1", len(got.Messages))
	}
	toolMsg := got.Messages[0]
	if toolMsg.Role != "tool" {
		t.Errorf("role: got %q, want %q", toolMsg.Role, "tool")
	}
	if toolMsg.ToolCallID != "tu_001" {
		t.Errorf("tool_call_id: got %q, want %q", toolMsg.ToolCallID, "tu_001")
	}
	if toolMsg.Content != "22°C, partly cloudy" {
		t.Errorf("content: got %q, want %q", toolMsg.Content, "22°C, partly cloudy")
	}
}

func TestToOpenAI_MultiTurnWithTools(t *testing.T) {
	t.Parallel()
	toolInput := json.RawMessage(`{"q":"go channels"}`)
	req := MessagesRequest{
		MaxTokens: 500,
		System:    "You are a coding assistant.",
		Messages: []AnthropicMessage{
			{Role: "user", Content: jsonStr("Search for Go channels docs.")},
			{
				Role: "assistant",
				Content: jsonBlocks([]ContentBlock{
					{Type: "text", Text: "Let me search for that."},
					{Type: "tool_use", ID: "tc_1", Name: "search", Input: toolInput},
				}),
			},
			{
				Role: "user",
				Content: jsonBlocks([]ContentBlock{
					{Type: "tool_result", ToolUseID: "tc_1", Content: json.RawMessage(`"Go channels are..."`)}},
				),
			},
			{Role: "assistant", Content: jsonStr("Based on the search results...")},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	// system + 4 anthropic messages, but tool_result expands to role:tool message
	// system, user, assistant(tool_calls), tool, assistant
	if len(got.Messages) != 5 {
		t.Fatalf("message count: got %d, want 5: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" {
		t.Errorf("[0] role: got %q", got.Messages[0].Role)
	}
	if got.Messages[1].Role != "user" {
		t.Errorf("[1] role: got %q", got.Messages[1].Role)
	}
	if got.Messages[2].Role != "assistant" || len(got.Messages[2].ToolCalls) == 0 {
		t.Errorf("[2] expected assistant with tool_calls, got role=%q tool_calls=%v", got.Messages[2].Role, got.Messages[2].ToolCalls)
	}
	if got.Messages[3].Role != "tool" {
		t.Errorf("[3] role: got %q, want %q", got.Messages[3].Role, "tool")
	}
	if got.Messages[4].Role != "assistant" {
		t.Errorf("[4] role: got %q", got.Messages[4].Role)
	}
}

func TestToOpenAI_MixedUserTurnOrder(t *testing.T) {
	t.Parallel()
	// A user turn with [tool_result, text] must produce [role:tool, role:user],
	// not [role:user, role:tool] as the old "prepend text" logic did.
	req := MessagesRequest{
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: jsonBlocks([]ContentBlock{
					{Type: "tool_result", ToolUseID: "tc_1", Content: json.RawMessage(`"result text"`)},
					{Type: "text", Text: "Follow-up question."},
				}),
			},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count: got %d, want 2: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "tool" {
		t.Errorf("[0] role: got %q, want %q", got.Messages[0].Role, "tool")
	}
	if got.Messages[0].ToolCallID != "tc_1" {
		t.Errorf("[0] tool_call_id: got %q, want %q", got.Messages[0].ToolCallID, "tc_1")
	}
	if got.Messages[1].Role != "user" {
		t.Errorf("[1] role: got %q, want %q", got.Messages[1].Role, "user")
	}
	if got.Messages[1].Content != "Follow-up question." {
		t.Errorf("[1] content: got %q", got.Messages[1].Content)
	}
}

func TestToOpenAI_ToolDefinitions(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	req := MessagesRequest{
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: "user", Content: jsonStr("hi")}},
		Tools: []AnthropicTool{
			{Name: "search", Description: "Search the web", InputSchema: schema},
		},
	}

	got, err := ToOpenAI(req, "qwen")
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("tools count: got %d, want 1", len(got.Tools))
	}
	tool := got.Tools[0]
	if tool.Type != "function" {
		t.Errorf("tool type: got %q, want %q", tool.Type, "function")
	}
	if tool.Function.Name != "search" {
		t.Errorf("tool name: got %q, want %q", tool.Function.Name, "search")
	}
	if tool.Function.Description != "Search the web" {
		t.Errorf("tool description: got %q", tool.Function.Description)
	}
	if string(tool.Function.Parameters) != string(schema) {
		t.Errorf("parameters: got %s, want %s", tool.Function.Parameters, schema)
	}
}

// ── ToAnthropic ───────────────────────────────────────────────────────────────

func TestToAnthropic_TextOnly(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		ID:    "chatcmpl-abc",
		Model: "qwen",
		Choices: []Choice{
			{
				Index:        0,
				FinishReason: "stop",
				Message:      ChatMessage{Role: "assistant", Content: "Hello!"},
			},
		},
		Usage: OAIUsage{PromptTokens: 10, CompletionTokens: 5},
	}

	got, err := ToAnthropic(resp, "qwen")
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want %q", got.StopReason, "end_turn")
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "Hello!" {
		t.Errorf("content: got %+v", got.Content)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Errorf("usage: got %+v", got.Usage)
	}
	if got.Model != "qwen" {
		t.Errorf("model: got %q", got.Model)
	}
}

func TestToAnthropic_ToolCalls(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		ID: "chatcmpl-xyz",
		Choices: []Choice{
			{
				FinishReason: "tool_calls",
				Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "get_weather",
								Arguments: `{"location":"Tokyo"}`,
							},
						},
					},
				},
			},
		},
		Usage: OAIUsage{PromptTokens: 20, CompletionTokens: 15},
	}

	got, err := ToAnthropic(resp, "qwen")
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want %q", got.StopReason, "tool_use")
	}
	if len(got.Content) != 1 {
		t.Fatalf("content count: got %d, want 1", len(got.Content))
	}
	block := got.Content[0]
	if block.Type != "tool_use" || block.ID != "call_1" || block.Name != "get_weather" {
		t.Errorf("tool_use block: %+v", block)
	}
	if string(block.Input) != `{"location":"Tokyo"}` {
		t.Errorf("input: got %s, want %s", block.Input, `{"location":"Tokyo"}`)
	}
}

func TestToAnthropic_StopReasonTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		finishReason string
		wantStop     string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"content_filter", "end_turn"}, // unknown → end_turn
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.finishReason, func(t *testing.T) {
			t.Parallel()
			resp := ChatResponse{
				Choices: []Choice{
					{FinishReason: tc.finishReason, Message: ChatMessage{Role: "assistant", Content: "ok"}},
				},
			}
			got, err := ToAnthropic(resp, "m")
			if err != nil {
				t.Fatalf("ToAnthropic: %v", err)
			}
			if got.StopReason != tc.wantStop {
				t.Errorf("finish_reason %q: got stop_reason %q, want %q", tc.finishReason, got.StopReason, tc.wantStop)
			}
		})
	}
}

func TestToAnthropic_UsageCounters(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		Choices: []Choice{
			{FinishReason: "stop", Message: ChatMessage{Role: "assistant", Content: "done"}},
		},
		Usage: OAIUsage{PromptTokens: 100, CompletionTokens: 42, TotalTokens: 142},
	}
	got, err := ToAnthropic(resp, "m")
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.Usage.InputTokens != 100 {
		t.Errorf("input_tokens: got %d, want 100", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 42 {
		t.Errorf("output_tokens: got %d, want 42", got.Usage.OutputTokens)
	}
}

func TestToAnthropic_EmptyChoices(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{}
	got, err := ToAnthropic(resp, "m")
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want %q", got.StopReason, "end_turn")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jsonStr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func jsonBlocks(blocks []ContentBlock) json.RawMessage {
	b, _ := json.Marshal(blocks)
	return json.RawMessage(b)
}

// TestToOpenAI_UnsupportedUserBlock verifies that a user turn containing an
// unsupported content block type (e.g. "image") returns a translation error
// rather than silently dropping the block.
func TestToOpenAI_UnsupportedUserBlock(t *testing.T) {
	t.Parallel()
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: jsonBlocks([]ContentBlock{{Type: "image", Text: ""}}),
			},
		},
	}
	_, err := ToOpenAI(req, "gpt-4")
	if err == nil {
		t.Fatal("expected error for unsupported user block type, got nil")
	}
}

// TestToOpenAI_UnsupportedAssistantBlock verifies that an assistant turn
// containing an unsupported content block type (e.g. "thinking") returns a
// translation error rather than silently dropping the block.
func TestToOpenAI_UnsupportedAssistantBlock(t *testing.T) {
	t.Parallel()
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{
				Role:    "assistant",
				Content: jsonBlocks([]ContentBlock{{Type: "thinking", Text: "some chain of thought"}}),
			},
		},
	}
	_, err := ToOpenAI(req, "gpt-4")
	if err == nil {
		t.Fatal("expected error for unsupported assistant block type, got nil")
	}
}

// TestToOpenAI_InterleavedAssistantBlocks verifies that an assistant turn with
// a text block following a tool_use block is rejected. OpenAI's message schema
// cannot represent this ordering without data loss, so the proxy must fail fast
// rather than silently reorder the content.
func TestToOpenAI_InterleavedAssistantBlocks(t *testing.T) {
	t.Parallel()
	toolInput := json.RawMessage(`{"key":"val"}`)
	// Pattern: text("before"), tool_use(A), text("after") — trailing text after tool_use.
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{
				Role: "assistant",
				Content: jsonBlocks([]ContentBlock{
					{Type: "text", Text: "before"},
					{Type: "tool_use", ID: "tu_1", Name: "my_tool", Input: toolInput},
					{Type: "text", Text: "after"},
				}),
			},
		},
	}
	_, err := ToOpenAI(req, "gpt-4")
	if err == nil {
		t.Fatal("expected error for interleaved text/tool_use ordering, got nil")
	}
}

// TestToOpenAI_LeadingTextThenToolUseIsValid verifies that the common pattern
// of text-then-tool_use (non-interleaved) translates without error.
func TestToOpenAI_LeadingTextThenToolUseIsValid(t *testing.T) {
	t.Parallel()
	toolInput := json.RawMessage(`{}`)
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{
				Role: "assistant",
				Content: jsonBlocks([]ContentBlock{
					{Type: "text", Text: "Let me call the tool."},
					{Type: "tool_use", ID: "tu_1", Name: "do_it", Input: toolInput},
				}),
			},
		},
	}
	got, err := ToOpenAI(req, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error for valid text-then-tool_use turn: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("message count: got %d, want 1", len(got.Messages))
	}
	msg := got.Messages[0]
	if msg.Content != "Let me call the tool." {
		t.Errorf("content: got %q, want %q", msg.Content, "Let me call the tool.")
	}
	if len(msg.ToolCalls) != 1 {
		t.Errorf("tool_calls count: got %d, want 1", len(msg.ToolCalls))
	}
}

// TestToOpenAI_UnsupportedToolResultBlock verifies that a tool_result whose
// content array contains an unsupported nested block type (e.g. "image")
// returns a translation error rather than silently dropping the block and
// producing lossy/partial text content.
func TestToOpenAI_UnsupportedToolResultBlock(t *testing.T) {
	t.Parallel()
	// tool_result with a content array that includes an image block.
	toolResultContent := json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]`)
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: jsonBlocks([]ContentBlock{
					{Type: "tool_result", ToolUseID: "tc_1", Content: toolResultContent},
				}),
			},
		},
	}
	_, err := ToOpenAI(req, "gpt-4")
	if err == nil {
		t.Fatal("expected error for unsupported tool_result nested block type, got nil")
	}
}

func assertMsg(t *testing.T, msg ChatMessage, role, content string, toolCalls []ToolCall) {
	t.Helper()
	if msg.Role != role {
		t.Errorf("role: got %q, want %q", msg.Role, role)
	}
	if msg.Content != content {
		t.Errorf("content: got %q, want %q", msg.Content, content)
	}
	if len(toolCalls) == 0 && len(msg.ToolCalls) != 0 {
		t.Errorf("expected no tool_calls, got %d", len(msg.ToolCalls))
	}
}
