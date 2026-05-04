package daemon_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// ── /api/agents ────────────────────────────────────────────────────────────

func TestHandleAPIAgentsReturnsConfiguredAgents(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Skills = map[string]fleet.Skill{"testing": {Prompt: "test thoroughly"}}
		c.Agents = []fleet.Agent{
			{
				Name:          "sec-reviewer",
				Backend:       "claude",
				Prompt:        "security review",
				Description:   "Reviews PRs for security issues.",
				AllowDispatch: true,
			},
			{
				Name:          "reviewer",
				Backend:       "claude",
				Prompt:        "review PRs",
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
	srv, _ := newTestServer(t, cfg)

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
	// Locate "reviewer" in the response, the fixture also seeds an agent
	// and "sec-reviewer" exists for the can_dispatch reference.
	idx := slices.IndexFunc(agents, func(a viewAgentJSON) bool { return a.Name == "reviewer" })
	if idx == -1 {
		t.Fatalf("reviewer not found in response: got %d agents", len(agents))
	}
	got := &agents[idx]
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
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "claude", Prompt: "p"}}
		c.Repos = []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "worker", Cron: "0 * * * *"}},
			},
		}
	})
	srv, _ := newTestServer(t, cfg)
	// Seed the scheduler's last-run state through the same hook the engine
	// uses on real cron completions.
	now := time.Now().UTC().Truncate(time.Second)
	srv.Scheduler().RecordLastRun("worker", "owner/repo", now, "ok")

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
// each binding, not just the last repo visited in the loop.
func TestHandleAPIAgentsMultiRepoSchedulePreserved(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "claude", Prompt: "p"}}
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
	srv, _ := newTestServer(t, cfg)
	now := time.Now().UTC().Truncate(time.Second)
	// Seed last-run state for repo-a only, repo-b stays at "never run" so
	// the test asserts the per-binding schedule slot on the agent view.
	srv.Scheduler().RecordLastRun("worker", "owner/repo-a", now, "ok")

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
	if repoB.Schedule.LastStatus != "" {
		t.Errorf("repo-b last_status: want empty (no run yet), got %q", repoB.Schedule.LastStatus)
	}
}

// TestHandleAPIAgentsIncludesDisabledRepoBindings verifies that bindings on
// disabled repos appear in the /agents fleet snapshot with repo_enabled=false.
// The wire view is for inspection (memory page, audit, MCP), not runtime
// dispatch, hiding disabled-repo bindings here also hides the agent's
// memory in the dashboard. The runtime entry points (POST /run, webhook,
// engine.HandleEvent) refuse disabled repos at the boundary; the wire view
// trusts consumers to filter.
func TestHandleAPIAgentsIncludesDisabledRepoBindings(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "claude", Prompt: "p"}}
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
	srv, _ := newTestServer(t, cfg)

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

func TestHandleAPIAgentsReturnsArrayShape(t *testing.T) {
	t.Parallel()
	// The store invariant ("at least one agent") prevents a literal "no
	// agents" config from booting, so the test now asserts the wire shape
	// rather than the empty-slice case (the JSON array contract is what
	// /agents commits to; the count is whatever the fixture seeded).
	srv, _ := newTestServer(t, testCfg(nil))

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
	if agents == nil {
		t.Errorf("/agents returned nil slice, want JSON array")
	}
}

func TestHandleAPIAgentsCurrentStatusIdleWhenNotRunning(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	srv, _ := newTestServer(t, cfg)

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

func TestHandleAPIAgentsCurrentStatusRunningWhenActive(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) {
		c.Agents = []fleet.Agent{{Name: "coder", Backend: "claude", Prompt: "p"}}
	})
	srv, _ := newTestServer(t, cfg)
	// Mark "coder" as running through the same hook the engine uses on a
	// real run start.
	srv.Observe().ActiveRuns.StartRun("coder")

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

// ── /config ────────────────────────────────────────────────────────────────

func TestHandleAPIConfigOmitsDaemonRuntimeConfig(t *testing.T) {
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
	srv, _ := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	secrets := []string{"super-secret-webhook", "proxy-secret", "sk-ant-secret"}
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("daemon secret %q must not appear in fleet config response", s)
		}
	}
	for _, key := range []string{"daemon", "http", "processor", "proxy", "webhook_secret_env", "api_key_env"} {
		if strings.Contains(body, key) {
			t.Errorf("daemon runtime key %q must not appear in fleet config response: %s", key, body)
		}
	}
	if !strings.Contains(body, "backends") {
		t.Errorf("fleet config response should include backends: %s", body)
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
	srv, _ := newTestServer(t, cfg)

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
	srv, _ := newTestServer(t, cfg)

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
	srv, _ := newTestServer(t, testCfg(nil))
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
		c.Agents = []fleet.Agent{{Name: "worker", Backend: "claude", Prompt: "p"}}
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
	srv, _ := newTestServer(t, cfg)

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
	srv, _ := newTestServer(t, cfg)
	obs := srv.Observe()

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

	srv, _ := newTestServer(t, testCfg(nil))
	obs := srv.Observe()

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
