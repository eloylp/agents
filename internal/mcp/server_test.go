package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerRoutesInitialize exercises the streamable-http transport to
// confirm the MCP endpoint is reachable and the tool list includes every
// tool this PR registers. The exact JSON-RPC wire format is tested by the
// upstream mcp-go library; we only assert the pieces that prove our tools
// are wired correctly.
func TestHandlerInitializeAndListTools(t *testing.T) {
	t.Parallel()

	deps := testFixture(t)
	h := New(deps)

	// Step 1: initialize. Streamable-http requires an initialize handshake
	// before tools/list will return results.
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
	rec := postMCP(t, h, initReq, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	session := rec.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatalf("initialize did not return a session id; headers: %+v", rec.Header())
	}

	// Per the MCP spec, clients must send notifications/initialized after the
	// initialize response before other requests. Without this, tools/list
	// responses are rejected with "session not initialized".
	notif := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	notifRec := postMCP(t, h, notif, session)
	if notifRec.Code != http.StatusOK && notifRec.Code != http.StatusAccepted {
		t.Fatalf("initialized notification: unexpected status %d: %s", notifRec.Code, notifRec.Body.String())
	}

	// Step 2: tools/list should enumerate the tools we registered.
	listReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	rec = postMCP(t, h, listReq, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := extractJSONRPC(t, rec)
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal tools/list body %q: %v", body, err)
	}
	got := make(map[string]bool, len(resp.Result.Tools))
	for _, tl := range resp.Result.Tools {
		got[tl.Name] = true
	}
	want := []string{
		"list_agents",
		"get_agent",
		"list_skills",
		"get_skill",
		"list_backends",
		"get_backend",
		"list_repos",
		"get_repo",
		"get_status",
		"trigger_agent",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tools/list missing tool %q (got %+v)", name, got)
		}
	}
}

// postMCP sends a JSON-RPC body to the MCP handler and returns the
// ResponseRecorder so callers can assert on status code and body.
func postMCP(t *testing.T, h http.Handler, body, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	// Streamable-http requires the client to accept both JSON and SSE media
	// types on POST, it chooses which to return based on whether the tool
	// streams incremental results.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// extractJSONRPC returns the JSON-RPC response payload from an MCP response,
// normalising SSE frames (data: <json>\n\n) into a plain body so tests can
// unmarshal without caring about the transport flavour.
func extractJSONRPC(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	body := rec.Body.Bytes()
	if bytes.HasPrefix(body, []byte("data:")) {
		// Minimal SSE extraction: find the first "data:" line payload.
		for _, line := range bytes.Split(body, []byte("\n")) {
			if payload, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				return bytes.TrimSpace(payload)
			}
		}
	}
	return body
}
