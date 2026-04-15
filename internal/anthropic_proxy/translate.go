package anthropicproxy

import (
	"encoding/json"
	"fmt"
)

// ToOpenAI translates an Anthropic MessagesRequest into an OpenAI ChatRequest
// using the provided upstream model name. The Anthropic model field is ignored;
// the caller supplies the model to use upstream.
//
// Translation rules (v1 scope):
//   - Top-level system string → role:system message prepended to messages.
//   - User/assistant text content → role:user / role:assistant with string content.
//   - Assistant tool_use blocks → role:assistant with tool_calls.
//   - User tool_result blocks → role:tool messages (one per block).
//   - Tools: input_schema → parameters; type set to "function".
func ToOpenAI(req MessagesRequest, upstreamModel string) (ChatRequest, error) {
	out := ChatRequest{
		Model:     upstreamModel,
		MaxTokens: req.MaxTokens,
	}

	// System prompt becomes the first message.
	if req.System != "" {
		out.Messages = append(out.Messages, ChatMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	for i, msg := range req.Messages {
		msgs, err := translateAnthropicMessage(msg)
		if err != nil {
			return ChatRequest{}, fmt.Errorf("message[%d]: %w", i, err)
		}
		out.Messages = append(out.Messages, msgs...)
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, OAITool{
			Type: "function",
			Function: OAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return out, nil
}

// translateAnthropicMessage converts one Anthropic message into zero or more
// OpenAI messages. A user message containing tool_result blocks expands into
// multiple role:tool messages (one per block).
func translateAnthropicMessage(msg AnthropicMessage) ([]ChatMessage, error) {
	blocks, text, err := parseContent(msg.Content)
	if err != nil {
		return nil, err
	}

	switch msg.Role {
	case "user":
		return translateUserMessage(text, blocks)
	case "assistant":
		return translateAssistantMessage(text, blocks)
	default:
		return nil, fmt.Errorf("unsupported role %q", msg.Role)
	}
}

// translateUserMessage handles role:user Anthropic messages. Tool result blocks
// are emitted as role:tool messages; remaining text is emitted as role:user.
func translateUserMessage(text string, blocks []ContentBlock) ([]ChatMessage, error) {
	var out []ChatMessage

	for _, b := range blocks {
		switch b.Type {
		case "text":
			// accumulated below
		case "tool_result":
			content, err := toolResultContent(b.Content)
			if err != nil {
				return nil, fmt.Errorf("tool_result content: %w", err)
			}
			out = append(out, ChatMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    content,
			})
		}
	}

	if text != "" {
		out = append([]ChatMessage{{Role: "user", Content: text}}, out...)
	} else if len(out) == 0 {
		// No text, no tool results — emit an empty user message to preserve turn order.
		out = append(out, ChatMessage{Role: "user"})
	}

	return out, nil
}

// translateAssistantMessage handles role:assistant Anthropic messages.
// Tool use blocks become OpenAI tool_calls; text blocks become the content.
func translateAssistantMessage(text string, blocks []ContentBlock) ([]ChatMessage, error) {
	msg := ChatMessage{Role: "assistant", Content: text}

	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		args, err := rawJSONToString(b.Input)
		if err != nil {
			return nil, fmt.Errorf("tool_use input: %w", err)
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   b.ID,
			Type: "function",
			Function: ToolCallFunction{
				Name:      b.Name,
				Arguments: args,
			},
		})
	}

	return []ChatMessage{msg}, nil
}

// ToAnthropic translates an OpenAI ChatResponse back to Anthropic MessagesResponse
// shape. Only the first choice is used. The upstream model name is reflected in
// the response.
//
// Translation rules (v1 scope):
//   - Text content → type:text content block.
//   - tool_calls → type:tool_use content blocks; arguments JSON string → input object.
//   - finish_reason: stop → stop_reason: end_turn.
//   - finish_reason: length → stop_reason: max_tokens.
//   - finish_reason: tool_calls → stop_reason: tool_use.
//   - usage: prompt_tokens/completion_tokens → input_tokens/output_tokens.
func ToAnthropic(resp ChatResponse, upstreamModel string) (MessagesResponse, error) {
	out := MessagesResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: upstreamModel,
		Usage: AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	if len(resp.Choices) == 0 {
		out.StopReason = "end_turn"
		return out, nil
	}

	choice := resp.Choices[0]
	out.StopReason = translateFinishReason(choice.FinishReason)

	if choice.Message.Content != "" {
		out.Content = append(out.Content, ContentBlock{
			Type: "text",
			Text: choice.Message.Content,
		})
	}

	for _, tc := range choice.Message.ToolCalls {
		input, err := stringToRawJSON(tc.Function.Arguments)
		if err != nil {
			return MessagesResponse{}, fmt.Errorf("tool_call arguments: %w", err)
		}
		out.Content = append(out.Content, ContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	if len(out.Content) == 0 {
		out.Content = []ContentBlock{}
	}

	return out, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// parseContent decodes the Anthropic content field. Content can be a JSON
// string (returns it as text with no blocks) or a JSON array of ContentBlock.
// Text blocks in an array are concatenated with newlines.
func parseContent(raw json.RawMessage) (blocks []ContentBlock, text string, err error) {
	if len(raw) == 0 {
		return nil, "", nil
	}

	// Peek at the first non-whitespace byte to determine type.
	first := firstNonSpace(raw)
	switch first {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, "", fmt.Errorf("parse string content: %w", err)
		}
		return nil, s, nil
	case '[':
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil, "", fmt.Errorf("parse content array: %w", err)
		}
		for _, b := range blocks {
			if b.Type == "text" {
				if text != "" {
					text += "\n"
				}
				text += b.Text
			}
		}
		return blocks, text, nil
	default:
		return nil, "", fmt.Errorf("unexpected content type (first byte: %q)", first)
	}
}

func firstNonSpace(b []byte) byte {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return c
		}
	}
	return 0
}

// toolResultContent extracts the string content from a tool_result block.
// The content field can be a JSON string or an array of content blocks.
func toolResultContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	first := firstNonSpace(raw)
	switch first {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("parse tool_result string: %w", err)
		}
		return s, nil
	case '[':
		var blocks []ContentBlock
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", fmt.Errorf("parse tool_result blocks: %w", err)
		}
		var out string
		for _, b := range blocks {
			if b.Type == "text" {
				if out != "" {
					out += "\n"
				}
				out += b.Text
			}
		}
		return out, nil
	default:
		return "", fmt.Errorf("unexpected tool_result content type (first byte: %q)", first)
	}
}

// rawJSONToString marshal a raw JSON value to its string representation.
// Used to convert Anthropic tool input (raw JSON object) to OpenAI function
// arguments (JSON-encoded string).
func rawJSONToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	// Validate that the raw JSON is valid before round-tripping.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("invalid tool input JSON: %w", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("re-marshal tool input: %w", err)
	}
	return string(b), nil
}

// stringToRawJSON parses a JSON string (OpenAI function arguments) into a
// raw JSON value (Anthropic tool input).
func stringToRawJSON(s string) (json.RawMessage, error) {
	if s == "" {
		return json.RawMessage("{}"), nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("parse function arguments: %w", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-marshal function arguments: %w", err)
	}
	return json.RawMessage(b), nil
}

// translateFinishReason maps an OpenAI finish_reason to an Anthropic stop_reason.
func translateFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}
