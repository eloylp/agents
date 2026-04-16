package anthropicproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(anthResp)
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
	for k, v := range h.upstream.ExtraBody {
		m[k] = v
	}
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
