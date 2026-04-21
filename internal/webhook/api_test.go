package webhook

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// ── /api/agents ────────────────────────────────────────────────────────────

func TestHandleAPIAgentsReturnsConfiguredAgents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{
				Name:          "reviewer",
				Backend:       "claude",
				Skills:        []string{"testing"},
				Description:   "Reviews PRs",
				AllowDispatch: true,
				CanDispatch:   []string{"sec-reviewer"},
			},
		}
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "reviewer", Labels: []string{"review-me"}},
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var agents []apiAgentJSON
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
		c.Agents = []config.AgentDef{{Name: "worker", Backend: "codex"}}
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Cron: "0 * * * *"}},
			},
		}
	})
	dc := workflow.NewDataChannels(1)
	provider := &stubStatusProvider{statuses: []AgentStatus{
		{Name: "worker", Repo: "owner/repo", LastRun: &now, NextRun: next, LastStatus: "ok"},
	}}
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, provider, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	var agents []apiAgentJSON
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
		c.Agents = []config.AgentDef{{Name: "worker", Backend: "codex"}}
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/repo-a",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Cron: "0 * * * *"}},
			},
			{
				Name:    "owner/repo-b",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Cron: "30 * * * *"}},
			},
		}
	})
	dc := workflow.NewDataChannels(1)
	provider := &stubStatusProvider{statuses: []AgentStatus{
		{Name: "worker", Repo: "owner/repo-a", LastRun: &now, NextRun: next1, LastStatus: "ok"},
		{Name: "worker", Repo: "owner/repo-b", NextRun: next2, LastStatus: "pending"},
	}}
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, provider, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	var agents []apiAgentJSON
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
	byRepo := make(map[string]agentBindingJSON, 2)
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

// TestHandleAPIAgentsSkipsDisabledRepos verifies that repos with enabled:false
// do not appear in the /api/agents fleet snapshot. A disabled repo is ignored
// by the runtime, so its bindings must not mislead operators by appearing as
// active bindings in the fleet view.
func TestHandleAPIAgentsSkipsDisabledRepos(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{{Name: "worker", Backend: "claude"}}
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/active-repo",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
			{
				Name:    "owner/disabled-repo",
				Enabled: false,
				Use:     []config.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []apiAgentJSON
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	bindings := agents[0].Bindings
	if len(bindings) != 1 {
		t.Fatalf("want exactly 1 binding (active repo only), got %d: %+v", len(bindings), bindings)
	}
	if bindings[0].Repo != "owner/active-repo" {
		t.Errorf("want binding for owner/active-repo, got %q", bindings[0].Repo)
	}
}

func TestHandleAPIAgentsEmptyWhenNoAgents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	cfg.Agents = nil
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []apiAgentJSON
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
	srv.handleAPIAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []apiAgentJSON
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
		c.Agents = []config.AgentDef{{Name: "coder", Backend: "claude"}}
	})
	srv, _ := newTestServer(cfg)
	srv.WithRuntimeState(&stubRuntimeState{running: map[string]bool{"coder": true}})

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var agents []apiAgentJSON
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
		c.Daemon.AIBackends = map[string]config.AIBackendConfig{
			"claude": {
				Command: "claude",
				Env:     map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret"},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Actual secret values must not appear anywhere in the response.
	secrets := []string{"super-secret-webhook", "proxy-secret", "sk-ant-secret"}
	for _, s := range secrets {
		if strings.Contains(body,s) {
			t.Errorf("secret %q must be redacted but appears in response", s)
		}
	}

	// The "[redacted]" sentinel and env var names must be present.
	if !strings.Contains(body,redacted) {
		t.Error("want at least one [redacted] marker in response")
	}
	if !strings.Contains(body,"GITHUB_WEBHOOK_SECRET") {
		t.Error("env var name GITHUB_WEBHOOK_SECRET must be preserved")
	}
	// Backend env key must appear, but value must be redacted.
	if !strings.Contains(body,"ANTHROPIC_API_KEY") {
		t.Error("backend env key ANTHROPIC_API_KEY must be preserved")
	}
}

// TestHandleAPIConfigOmitsProxyExtraBody verifies that proxy.upstream.extra_body
// never appears in the /api/config response, regardless of what values the
// operator has set there. The field is a free-form map that can hold bearer
// tokens or other vendor-specific auth credentials.
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
					"x-api-key":    "vendor-secret",
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// The field name itself must not appear.
	if strings.Contains(body, "extra_body") {
		t.Error("extra_body key must be omitted from /api/config response")
	}
	// Neither value must leak.
	for _, secret := range []string{"secret-token-xyz", "vendor-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("extra_body secret value %q must not appear in response", secret)
		}
	}
}

