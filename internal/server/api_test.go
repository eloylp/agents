package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/server"
	serverobserve "github.com/eloylp/agents/internal/server/observe"
	"github.com/eloylp/agents/internal/store"
)

// ── /api/agents ────────────────────────────────────────────────────────────

func TestHandleAPIAgentsReturnsConfiguredAgents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{
			{
				Name:          "reviewer",
				Backend:       "claude",
				Skills:        []string{"testing"},
				Description:   "Reviews PRs",
				AllowDispatch: true,
				CanDispatch:   []string{"sec-reviewer"},
			},
		}
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "reviewer", Labels: []string{"review-me"}},
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}

	got := agents[0]
	if got.Name != "reviewer" {
		t.Errorf("name: want %q, got %q", "reviewer", got.Name)
	}
	if got.Backend != "claude" {
		t.Errorf("backend: want %q, got %q", "claude", got.Backend)
	}
	if !got.AllowDispatch {
		t.Error("want allow_dispatch=true")
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Repo != "owner/repo" {
		t.Errorf("bindings: want 1 entry for owner/repo, got %+v", got.Bindings)
	}
	if len(got.Bindings[0].Labels) != 1 || got.Bindings[0].Labels[0] != "review-me" {
		t.Errorf("binding labels: want [review-me], got %v", got.Bindings[0].Labels)
	}
}

func TestHandleAPIAgentsAttachesScheduleForCronBindings(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(time.Hour)

	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "codex"}}
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "worker", Cron: "0 * * * *"}},
			},
		}
	})
	provider := &stubStatusProvider{statuses: []scheduler.AgentStatus{
		{Name: "worker", Repo: "owner/repo", LastRun: &now, NextRun: next, LastStatus: "ok"},
	}}
	srv, _ := newTestServerWithProvider(cfg, provider)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent")
	}
	if len(agents[0].Bindings) != 1 {
		t.Fatalf("want 1 binding, got %d", len(agents[0].Bindings))
	}
	sched := agents[0].Bindings[0].Schedule
	if sched == nil {
		t.Fatal("want schedule on cron binding, got nil")
	}
	if sched.LastRun == nil {
		t.Error("want last_run set")
	}
	if sched.LastStatus != "ok" {
		t.Errorf("last_status: want %q, got %q", "ok", sched.LastStatus)
	}
}

// TestHandleAPIAgentsMultiRepoSchedulePreserved verifies that an agent with
// cron bindings in two different repos carries independent schedule state on
// each binding — not just the last repo visited in the loop.
func TestHandleAPIAgentsMultiRepoSchedulePreserved(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	next1 := now.Add(time.Hour)
	next2 := now.Add(2 * time.Hour)

	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "codex"}}
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/repo-a",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "worker", Cron: "0 * * * *"}},
			},
			{
				Name:    "owner/repo-b",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "worker", Cron: "30 * * * *"}},
			},
		}
	})
	provider := &stubStatusProvider{statuses: []scheduler.AgentStatus{
		{Name: "worker", Repo: "owner/repo-a", LastRun: &now, NextRun: next1, LastStatus: "ok"},
		{Name: "worker", Repo: "owner/repo-b", NextRun: next2, LastStatus: "pending"},
	}}
	srv, _ := newTestServerWithProvider(cfg, provider)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	if len(agents[0].Bindings) != 2 {
		t.Fatalf("want 2 bindings, got %d", len(agents[0].Bindings))
	}

	// Build a map by repo name for stable assertions regardless of loop order.
	byRepo := make(map[string]viewBindingJSON, 2)
	for _, b := range agents[0].Bindings {
		byRepo[b.Repo] = b
	}

	repoA, ok := byRepo["owner/repo-a"]
	if !ok {
		t.Fatal("missing binding for owner/repo-a")
	}
	if repoA.Schedule == nil {
		t.Fatal("want schedule on repo-a binding, got nil")
	}
	if repoA.Schedule.LastRun == nil {
		t.Error("repo-a: want last_run set")
	}
	if repoA.Schedule.LastStatus != "ok" {
		t.Errorf("repo-a last_status: want %q, got %q", "ok", repoA.Schedule.LastStatus)
	}

	repoB, ok := byRepo["owner/repo-b"]
	if !ok {
		t.Fatal("missing binding for owner/repo-b")
	}
	if repoB.Schedule == nil {
		t.Fatal("want schedule on repo-b binding, got nil")
	}
	if repoB.Schedule.LastRun != nil {
		t.Error("repo-b: want last_run nil (no run yet)")
	}
	if repoB.Schedule.LastStatus != "pending" {
		t.Errorf("repo-b last_status: want %q, got %q", "pending", repoB.Schedule.LastStatus)
	}
}

