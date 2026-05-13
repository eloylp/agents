package anthropicproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// UpstreamConfig holds the connection parameters for the OpenAI-compatible upstream.
type UpstreamConfig struct {
	URL       string
	Model     string
	APIKey    string
	Timeout   time.Duration
	ExtraBody map[string]any
}

// Handler is an HTTP handler that accepts Anthropic Messages API requests,
// translates them to OpenAI Chat Completions, proxies them to an upstream, and
// returns the response in Anthropic format.
type Handler struct {
	upstream UpstreamConfig
	client   *http.Client
	logger   zerolog.Logger
}

// NewHandler creates a Handler with the given upstream configuration.
func NewHandler(upstream UpstreamConfig, logger zerolog.Logger) *Handler {
	return &Handler{
		upstream: upstream,
		client:   &http.Client{Timeout: upstream.Timeout},
		logger:   logger.With().Str("component", "anthropic_proxy").Logger(),
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // 4 MiB limit
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}

	var anthReq MessagesRequest
	if err := json.Unmarshal(body, &anthReq); err != nil {
		h.logger.Warn().Err(err).Int("body_bytes", len(body)).Str("body_head", truncate(string(body), 300)).Msg("proxy: malformed request")
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "malformed Anthropic request: "+err.Error())
		return
	}
	if anthReq.MaxTokens <= 0 {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "max_tokens is required and must be positive")
		return
	}
	if len(anthReq.Messages) == 0 {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty")
		return
	}

	oaiReq, err := ToOpenAI(anthReq, h.upstream.Model)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "translation to OpenAI format failed: "+err.Error())
		return
	}

	reqBody, err := h.marshalWithExtraBody(oaiReq)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to marshal upstream request")
		h.writeError(w, http.StatusInternalServerError, "api_error", "internal marshalling error")
		return
	}

	upstreamURL := h.upstream.URL + "/chat/completions"
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to build upstream request")
		h.writeError(w, http.StatusInternalServerError, "api_error", "internal error building upstream request")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	if h.upstream.APIKey != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+h.upstream.APIKey)
	}

	resp, err := h.client.Do(upstreamReq)
	if err != nil {
		h.logger.Error().Err(err).Str("url", upstreamURL).Msg("upstream request failed")
		h.writeError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB limit
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to read upstream response")
		h.writeError(w, http.StatusBadGateway, "api_error", "failed to read upstream response")
		return
	}

	if resp.StatusCode != http.StatusOK {
		h.logger.Error().Int("status", resp.StatusCode).Str("body", truncate(string(respBody), 200)).Msg("upstream error")
		h.writeError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("upstream returned status %d", resp.StatusCode))
		return
	}
	h.logger.Info().Int("body_bytes", len(respBody)).Bool("client_stream", anthReq.Stream).Int("msgs", len(anthReq.Messages)).Int("tools", len(anthReq.Tools)).Msg("proxy upstream ok")

	var oaiResp ChatResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		h.logger.Error().Err(err).Msg("failed to parse upstream response")
		h.writeError(w, http.StatusBadGateway, "api_error", "failed to parse upstream response")
		return
	}

	anthResp, err := ToAnthropic(oaiResp, h.upstream.Model)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to translate upstream response to Anthropic format")
		h.writeError(w, http.StatusBadGateway, "api_error", "response translation failed: "+err.Error())
		return
	}

	if anthReq.Stream {
		h.writeStreamingResponse(w, anthResp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(anthResp)
}

// writeStreamingResponse emits a fully-formed Anthropic SSE event sequence
// derived from a single non-streaming upstream response. The client sees the
// whole response arrive as one burst of events rather than token-by-token, but
// the event shape and ordering match what clients (claude CLI) expect, so they
// parse without error. Upstream streaming is not yet piped through, this is a
// minimally viable implementation that unblocks real claude CLI usage.
func (h *Handler) writeStreamingResponse(w http.ResponseWriter, resp MessagesResponse) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	emit := func(eventType string, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			h.logger.Error().Err(err).Str("event", eventType).Msg("failed to marshal streaming event")
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, raw)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            resp.ID,
			"type":          "message",
			"role":          "assistant",
			"model":         resp.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  resp.Usage.InputTokens,
				"output_tokens": 0,
			},
		},
	})

	for i, block := range resp.Content {
		switch block.Type {
		case "text":
			emit("content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         i,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": i,
				"delta": map[string]any{"type": "text_delta", "text": block.Text},
			})
			emit("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": i,
			})
		case "tool_use":
			emit("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": i,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    block.ID,
					"name":  block.Name,
					"input": map[string]any{},
				},
			})
			partial := string(block.Input)
			if partial == "" {
				partial = "{}"
			}
			emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
			})
			emit("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": i,
			})
		}
	}

	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp.StopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": resp.Usage.OutputTokens},
	})

	emit("message_stop", map[string]any{"type": "message_stop"})
}

// ModelsHandler returns a minimal OpenAI-style /v1/models listing so clients
// that pre-validate their configuration before sending a real request (e.g.
// the claude CLI in --bare mode) don't fail with "Invalid API key" on startup.
// Only the single model the proxy is configured to forward to is listed.
func (h *Handler) ModelsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": h.upstream.Model, "object": "model", "owned_by": "proxy"},
		},
	})
}

// marshalWithExtraBody marshals req to JSON and then merges any extra_body
// fields from the upstream config into the top-level object.
func (h *Handler) marshalWithExtraBody(req ChatRequest) ([]byte, error) {
	if len(h.upstream.ExtraBody) == 0 {
		return json.Marshal(req)
	}
	// Round-trip through map[string]any so we can inject extra_body fields.
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	maps.Copy(m, h.upstream.ExtraBody)
	return json.Marshal(m)
}

// writeError writes an Anthropic-shaped error response.
func (h *Handler) writeError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(AnthropicError{
		Type: "error",
		Error: AnthropicErrorInner{
			Type:    errType,
			Message: msg,
		},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