func TestHandleAPIConfigNoSecretsWhenNotSet(t *testing.T) {
	t.Parallel()
	// Minimal config: secrets are empty strings — [redacted] must NOT appear.
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WebhookSecret = ""
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	body := rec.Body.String()
	if strings.Contains(body,redacted) {
		t.Errorf("[redacted] must not appear when no secrets are set: %s", body)
	}
}

func TestHandleAPIConfigContentType(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(testCfg(nil))
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
}

// TestHandleAPIConfigRepoBindingDefaultEnabled asserts the exact JSON shape for
// a repo binding whose enabled field is omitted in config (nil *bool). The
// effective value must be true — not null — and all keys must use snake_case
// json tags rather than the Go struct's YAML-tag casing.
func TestHandleAPIConfigRepoBindingDefaultEnabled(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					// enabled deliberately omitted — nil *bool means "default on"
					{Agent: "worker", Labels: []string{"triage"}},
				},
			},
		}
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	// Capture raw body for key-shape checks before decoding consumes it.
	raw := rec.Body.String()

	var resp struct {
		Repos []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			Use     []struct {
				Agent   string   `json:"agent"`
				Labels  []string `json:"labels"`
				Enabled bool     `json:"enabled"`
			} `json:"use"`
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
	if len(repo.Use) != 1 {
		t.Fatalf("repos[0].use: want 1 binding, got %d", len(repo.Use))
	}
	b := repo.Use[0]
	if b.Agent != "worker" {
		t.Errorf("binding.agent: want %q, got %q", "worker", b.Agent)
	}
	// nil *bool in config must normalize to true — not null.
	if !b.Enabled {
		t.Error("binding.enabled: want true for nil *bool (default-enabled), got false")
	}
	if len(b.Labels) != 1 || b.Labels[0] != "triage" {
		t.Errorf("binding.labels: want [triage], got %v", b.Labels)
	}

	// Verify raw JSON uses snake_case keys, not PascalCase from YAML tags.
	for _, badKey := range []string{`"Name"`, `"Enabled"`, `"Use"`, `"Agent"`, `"Labels"`} {
		if strings.Contains(raw, badKey) {
			t.Errorf("response must not contain PascalCase key %s; got body: %s", badKey, raw)
		}
	}
}

// ── /api/dispatches ────────────────────────────────────────────────────────

func TestHandleAPIDispatchesDelegatesToProvider(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	dc := workflow.NewDataChannels(1)
	want := workflow.DispatchStats{
		RequestedTotal: 5,
		Enqueued:       3,
		Deduped:        2,
	}
	provider := &stubDispatchProvider{stats: want}
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, nil, provider, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/dispatches", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIDispatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got workflow.DispatchStats
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("stats: want %+v, got %+v", want, got)
	}
}

func TestHandleAPIDispatchesZeroWhenNoProvider(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(testCfg(nil)) // dispatchStats is nil

	req := httptest.NewRequest(http.MethodGet, "/dispatches", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIDispatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got workflow.DispatchStats
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (workflow.DispatchStats{}) {
		t.Errorf("want zero stats, got %+v", got)
	}
}

// ── /api/events ────────────────────────────────────────────────────────────

func TestHandleAPIEventsReturnsStoredEvents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	now := time.Now().UTC()
	obs.RecordEvent(now, workflow.Event{ID: "evt-1", Kind: "issues.labeled", Repo: workflow.RepoRef{FullName: "owner/repo"}, Number: 42, Actor: "user"})
	obs.RecordEvent(now.Add(time.Second), workflow.Event{ID: "evt-2", Kind: "push", Repo: workflow.RepoRef{FullName: "owner/repo"}, Actor: "bot"})
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var events []apiEventJSON
	if err := json.NewDecoder(rec.Body).Decode(&events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].ID != "evt-1" || events[1].ID != "evt-2" {
		t.Fatalf("unexpected event IDs: %v %v", events[0].ID, events[1].ID)
	}
}

