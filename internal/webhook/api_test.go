package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
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

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, provider, nil, zerolog.Nop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, provider, nil, zerolog.Nop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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

// ── /api/config ────────────────────────────────────────────────────────────

func TestHandleAPIConfigRedactsSecrets(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WebhookSecret = "super-secret-webhook"
		c.Daemon.HTTP.WebhookSecretEnv = "GITHUB_WEBHOOK_SECRET"
		c.Daemon.HTTP.APIKey = "super-secret-api-key"
		c.Daemon.HTTP.APIKeyEnv = "AGENTS_API_KEY"
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

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	srv.handleAPIConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Actual secret values must not appear anywhere in the response.
	secrets := []string{"super-secret-webhook", "super-secret-api-key", "proxy-secret", "sk-ant-secret"}
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
	if !strings.Contains(body,"AGENTS_API_KEY") {
		t.Error("env var name AGENTS_API_KEY must be preserved")
	}
	// Backend env key must appear, but value must be redacted.
	if !strings.Contains(body,"ANTHROPIC_API_KEY") {
		t.Error("backend env key ANTHROPIC_API_KEY must be preserved")
	}
}

func TestHandleAPIConfigNoSecretsWhenNotSet(t *testing.T) {
	t.Parallel()
	// Minimal config: secrets are empty strings — [redacted] must NOT appear.
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.WebhookSecret = ""
		c.Daemon.HTTP.APIKey = ""
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
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
	srv := NewServer(cfg, NewDeliveryStore(time.Hour), dc, nil, provider, zerolog.Nop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/dispatches", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/dispatches", nil)
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

// ── requireAPIKey middleware ────────────────────────────────────────────────

// TestRequireAPIKeyBlocksWhenKeyConfigured verifies that /api/* returns 401
// when daemon.http.api_key is set and no Authorization header is sent.
func TestRequireAPIKeyBlocksWhenKeyConfigured(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/agents", "/api/config", "/api/dispatches"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			srv, _ := newTestServer(testCfg(nil)) // testCfg sets APIKey = testAPIKey

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: want 401 without token, got %d", path, rec.Code)
			}
		})
	}
}

// TestRequireAPIKeyBlocksWrongToken verifies that a wrong Bearer token is
// rejected with 401.
func TestRequireAPIKeyBlocksWrongToken(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(testCfg(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	srv.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for wrong token, got %d", rec.Code)
	}
}

// TestRequireAPIKeyBlocksNonBearerScheme verifies that the middleware rejects
// Authorization headers that do not use the Bearer scheme, even when the value
// happens to be the correct API key.
func TestRequireAPIKeyBlocksNonBearerScheme(t *testing.T) {
	t.Parallel()
	for _, authHeader := range []string{
		testAPIKey,                   // raw key, no scheme
		"Basic " + testAPIKey,        // Basic scheme instead of Bearer
		"Token " + testAPIKey,        // other non-Bearer scheme
	} {
		authHeader := authHeader
		t.Run(authHeader, func(t *testing.T) {
			t.Parallel()
			srv, _ := newTestServer(testCfg(nil))

			req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
			req.Header.Set("Authorization", authHeader)
			rec := httptest.NewRecorder()
			srv.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("want 401 for non-Bearer auth %q, got %d", authHeader, rec.Code)
			}
		})
	}
}

// TestRequireAPIKeyAllowsCorrectToken verifies that the correct Bearer token
// passes the middleware and reaches the inner handler.
func TestRequireAPIKeyAllowsCorrectToken(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(testCfg(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rec := httptest.NewRecorder()
	srv.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with correct token, got %d", rec.Code)
	}
}

// TestRequireAPIKeyOpenWhenNoKeyConfigured verifies that when no API key is
// configured the middleware allows unauthenticated requests through.
func TestRequireAPIKeyOpenWhenNoKeyConfigured(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Daemon.HTTP.APIKey = "" // no key → open access
	})
	srv, _ := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	srv.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 when no key configured, got %d", rec.Code)
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