// TestHandleAPIAgentsIncludesDisabledRepoBindings verifies that bindings on
// disabled repos appear in the /agents fleet snapshot with repo_enabled=false.
// The wire view is for inspection (memory page, audit, MCP), not runtime
// dispatch — hiding disabled-repo bindings here also hides the agent's
// memory in the dashboard. The runtime entry points (POST /run, webhook,
// engine.HandleEvent) refuse disabled repos at the boundary; the wire view
// trusts consumers to filter.
func TestHandleAPIAgentsIncludesDisabledRepoBindings(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "claude"}}
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/active-repo",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
			{
				Name:    "owner/disabled-repo",
				Enabled: false,
				Use:     []fleet.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	bindings := agents[0].Bindings
	if len(bindings) != 2 {
		t.Fatalf("want 2 bindings (both repos), got %d: %+v", len(bindings), bindings)
	}
	byRepo := map[string]viewBindingJSON{}
	for _, b := range bindings {
		byRepo[b.Repo] = b
	}
	active, ok := byRepo["owner/active-repo"]
	if !ok {
		t.Fatal("missing binding for owner/active-repo")
	}
	if !active.RepoEnabled {
		t.Errorf("owner/active-repo: want repo_enabled=true, got false")
	}
	disabled, ok := byRepo["owner/disabled-repo"]
	if !ok {
		t.Fatal("missing binding for owner/disabled-repo")
	}
	if disabled.RepoEnabled {
		t.Errorf("owner/disabled-repo: want repo_enabled=false, got true")
	}
}

func TestHandleAPIAgentsEmptyWhenNoAgents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	cfg.Agents = nil
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("want empty slice, got %d entries", len(agents))
	}
}

func TestHandleAPIAgentsCurrentStatusIdleWhenNotRunning(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range agents {
		if a.CurrentStatus != "idle" {
			t.Errorf("agent %q: want current_status=idle, got %q", a.Name, a.CurrentStatus)
		}
	}
}

// stubRuntimeState is a minimal RuntimeStateProvider for tests.
type stubRuntimeState struct{ running map[string]bool }

func (s *stubRuntimeState) IsRunning(name string) bool { return s.running[name] }

func TestHandleAPIAgentsCurrentStatusRunningWhenActive(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "coder", Backend: "claude"}}
	})
	rs := &stubRuntimeState{running: map[string]bool{"coder": true}}
	srv, _ := newTestServerWithRuntimeState(cfg, nil, rs)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []viewAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "coder" {
			if a.CurrentStatus != "running" {
				t.Errorf("want current_status=running for active agent, got %q", a.CurrentStatus)
			}
			found = true
		} else if a.CurrentStatus != "idle" {
			t.Errorf("agent %q: want current_status=idle, got %q", a.Name, a.CurrentStatus)
		}
	}
	if !found {
		t.Error("agent 'coder' not found in response")
	}
}

// ── /api/config ────────────────────────────────────────────────────────────

