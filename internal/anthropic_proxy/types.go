// Package anthropicproxy provides a translation layer between the Anthropic
// Messages API and OpenAI Chat Completions API, plus an HTTP handler that
// accepts Anthropic-format requests and proxies them to any OpenAI-compatible
// upstream.
//
// Only non-streaming text and tool-use turns are covered (v1 scope). Streaming,
// vision, prompt-caching control blocks, and extended thinking are out of scope.
package anthropicproxy

import "encoding/json"

// ── Anthropic Messages API ────────────────────────────────────────────────────

// MessagesRequest is the body of POST /v1/messages (Anthropic format).
type MessagesRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []AnthropicMessage  `json:"messages"`
	Tools     []AnthropicTool     `json:"tools,omitempty"`
}

// AnthropicMessage is a single turn in the Anthropic message array.
// Content is either a JSON string or a JSON array of ContentBlock.
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ContentBlock is one element of the content array in an AnthropicMessage.
type ContentBlock struct {
	Type string `json:"type"`

	// type == "text"
	Text string `json:"text,omitempty"`

	// type == "tool_use"
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type == "tool_result"
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or array
}

// AnthropicTool describes a tool available to the model (Anthropic format).
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// MessagesResponse is the Anthropic-format response returned to the client.
type MessagesResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        AnthropicUsage `json:"usage"`
}

// AnthropicUsage carries token counts in the Anthropic response shape.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicError is the Anthropic-format error body returned to the client.
type AnthropicError struct {
	Type  string              `json:"type"`
	Error AnthropicErrorInner `json:"error"`
}

// AnthropicErrorInner holds the error type and human-readable message.
type AnthropicErrorInner struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ── OpenAI Chat Completions API ───────────────────────────────────────────────

// ChatRequest is the body sent to the OpenAI-compatible upstream.
type ChatRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []ChatMessage `json:"messages"`
	Tools     []OAITool     `json:"tools,omitempty"`
}

// ChatMessage is one message in the OpenAI messages array.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one function call inside an assistant ChatMessage.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and its JSON-encoded arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string (not raw JSON)
}

// OAITool is a tool definition in the OpenAI format.
type OAITool struct {
	Type     string      `json:"type"` // always "function"
	Function OAIFunction `json:"function"`
}

// OAIFunction is the function definition inside an OAITool.
type OAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatResponse is the response from the OpenAI-compatible upstream.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   OAIUsage `json:"usage"`
}

// Choice is one generation choice in the OpenAI response.
type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// OAIUsage carries token counts in the OpenAI response shape.
type OAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