func TestHandleAPIEventsSinceFilter(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	base := time.Now().UTC()
	obs.RecordEvent(base, workflow.Event{ID: "old", Kind: "push"})
	obs.RecordEvent(base.Add(2*time.Second), workflow.Event{ID: "new", Kind: "push"})
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	since := base.Add(time.Second).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/events?since="+since, nil)
	rec := httptest.NewRecorder()
	srv.handleAPIEvents(rec, req)

	var events []apiEventJSON
	_ = json.NewDecoder(rec.Body).Decode(&events)
	if len(events) != 1 || events[0].ID != "new" {
		t.Fatalf("want only 'new' event after filter, got %v", events)
	}
}

// ── SSE stream handlers ─────────────────────────────────────────────────────

// TestHandleSSEStreams verifies the three SSE stream handlers
// (events/stream, traces/stream, memory/stream) using a table-driven approach.
//
// Each sub-test:
//  1. Connects a subscriber (sseCapture) via the handler goroutine.
//  2. Reads the immediate ": connected\n\n" heartbeat and checks headers.
//  3. Publishes one message to the hub and verifies it arrives at the client.
//  4. Cancels the request context to stop the handler cleanly.
func TestHandleSSEStreams(t *testing.T) {
	t.Parallel()

	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		publish func(msg []byte)
		msg     string
	}{
		{
			name:    "events/stream",
			handler: srv.handleAPIEventsStream,
			publish: obs.EventsSSE.Publish,
			msg:     `data: {"id":"ev1"}` + "\n\n",
		},
		{
			name:    "traces/stream",
			handler: srv.handleAPITracesStream,
			publish: obs.TracesSSE.Publish,
			msg:     `data: {"span_id":"sp1"}` + "\n\n",
		},
		{
			name:    "memory/stream",
			handler: srv.handleAPIMemoryStream,
			publish: obs.MemorySSE.Publish,
			msg:     `data: {"agent":"coder","repo":"owner_repo"}` + "\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cap := newSSECapture()
			req := httptest.NewRequest(http.MethodGet, "/"+tc.name, nil).WithContext(ctx)

			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.handler(cap, req)
			}()

			// The handler sends ": connected\n\n" before entering the select
			// loop, so it must arrive before any published message.
			heartbeat := mustReadSSEMsg(t, cap.writes, 2*time.Second)
			if heartbeat != ": connected\n\n" {
				t.Fatalf("want heartbeat %q, got %q", ": connected\n\n", heartbeat)
			}

			// Verify SSE-required headers were set before the first write.
			if got := cap.Header().Get("Content-Type"); got != "text/event-stream" {
				t.Errorf("Content-Type: want %q, got %q", "text/event-stream", got)
			}
			if got := cap.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control: want %q, got %q", "no-cache", got)
			}

			// Publish a message and confirm the handler fans it out.
			tc.publish([]byte(tc.msg))
			got := mustReadSSEMsg(t, cap.writes, 2*time.Second)
			if got != tc.msg {
				t.Errorf("published %q but received %q", tc.msg, got)
			}

			// Cancel the context; the handler must exit promptly.
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("handler did not exit after context cancellation")
			}
		})
	}
}

// ── /api/traces ────────────────────────────────────────────────────────────

func TestHandleAPITracesReturnsStoredSpans(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	now := time.Now().UTC()
	obs.RecordSpan("s1", "root-A", "", "coder", "claude", "owner/repo", "issues.labeled", "", 1, 0, 0, 0, "", now, now.Add(5*time.Second), "success", "")
	obs.RecordSpan("s2", "root-A", "", "reviewer", "claude", "owner/repo", "agent.dispatch", "coder", 1, 1, 0, 0, "", now.Add(time.Second), now.Add(6*time.Second), "success", "")
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	req := httptest.NewRequest(http.MethodGet, "/traces", nil)
	rec := httptest.NewRecorder()
	srv.handleAPITraces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var spans []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&spans); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
}

func TestHandleAPITraceByRootEventID(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	now := time.Now().UTC()
	obs.RecordSpan("s1", "root-A", "", "coder", "claude", "r", "issues.labeled", "", 1, 0, 0, 0, "", now, now.Add(time.Second), "success", "")
	obs.RecordSpan("s2", "root-B", "", "reviewer", "claude", "r", "push", "", 0, 0, 0, 0, "", now, now.Add(time.Second), "success", "")
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	// Use the full router so mux populates the {root_event_id} variable.
	router := srv.buildHandler()
	req := httptest.NewRequest(http.MethodGet, "/traces/root-A", nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var spans []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&spans)
	if len(spans) != 1 {
		t.Fatalf("want 1 span for root-A, got %d", len(spans))
	}
}