func TestHandleAPIConfigRedactsSecrets(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WebhookSecret = "super-secret-webhook"
		c.Daemon.HTTP.WebhookSecretEnv = "GITHUB_WEBHOOK_SECRET"
		c.Daemon.Proxy = config.ProxyConfig{
			Enabled: true,
			Upstream: config.ProxyUpstreamConfig{
				APIKey:    "proxy-secret",
				APIKeyEnv: "PROXY_API_KEY",
			},
		}
		c.Daemon.AIBackends = map[string]fleet.Backend{
			"claude": {
				Command: "claude",
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Actual secret values must not appear anywhere in the response.
	secrets := []string{"super-secret-webhook", "proxy-secret", "sk-ant-secret"}
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("secret %q must be redacted but appears in response", s)
		}
	}

	// The "[redacted]" sentinel and env var names must be present.
	if !strings.Contains(body, "[redacted]") {
		t.Error("want at least one [redacted] marker in response")
	}
	if !strings.Contains(body, "GITHUB_WEBHOOK_SECRET") {
		t.Error("env var name GITHUB_WEBHOOK_SECRET must be preserved")
	}
}

// TestHandleAPIConfigOmitsProxyExtraBody verifies that proxy.upstream.extra_body
// never appears in the /api/config response.
func TestHandleAPIConfigOmitsProxyExtraBody(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.Proxy = config.ProxyConfig{
			Enabled: true,
			Upstream: config.ProxyUpstreamConfig{
				URL:   "https://upstream.example.com",
				Model: "gpt-4o",
				ExtraBody: map[string]any{
					"authorization": "Bearer secret-token-xyz",
					"x-api-key":     "vendor-secret",
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	if strings.Contains(body, "extra_body") {
		t.Error("extra_body key must be omitted from /api/config response")
	}
	for _, secret := range []string{"secret-token-xyz", "vendor-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("extra_body secret value %q must not appear in response", secret)
		}
	}
}

func TestHandleAPIConfigNoSecretsWhenNotSet(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WebhookSecret = ""
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "[redacted]") {
		t.Errorf("[redacted] must not appear when no secrets are set: %s", body)
	}
}

func TestHandleAPIConfigContentType(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(testCfg(nil))
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
}

// TestHandleAPIConfigRepoBindingDefaultEnabled asserts the exact JSON shape
// for a repo binding whose enabled field is omitted in config.
func TestHandleAPIConfigRepoBindingDefaultEnabled(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "worker", Labels: []string{"triage"}},
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	raw := rec.Body.String()

	var resp struct {
		Repos []struct {
			Name     string `json:"name"`
			Enabled  bool   `json:"enabled"`
			Bindings []struct {
				Agent   string   `json:"agent"`
				Labels  []string `json:"labels"`
				Enabled bool     `json:"enabled"`
			} `json:"bindings"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Repos) != 1 {
		t.Fatalf("want 1 repo, got %d", len(resp.Repos))
	}
	repo := resp.Repos[0]
	if repo.Name != "owner/repo" {
		t.Errorf("repos[0].name: want %q, got %q", "owner/repo", repo.Name)
	}
	if !repo.Enabled {
		t.Error("repos[0].enabled: want true, got false")
	}
	if len(repo.Bindings) != 1 {
		t.Fatalf("repos[0].bindings: want 1 binding, got %d", len(repo.Bindings))
	}
	b := repo.Bindings[0]
	if b.Agent != "worker" {
		t.Errorf("binding.agent: want %q, got %q", "worker", b.Agent)
	}
	if !b.Enabled {
		t.Error("binding.enabled: want true for nil *bool (default-enabled), got false")
	}
	if len(b.Labels) != 1 || b.Labels[0] != "triage" {
		t.Errorf("binding.labels: want [triage], got %v", b.Labels)
	}

	for _, badKey := range []string{`"Name"`, `"Enabled"`, `"Bindings"`, `"Use"`, `"Agent"`, `"Labels"`} {
		if strings.Contains(raw, badKey) {
			t.Errorf("response must not contain PascalCase key %s; got body: %s", badKey, raw)
		}
	}
}

// ── SSE integration through buildHandler ───────────────────────────────────

// TestBuildHandlerSSETimeoutSplit verifies the router-level write-timeout split:
// SSE stream routes (/api/*/stream) must NOT be wrapped with http.TimeoutHandler,
// while non-SSE routes ARE wrapped. Regression test for issue #173.
func TestBuildHandlerSSETimeoutSplit(t *testing.T) {
	// Not parallel: this test intentionally sleeps 1.2 s to cross the timeout boundary.

	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WriteTimeoutSeconds = 1
	})
	obs := newTestObserve(t)
	srv, _ := newTestServer(cfg)
	wireObserveForTest(srv, obs)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/events/stream") //nolint:noctx
	if err != nil {
		t.Fatalf("connect to /api/events/stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE route Content-Type: want text/event-stream, got %q (TimeoutHandler sends text/plain on timeout)", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	lineCh := make(chan string, 16)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	select {
	case line := <-lineCh:
		if line != ": connected" {
			t.Fatalf("SSE: expected ': connected', got %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE connected comment")
	}

	time.Sleep(1200 * time.Millisecond)

	obs.EventsSSE.Publish([]byte("data: post-timeout\n\n"))

	deadline := time.After(500 * time.Millisecond)
	var lines []string
receiveLoop:
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("SSE stream was closed after write-timeout boundary; received lines: %v", lines)
			}
			lines = append(lines, line)
			if strings.Contains(strings.Join(lines, "\n"), "post-timeout") {
				break receiveLoop
			}
		case <-deadline:
			t.Fatalf("SSE stream did not deliver event after write-timeout boundary (stream killed by TimeoutHandler?); lines: %v", lines)
		}
	}

	r, err := http.Get(ts.URL + "/events") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("/api/events: want 200, got %d", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("/api/events Content-Type: want application/json, got %q", ct)
	}
}

// TestServeSSEClearsServerWriteDeadline verifies that the SSE helper called
// from buildHandler clears the server-level write deadline so SSE streams
// are not killed by http.Server.WriteTimeout. Tests the underlying mechanism
// from the buildHandler boundary.
func TestServeSSEClearsServerWriteDeadline(t *testing.T) {
	// Not parallel: test intentionally sleeps to cross a write-deadline boundary.

	obs := newTestObserve(t)
	srv, _ := newTestServer(testCfg(nil))
	wireObserveForTest(srv, obs)

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.WriteTimeout = 200 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/events/stream") //nolint:noctx
	if err != nil {
		t.Fatalf("connect to /api/events/stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: want text/event-stream, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	lineCh := make(chan string, 16)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	select {
	case line := <-lineCh:
		if line != ": connected" {
			t.Fatalf("first SSE comment: want ': connected', got %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE connected comment")
	}

	time.Sleep(400 * time.Millisecond)

	obs.EventsSSE.Publish([]byte("data: after-deadline\n\n"))

	deadline := time.After(500 * time.Millisecond)
	var lines []string
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("SSE stream was closed after write-deadline boundary (SetWriteDeadline not clearing server timeout?); lines so far: %v", lines)
			}
			lines = append(lines, line)
			if strings.Contains(strings.Join(lines, "\n"), "after-deadline") {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for post-deadline event; lines so far: %v", lines)
		}
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

type stubStatusProvider struct {
	statuses []scheduler.AgentStatus
}

func (p *stubStatusProvider) AgentStatuses() []scheduler.AgentStatus { return p.statuses }

// newTestObserve creates an observe.Store backed by a temporary SQLite DB.
// Used by the integration tests for buildHandler routing through the SSE
// surface.
func newTestObserve(t *testing.T) *observe.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return observe.NewStore(db)
}

// wireObserveForTest mounts the observability routes on srv. The observe
// handler construction lives outside this package now (cmd/agents wires it
// via WithObserveRegister); tests do the same so the routes are reachable
// when integration tests exercise the full router.
func wireObserveForTest(srv *server.Server, obs *observe.Store) {
	srv.WithObserve(obs)
	srv.WithObserveRegister(func(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
		obh := serverobserve.New(obs, srv, nil, nil, nil, nil)
		obh.RegisterRoutes(r, withTimeout)
	})
}
