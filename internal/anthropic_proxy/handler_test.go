package anthropicproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fakeUpstream returns an httptest.Server that responds with the given status
// and body. It also captures the last request body and headers for assertions.
type fakeUpstream struct {
	*httptest.Server
	status  int
	body    string
	lastReq *http.Request
	lastBody []byte
}

func newFakeUpstream(status int, body string) *fakeUpstream {
	f := &fakeUpstream{status: status, body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastReq = r
		f.lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return f
}

func newHandler(upstream *fakeUpstream, extraBody map[string]any) *Handler {
	return NewHandler(UpstreamConfig{
		URL:       upstream.URL,
		Model:     "test-model",
		Timeout:   5 * time.Second,
		ExtraBody: extraBody,
	}, zerolog.Nop())
}

const oaiTextResp = `{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "model": "test-model",
  "choices": [{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}
}`

func TestHandler_TextRequest(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusOK, oaiTextResp)
	defer up.Close()

	h := newHandler(up, nil)
	body := `{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":"Hello!"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp MessagesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want %q", resp.StopReason, "end_turn")
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Hello!" {
		t.Errorf("content: got %+v", resp.Content)
	}
}

func TestHandler_TranslationApplied(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusOK, oaiTextResp)
	defer up.Close()

	h := newHandler(up, nil)
	body := `{
		"model":"claude","max_tokens":50,
		"system":"You are a bot.",
		"messages":[{"role":"user","content":"Hi"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}

	// Verify the upstream received the translated OpenAI request with system message.
	var sentReq ChatRequest
	if err := json.Unmarshal(up.lastBody, &sentReq); err != nil {
		t.Fatalf("parse upstream body: %v", err)
	}
	if len(sentReq.Messages) != 2 {
		t.Fatalf("upstream messages count: got %d, want 2", len(sentReq.Messages))
	}
	if sentReq.Messages[0].Role != "system" || sentReq.Messages[0].Content != "You are a bot." {
		t.Errorf("upstream[0]: got %+v", sentReq.Messages[0])
	}
	if sentReq.Model != "test-model" {
		t.Errorf("upstream model: got %q, want %q", sentReq.Model, "test-model")
	}
}

func TestHandler_ExtraBodyInjected(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusOK, oaiTextResp)
	defer up.Close()

	extra := map[string]any{
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	h := newHandler(up, extra)

	body := `{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}

	var m map[string]any
	if err := json.Unmarshal(up.lastBody, &m); err != nil {
		t.Fatalf("parse upstream body: %v", err)
	}
	if _, ok := m["chat_template_kwargs"]; !ok {
		t.Error("extra_body field 'chat_template_kwargs' not found in upstream request")
	}
}

func TestHandler_Upstream500ReturnsBadGateway(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusInternalServerError, `{"error":"server error"}`)
	defer up.Close()

	h := newHandler(up, nil)
	body := `{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status: got %d, want 502", w.Code)
	}
	var errResp AnthropicError
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Type != "error" {
		t.Errorf("error type: got %q, want %q", errResp.Type, "error")
	}
}

func TestHandler_MalformedRequest(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusOK, oaiTextResp)
	defer up.Close()

	h := newHandler(up, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`not json`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	var errResp AnthropicError
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("error inner type: got %q, want %q", errResp.Error.Type, "invalid_request_error")
	}
}

func TestHandler_MissingMaxTokensReturns400(t *testing.T) {
	t.Parallel()
	up := newFakeUpstream(http.StatusOK, oaiTextResp)
	defer up.Close()

	h := newHandler(up, nil)
	// max_tokens omitted (zero value)
	body := `{"model":"claude","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}