func TestHandleAPITraceNotFound(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	router := srv.buildHandler()
	req := httptest.NewRequest(http.MethodGet, "/traces/nonexistent", nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// ── /api/graph ────────────────────────────────────────────────────────────

func TestHandleAPIGraphReturnsEdges(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	obs.RecordDispatch("coder", "reviewer", "owner/repo", 10, "needs review")
	obs.RecordDispatch("coder", "reviewer", "owner/repo", 11, "follow-up")
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g apiGraphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(g.Edges))
	}
	if g.Edges[0].Count != 2 {
		t.Fatalf("want edge count=2, got %d", g.Edges[0].Count)
	}
}

func TestHandleAPIGraphEmptyWhenNoDispatches(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIGraph(rec, req)

	var g apiGraphJSON
	_ = json.NewDecoder(rec.Body).Decode(&g)
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("want empty graph, got %+v", g)
	}
}

func TestHandleAPIGraphIncludesConfiguredAgentWithNoDispatches(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{Name: "solo-agent", Backend: "claude"},
		}
	})
	srv, _ := newTestServer(cfg)
	obs := newTestObserve(t)
	srv.WithObserve(obs)

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g apiGraphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("want 1 node for configured agent, got %d", len(g.Nodes))
	}
	if g.Nodes[0].ID != "solo-agent" {
		t.Fatalf("want node ID %q, got %q", "solo-agent", g.Nodes[0].ID)
	}
	if len(g.Edges) != 0 {
		t.Fatalf("want 0 edges, got %d", len(g.Edges))
	}
}

func TestHandleAPIGraphNodeStatusReflectsRuntimeState(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{Name: "runner", Backend: "claude"},
			{Name: "idle-ok", Backend: "claude"},
			{Name: "idle-err", Backend: "claude"},
		}
	})
	// "idle-err" had a previous error run per the scheduler.
	provider := &stubStatusProvider{statuses: []AgentStatus{
		{Name: "idle-err", LastStatus: "error"},
	}}
	dc := workflow.NewDataChannels(1)
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, provider, nil, zerolog.Nop())
	obs := newTestObserve(t)
	srv.WithObserve(obs)
	// "runner" is currently active.
	srv.WithRuntimeState(&stubRuntimeState{running: map[string]bool{"runner": true}})

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g apiGraphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	statusByID := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		statusByID[n.ID] = n.Status
	}
	if statusByID["runner"] != "running" {
		t.Errorf("running agent: want status=%q, got %q", "running", statusByID["runner"])
	}
	if statusByID["idle-err"] != "error" {
		t.Errorf("error agent: want status=%q, got %q", "error", statusByID["idle-err"])
	}
	if statusByID["idle-ok"] != "" {
		t.Errorf("idle-ok agent: want empty status, got %q", statusByID["idle-ok"])
	}
}

// ── SSE ────────────────────────────────────────────────────────────────────

// TestServeSSEHeartbeatSentPeriodically verifies that serveSSE writes periodic
// ": heartbeat\n\n" SSE comments when no data arrives from the hub. These keep
// the TCP connection alive through intermediate proxies.
func TestServeSSEHeartbeatSentPeriodically(t *testing.T) {
	t.Parallel()

	hub := observe.NewSSEHub(4)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSEWithInterval(w, r, hub, 20*time.Millisecond)
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: want text/event-stream, got %q", ct)
	}

	// Read lines until we see both the initial connected comment and at least
	// one periodic heartbeat comment, or give up after a generous deadline.
	deadline := time.After(500 * time.Millisecond)
	scanner := bufio.NewScanner(resp.Body)
	var seenConnected, seenHeartbeat bool
	lineCh := make(chan string)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()
	for !seenConnected || !seenHeartbeat {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatal("SSE stream closed before heartbeat was received")
			}
			switch line {
			case ": connected":
				seenConnected = true
			case ": heartbeat":
				seenHeartbeat = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for SSE heartbeat (connected=%v heartbeat=%v)", seenConnected, seenHeartbeat)
		}
	}
}

// TestServeSSEDeliversDataMessages verifies that messages published to the hub
// are forwarded to SSE subscribers as "data: ...\n\n" frames.
func TestServeSSEDeliversDataMessages(t *testing.T) {
	t.Parallel()

	hub := observe.NewSSEHub(4)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSEWithInterval(w, r, hub, time.Hour) // suppress heartbeat during this test
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	defer resp.Body.Close()

	// Wait for the initial ": connected\n\n" before publishing so the
	// subscriber channel is ready.
	scanner := bufio.NewScanner(resp.Body)
	lineCh := make(chan string)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()
	select {
	case line := <-lineCh:
		if line != ": connected" {
			t.Fatalf("expected ': connected', got %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for initial connected comment")
	}

	// Publish a message and expect to receive it.
	payload := []byte("data: hello\n\n")
	hub.Publish(payload)

	deadline := time.After(500 * time.Millisecond)
	var received []string
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("stream closed before message was received; lines so far: %v", received)
			}
			received = append(received, line)
			if strings.Contains(strings.Join(received, "\n"), "data: hello") {
				return // success
			}
		case <-deadline:
			t.Fatalf("timed out waiting for data message; lines so far: %v", received)
		}
	}
}

// TestBuildHandlerSSETimeoutSplit verifies the router-level write-timeout split:
// SSE stream routes (/api/*/stream) must NOT be wrapped with http.TimeoutHandler,
// while non-SSE routes ARE wrapped. This is the regression test for issue #173:
// if a future route registration change accidentally re-wraps an SSE endpoint,
// the stream will be killed after WriteTimeoutSeconds and this test will fail.
//
// Approach: configure WriteTimeoutSeconds=1, connect to /api/events/stream through
// buildHandler(), sleep past the 1-second timeout boundary, then publish an event
// and verify the stream is still alive. A TimeoutHandler-wrapped SSE endpoint would
// have its connection closed at the 1-second mark, causing the final receive to fail.
func TestBuildHandlerSSETimeoutSplit(t *testing.T) {
	// Not parallel: this test intentionally sleeps 1.2 s to cross the timeout boundary.

	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WriteTimeoutSeconds = 1 // enable per-handler write timeout
	})
	obs := newTestObserve(t)
	srv, _ := newTestServer(cfg)
	srv.WithObserve(obs)

	ts := httptest.NewServer(srv.buildHandler())
	t.Cleanup(ts.Close)

	// ── SSE route: must survive past the write-timeout boundary ──────────────

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

	// Wait for the initial ": connected" comment so we know the subscriber is ready.
	select {
	case line := <-lineCh:
		if line != ": connected" {
			t.Fatalf("SSE: expected ': connected', got %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE connected comment")
	}

	// Sleep past the 1-second write timeout. If the SSE route were wrapped with
	// http.TimeoutHandler, the server would close the connection here.
	time.Sleep(1200 * time.Millisecond)

	// Publish an event and verify it arrives — proving the stream is still alive.
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
				break receiveLoop // stream survived past the write timeout
			}
		case <-deadline:
			t.Fatalf("SSE stream did not deliver event after write-timeout boundary (stream killed by TimeoutHandler?); lines: %v", lines)
		}
	}

	// ── Non-SSE route: must respond normally with JSON ────────────────────────
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

// TestServeSSEClearsServerWriteDeadline verifies that serveSSEWithInterval
// calls http.ResponseController.SetWriteDeadline to clear the server-level
// write deadline, so SSE streams are not killed by http.Server.WriteTimeout.
// This is the underlying mechanism tested by TestBuildHandlerSSETimeoutSplit at
// the handler-execution layer; here we test it at the TCP write-deadline layer
// using httptest.NewUnstartedServer with an explicit WriteTimeout.
func TestServeSSEClearsServerWriteDeadline(t *testing.T) {
	// Not parallel: test intentionally sleeps to cross a write-deadline boundary.

	obs := newTestObserve(t)
	srv, _ := newTestServer(testCfg(nil))
	srv.WithObserve(obs)

	ts := httptest.NewUnstartedServer(srv.buildHandler())
	ts.Config.WriteTimeout = 200 * time.Millisecond // very short deadline
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

	// Sleep past the 200 ms write deadline. If SetWriteDeadline(time.Time{}) did
	// not clear the server deadline, the connection is closed here and lineCh is
	// closed by the scanner goroutine.
	time.Sleep(400 * time.Millisecond)

	// Publish an event and verify it arrives — stream must still be alive.
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
				return // stream is alive — test passes
			}
		case <-deadline:
			t.Fatalf("timed out waiting for post-deadline event; lines so far: %v", lines)
		}
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

type stubStatusProvider struct {
	statuses []AgentStatus
}

func (p *stubStatusProvider) AgentStatuses() []AgentStatus { return p.statuses }

type stubDispatchProvider struct {
	stats workflow.DispatchStats
}

func (p *stubDispatchProvider) DispatchStats() workflow.DispatchStats { return p.stats }

// newTestObserve creates an observe.Store backed by a temporary SQLite DB.
// It requires a testing.T to manage the temp directory lifetime.
func newTestObserve(t *testing.T) *observe.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return observe.NewStore(db)
}

// sseCapture is a minimal http.ResponseWriter + http.Flusher that forwards
// each Write call to a buffered channel. This lets test goroutines receive SSE
// frames without races on a shared bytes.Buffer: the handler goroutine sends,
// the test goroutine receives.
type sseCapture struct {
	header http.Header
	writes chan []byte
}

func newSSECapture() *sseCapture {
	return &sseCapture{
		header: make(http.Header),
		writes: make(chan []byte, 32),
	}
}

func (c *sseCapture) Header() http.Header { return c.header }
func (c *sseCapture) WriteHeader(_ int)   {}
func (c *sseCapture) Write(b []byte) (int, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	c.writes <- cp
	return len(b), nil
}
func (c *sseCapture) Flush() {} // satisfies http.Flusher

// ── handleAPIMemory SQLite mode ────────────────────────────────────────────

// stubMemoryReader is a MemoryReader that returns a fixed mapping of
// (agent, repo) → content for use in unit tests. Keys present in the map
// but mapping to "" represent existing empty-memory records; absent keys
// represent missing records and cause ErrMemoryNotFound.
// mtimes optionally maps the same key to a last-updated timestamp; a missing
// entry returns time.Time{} (zero), meaning the X-Memory-Mtime header is
// omitted.
type stubMemoryReader struct {
	content map[string]string    // key: "agent\x00repo"; present=exists, absent=not found
	mtimes  map[string]time.Time // optional per-record timestamps
}

func (r *stubMemoryReader) ReadMemory(agent, repo string) (string, time.Time, error) {
	key := agent + "\x00" + repo
	content, ok := r.content[key]
	if !ok {
		return "", time.Time{}, ErrMemoryNotFound
	}
	var mtime time.Time
	if r.mtimes != nil {
		mtime = r.mtimes[key]
	}
	return content, mtime, nil
}

func TestHandleAPIMemorySQLiteMode(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		agent     string
		repo      string
		stored    map[string]string
		mtimes    map[string]time.Time
		wantCode  int
		wantBody  string
		wantMtime string // expected X-Memory-Mtime header value; "" means header absent
	}{
		{
			name:     "returns stored memory",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{"coder\x00owner_repo": "# memory"},
			wantCode: http.StatusOK,
			wantBody: "# memory",
		},
		{
			name:     "missing record returns 404",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "existing empty memory returns 200",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{"coder\x00owner_repo": ""},
			wantCode: http.StatusOK,
			wantBody: "",
		},
		{
			name:      "X-Memory-Mtime set from SQLite updated_at",
			agent:     "coder",
			repo:      "owner_repo",
			stored:    map[string]string{"coder\x00owner_repo": "# memory"},
			mtimes:    map[string]time.Time{"coder\x00owner_repo": fixedTime},
			wantCode:  http.StatusOK,
			wantBody:  "# memory",
			wantMtime: fixedTime.UTC().Format(time.RFC3339),
		},
		{
			name:      "zero timestamp omits X-Memory-Mtime header",
			agent:     "coder",
			repo:      "owner_repo",
			stored:    map[string]string{"coder\x00owner_repo": "# memory"},
			wantCode:  http.StatusOK,
			wantBody:  "# memory",
			wantMtime: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testCfg(nil)
			srv, _ := newTestServer(cfg)
			obs := newTestObserve(t)
			srv.WithObserve(obs)
			srv.WithMemoryReader(&stubMemoryReader{content: tc.stored, mtimes: tc.mtimes})

			router := srv.buildHandler()
			req := httptest.NewRequest(http.MethodGet, "/memory/"+tc.agent+"/"+tc.repo, nil)
		
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("want body %q, got %q", tc.wantBody, got)
				}
			}
			if got := rec.Header().Get("X-Memory-Mtime"); got != tc.wantMtime {
				t.Fatalf("X-Memory-Mtime: want %q, got %q", tc.wantMtime, got)
			}
		})
	}
}

// mustReadSSEMsg drains one message from ch within timeout or fails the test.
func mustReadSSEMsg(t *testing.T, ch <-chan []byte, timeout time.Duration) string {
	t.Helper()
	select {
	case msg := <-ch:
		return string(msg)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for SSE message")
		return ""
	}
}
