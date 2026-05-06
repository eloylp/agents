// Command screenshotseed boots a daemon backed by a tempdir SQLite,
// seeds it with synthetic but realistic-looking fleet config and
// activity (events, traces, dispatch history, an active run with a
// live stream feed), and listens on :8081 so the docs screenshot
// script can capture every page.
//
// Run from the repo root:
//
//	go run ./cmd/screenshotseed
//
// The daemon stays up until you Ctrl-C. Hit /ui/ in a browser to
// preview before letting Playwright drive it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/daemon"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// blockingRunner satisfies ai.Runner but never returns. The screenshot
// seeder uses this so events the workflow processor picks up stay
// in-flight (event_queue.completed_at stays NULL, the engine's
// BeginRun keeps an entry in observe.Runs) until the daemon is shut
// down. Without this, the screenshot of the runners page would only
// show completed rows because the real claude/codex binaries aren't
// available on the screenshotting host.
type blockingRunner struct{ name string }

func (b *blockingRunner) Run(ctx context.Context, _ ai.Request) (ai.Response, error) {
	<-ctx.Done()
	return ai.Response{}, ctx.Err()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "screenshotseed:", err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "screenshotseed-")
	if err != nil {
		return err
	}
	dbPath := filepath.Join(tmp, "agents.db")
	fmt.Fprintf(os.Stderr, "screenshotseed: tempdir = %s\n", tmp)

	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	st := store.New(db)

	cfg := buildFixtureConfig()
	if err := st.ImportAll(cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends, cfg.Guardrails, nil); err != nil {
		return fmt.Errorf("import seed: %w", err)
	}

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(zerolog.WarnLevel)
	d, err := daemon.New(cfg, st, logger)
	if err != nil {
		return fmt.Errorf("daemon.New: %w", err)
	}
	// Replace the real runner factory with a blocking stub so events
	// the workflow processor picks up stay in flight indefinitely. We
	// want the runners page to show "running" rows for the screenshot.
	d.Engine().WithRunnerBuilder(func(name string, _ fleet.Backend) ai.Runner {
		return &blockingRunner{name: name}
	})

	// Seed observability + an in-flight run AFTER daemon.New so we use
	// the same observe.Store the HTTP handlers will read from.
	seedActivity(d.Observe(), st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "screenshotseed: daemon listening on :8081, Ctrl-C to stop")
	return d.Run(ctx)
}

// buildFixtureConfig assembles a small but realistic fleet so screenshots
// have rich content, multiple agents with descriptions, two backends
// with one healthy / one with a discovery warning, a couple of repos
// with mixed binding shapes (labels + cron).
func buildFixtureConfig() *config.Config {
	enabled := true
	disabled := false
	_ = disabled

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Log: config.LogConfig{Format: "console", Level: "warn"},
			HTTP: config.HTTPConfig{
				ListenAddr:             ":8081",
				WebhookPath:            "/webhooks/github",
				StatusPath:             "/status",
				MaxBodyBytes:           1 << 20,
				WebhookSecret:          "screenshot-only",
				DeliveryTTLSeconds:     3600,
				ShutdownTimeoutSeconds: 5,
			},
			Processor: config.ProcessorConfig{
				EventQueueBuffer:    32,
				MaxConcurrentAgents: 2,
				Dispatch: config.DispatchConfig{
					MaxDepth:           3,
					MaxFanout:          4,
					DedupWindowSeconds: 300,
				},
			},
			AIBackends: map[string]fleet.Backend{
				"claude": {
					Command: "claude", Version: "claude-code 0.4.2",
					Models:  []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"},
					Healthy: true, HealthDetail: "GitHub MCP reachable",
					TimeoutSeconds: 1500, MaxPromptChars: 12000,
				},
				"codex": {
					Command: "codex", Version: "codex 0.9.1",
					Models:  []string{"gpt-5", "gpt-5-mini"},
					Healthy: false, HealthDetail: "GitHub MCP server not registered, run `codex mcp add github`",
					TimeoutSeconds: 600, MaxPromptChars: 10000,
				},
				"local-qwen": {
					Command: "claude",
					Version: "claude-code 0.4.2 → local-llm",
					Models:  []string{"qwen3-coder-480b"},
					Healthy: true, HealthDetail: "routed via Anthropic↔OpenAI proxy",
					LocalModelURL:  "http://local-llm:18000/v1",
					TimeoutSeconds: 900, MaxPromptChars: 8000,
				},
			},
		},
		Skills: map[string]fleet.Skill{
			"architect": {Prompt: "## Architect mindset\n\nFavour boring solutions. Write down the trade-offs you considered. Reject premature abstractions; three similar lines beats a clever helper.\n\nThink about reversibility, destructive actions need a confirmation gate.\n"},
			"security":  {Prompt: "## Security review\n\nThink about: input validation, authn/authz boundaries, secrets handling, injection vectors (SQL, command, prompt), supply-chain (dependency pinning), and the OWASP top 10.\n\nFlag risks in the response summary; do not mention them on PR comments without an explicit operator request.\n"},
			"testing":   {Prompt: "## Testing discipline\n\nEvery behavioural change ships with a test. Table-driven for >2 input shapes. `t.Parallel()` when independent. Run `-race` before declaring done.\n"},
			"dx":        {Prompt: "## Developer experience\n\nReadable error messages. Sensible defaults. Logs that an operator can grep without a manual.\n"},
		},
		Agents: []fleet.Agent{
			{
				Name:          "coder",
				Backend:       "claude",
				Model:         "claude-opus-4-7",
				Skills:        []string{"architect", "testing", "dx"},
				Description:   "Implements small features and bug fixes from labelled issues. Works on a branch, opens a PR.",
				AllowPRs:      true,
				AllowDispatch: true,
				CanDispatch:   []string{"pr-reviewer", "scout"},
				Prompt:        "You are the coder agent. Pick up the issue described in the runtime context, write the smallest viable change, ship a PR with a tight description and a test plan. Dispatch pr-reviewer once the PR is open.\n",
			},
			{
				Name:          "pr-reviewer",
				Backend:       "claude",
				Model:         "claude-sonnet-4-6",
				Skills:        []string{"architect", "security", "testing"},
				Description:   "Reviews open PRs for correctness, design, and test coverage. Approves or requests changes.",
				AllowPRs:      true,
				AllowDispatch: true,
				Prompt:        "You are the pr-reviewer agent. Read the PR diff, check correctness against the linked issue, surface design concerns, verify test coverage, then either approve or request changes with concrete feedback.\n",
			},
			{
				Name:          "scout",
				Backend:       "claude",
				Model:         "claude-haiku-4-5",
				Skills:        []string{"architect"},
				Description:   "Sweeps the codebase weekly for stale TODOs, dead code, doc drift, and missed follow-ups.",
				AllowDispatch: true,
				CanDispatch:   []string{"coder"},
				Prompt:        "You are the scout agent. Walk the repo on a schedule, surface drift between code and docs, file issues for follow-ups, and dispatch coder when the fix is small and obvious.\n",
			},
			{
				Name:        "refactorer",
				Backend:     "local-qwen",
				Model:       "qwen3-coder-480b",
				Skills:      []string{"architect"},
				Description: "Cron-driven housekeeper: removes dead branches, migrates deprecated APIs, keeps tooling current.",
				AllowPRs:    true,
				Prompt:      "You are the refactorer. On every cron tick, find one piece of housekeeping work the codebase needs, do it, ship a PR. Always small, never speculative.\n",
			},
		},
		Repos: []fleet.Repo{
			{
				Name:    "acme/widgets",
				Enabled: true,
				Use: []fleet.Binding{
					{ID: 1, Agent: "coder", Labels: []string{"ai:fix", "ai:feature"}, Enabled: &enabled},
					{ID: 2, Agent: "pr-reviewer", Events: []string{"pull_request.opened", "pull_request.synchronize"}, Enabled: &enabled},
					{ID: 3, Agent: "refactorer", Cron: "0 6 * * 1", Enabled: &enabled},
				},
			},
			{
				Name:    "acme/control-plane",
				Enabled: true,
				Use: []fleet.Binding{
					{ID: 4, Agent: "pr-reviewer", Events: []string{"pull_request.opened"}, Enabled: &enabled},
					{ID: 5, Agent: "scout", Cron: "0 7 * * *", Enabled: &enabled},
				},
			},
			{
				Name:    "acme/playground",
				Enabled: false,
				Use:     []fleet.Binding{},
			},
		},
	}
	return cfg
}

// seedActivity writes synthetic events / traces / dispatch_history
// directly into the observe + queue tables so the relevant pages have
// visible data. Also registers one ActiveRun in the in-memory registry
// so the runners page shows a live row with a working ▶ Live button.
func seedActivity(obs *observe.Store, st *store.Store) {
	now := time.Now()

	// Recent events firehose
	events := []workflow.Event{
		{ID: "evt-001", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "issues.labeled", Number: 142, Actor: "alice", Payload: map[string]any{"label": "ai:fix"}},
		{ID: "evt-002", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "pull_request.opened", Number: 143, Actor: "coder", Payload: map[string]any{"title": "fix: handle empty cart in checkout", "draft": false}},
		{ID: "evt-003", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "pull_request.synchronize", Number: 143, Actor: "coder", Payload: map[string]any{"title": "fix: handle empty cart in checkout"}},
		{ID: "evt-004", Repo: workflow.RepoRef{FullName: "acme/control-plane", Enabled: true}, Kind: "pull_request.opened", Number: 88, Actor: "bob", Payload: map[string]any{"title": "add: rate limiter for /run", "draft": false}},
		{ID: "evt-005", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "agent.dispatch", Number: 143, Actor: "coder", Payload: map[string]any{"target_agent": "pr-reviewer", "reason": "PR ready for review", "invoked_by": "coder"}},
		{ID: "evt-006", Repo: workflow.RepoRef{FullName: "acme/control-plane", Enabled: true}, Kind: "cron", Number: 0, Actor: "scout", Payload: map[string]any{"target_agent": "scout"}},
		{ID: "evt-007", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "issue_comment.created", Number: 142, Actor: "carol", Payload: map[string]any{"body": "Could you also handle the multi-currency case?"}},
		{ID: "evt-008", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true}, Kind: "pull_request.closed", Number: 138, Actor: "alice", Payload: map[string]any{"title": "chore: bump go to 1.25", "merged": true}},
		{ID: "evt-009", Repo: workflow.RepoRef{FullName: "acme/control-plane", Enabled: true}, Kind: "agents.run", Number: 0, Actor: "mcp", Payload: map[string]any{"target_agent": "scout"}},
	}
	// Stagger over the last 30 minutes so the time-bucket histogram has shape.
	for i, ev := range events {
		at := now.Add(-time.Duration(len(events)-i) * 3 * time.Minute)
		obs.RecordEvent(at, ev)
	}

	// Trace spans, both completed and one in-flight (registered separately).
	completed := []workflow.SpanInput{
		{
			SpanID: "span-001", RootEventID: "evt-001",
			Agent: "coder", Backend: "claude", Repo: "acme/widgets",
			Number: 142, EventKind: "issues.labeled",
			Summary:   "Implemented checkout fix; opened PR #143 with two regression tests",
			StartedAt: now.Add(-22 * time.Minute), FinishedAt: now.Add(-19 * time.Minute),
			Status:      "success",
			Prompt:      "## Runtime context\n\nEvent: issues.labeled\nActor: alice\nRepo: acme/widgets #142\n\n## Issue\n\n**Empty cart in checkout returns 500.** Reproduce by clicking 'pay' with no items in the cart.\n\n## Available experts\n\n- pr-reviewer, Reviews open PRs for correctness, design, and test coverage.\n\n## Memory\n\n_no memory yet_\n\n## Response format\n\nReturn a single JSON object matching the response schema. Include a one-line summary, any artifacts you produced, and any agents you want to dispatch.\n",
			InputTokens: 4321, OutputTokens: 1850, CacheReadTokens: 12400, CacheWriteTokens: 2100,
			ArtifactsCount: 1,
		},
		{
			SpanID: "span-002", RootEventID: "evt-005", ParentSpanID: "span-001",
			Agent: "pr-reviewer", Backend: "claude", Repo: "acme/widgets",
			Number: 143, EventKind: "agent.dispatch", InvokedBy: "coder", DispatchDepth: 1,
			Summary:   "Approved with one nit: missing edge case for negative totals",
			StartedAt: now.Add(-17 * time.Minute), FinishedAt: now.Add(-14 * time.Minute),
			Status:      "success",
			Prompt:      "## Runtime context\n\nEvent: agent.dispatch\nInvoked by: coder\nReason: PR ready for review\nRepo: acme/widgets #143\n\nReview PR #143: 'fix: handle empty cart in checkout'.\n",
			InputTokens: 5210, OutputTokens: 920, CacheReadTokens: 11800, CacheWriteTokens: 1500,
		},
		{
			SpanID: "span-003", RootEventID: "evt-004",
			Agent: "pr-reviewer", Backend: "claude", Repo: "acme/control-plane",
			Number: 88, EventKind: "pull_request.opened",
			Summary:   "Requested changes: rate limiter algorithm choice doesn't account for the cron-tick burst pattern",
			StartedAt: now.Add(-10 * time.Minute), FinishedAt: now.Add(-7 * time.Minute),
			Status:      "success",
			InputTokens: 6100, OutputTokens: 1340, CacheReadTokens: 9800, CacheWriteTokens: 1100,
		},
		{
			SpanID: "span-004", RootEventID: "evt-006",
			Agent: "scout", Backend: "claude", Repo: "acme/control-plane",
			Number: 0, EventKind: "cron",
			Summary:   "Found 3 stale TODOs in internal/limiter; filed issue #91",
			StartedAt: now.Add(-5 * time.Minute), FinishedAt: now.Add(-3 * time.Minute),
			Status:      "success",
			InputTokens: 2200, OutputTokens: 410, CacheReadTokens: 8200, CacheWriteTokens: 800,
		},
		{
			SpanID: "span-005", RootEventID: "evt-008",
			Agent: "pr-reviewer", Backend: "claude", Repo: "acme/widgets",
			Number: 138, EventKind: "pull_request.opened",
			Summary:   "Approved",
			StartedAt: now.Add(-45 * time.Minute), FinishedAt: now.Add(-42 * time.Minute),
			Status: "success",
		},
	}
	for _, sp := range completed {
		obs.RecordSpan(sp)
	}

	// Dispatch history rows for the graph view.
	for _, d := range []struct {
		from, to, repo, reason string
		number                 int
		ago                    time.Duration
	}{
		{"coder", "pr-reviewer", "acme/widgets", "PR ready for review", 143, 17 * time.Minute},
		{"coder", "pr-reviewer", "acme/widgets", "PR ready for review", 138, 45 * time.Minute},
		{"scout", "coder", "acme/control-plane", "small fix worth doing now", 91, 5 * time.Minute},
	} {
		obs.RecordDispatch(d.from, d.to, d.repo, d.number, d.reason)
		_ = d.ago
	}

	// Memory entry so the memory page renders something interesting.
	if _, err := st.DB().Exec(
		`INSERT OR REPLACE INTO memory (agent, repo, content, updated_at) VALUES (?, ?, ?, ?)`,
		"refactorer", "acme/widgets",
		"# refactorer notes, acme/widgets\n\n## Recent sweeps\n\n- 2026-04-29: removed three unused helpers in `internal/checkout/`. PR #129 merged.\n- 2026-04-22: migrated 12 call sites off the deprecated `db.Tx{}` shape. PR #117 merged.\n\n## Known follow-ups\n\n- The `pricing.Strategy` interface has only one impl. Worth collapsing; not urgent.\n- `internal/audit/` has a `// TODO(coder): index by tenant` comment that's been there for two months.\n\n## Style decisions I've absorbed\n\n- Repo prefers concrete types over interfaces unless 2+ impls exist.\n- Tests next to code, not in `_test/` subdirs.\n- Error wrapping uses `fmt.Errorf(\"x: %w\", err)`, never `errors.Wrap`.\n",
		now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		log.Println("seed memory:", err)
	}

	// Event_queue rows so the runners view has both completed and
	// in-flight rows. The completed rows JOIN to the trace spans above.
	for _, q := range []struct {
		ev           workflow.Event
		started, end *time.Time
	}{
		queueRow(events[0], ptrMinus(now, 22*time.Minute), ptrMinus(now, 19*time.Minute)),
		queueRow(events[1], ptrMinus(now, 18*time.Minute), ptrMinus(now, 17*time.Minute)),
		queueRow(events[3], ptrMinus(now, 11*time.Minute), ptrMinus(now, 7*time.Minute)),
		queueRow(events[4], ptrMinus(now, 17*time.Minute), ptrMinus(now, 14*time.Minute)),
		queueRow(events[5], ptrMinus(now, 5*time.Minute), ptrMinus(now, 3*time.Minute)),
	} {
		blob, _ := json.Marshal(q.ev)
		id, err := st.EnqueueEvent(string(blob))
		if err != nil {
			log.Println("seed queue:", err)
			continue
		}
		if q.started != nil {
			_ = st.MarkEventStarted(id)
		}
		if q.end != nil {
			_ = st.MarkEventCompleted(id)
		}
	}

	// One in-flight event with a registered ActiveRun → the runners
	// page renders a live row with the ▶ Live button.
	live := workflow.Event{
		ID: "evt-live", Repo: workflow.RepoRef{FullName: "acme/widgets", Enabled: true},
		Kind: "issues.labeled", Number: 144, Actor: "alice",
		Payload: map[string]any{"label": "ai:feature"},
	}
	blob, _ := json.Marshal(live)
	if id, err := st.EnqueueEvent(string(blob)); err == nil {
		_ = st.MarkEventStarted(id)
	}
	// Two distinct agents on the same event so the runners screenshot
	// shows the fanout shape clearly. The engine's blocking-runner
	// stub will register its own ActiveRun for `coder` (the only
	// binding that matches `ai:feature` on widgets); we seed
	// `pr-reviewer` here so two different agent names show up live.
	obs.Runs.BeginRun(observe.ActiveRun{
		SpanID: "span-live-1", EventID: "evt-live",
		Agent: "pr-reviewer", Backend: "claude",
		Repo: "acme/widgets", EventKind: "issues.labeled",
		StartedAt: now.Add(-30 * time.Second),
	})
	// Pre-persist a few transcript steps so a fresh subscriber sees content
	// in the Live modal immediately.
	for _, step := range []workflow.TraceStep{
		{Kind: workflow.StepKindThinking, InputSummary: "Reading the existing checkout module to understand the empty-cart code path."},
		{Kind: workflow.StepKindTool, ToolName: "read_file", InputSummary: `{"path":"internal/checkout/handler.go"}`, OutputSummary: "package checkout\n\nfunc Handle(...)... (truncated 220 lines)"},
		{Kind: workflow.StepKindThinking, InputSummary: "The handler doesn't validate that the cart is non-empty before computing total. I'll add a guard and a regression test."},
		{Kind: workflow.StepKindTool, ToolName: "write_file", InputSummary: `{"path":"internal/checkout/handler.go","content":"<patched body, ~40 lines>"}`},
	} {
		obs.RecordStep("span-live-1", step)
	}
}

func queueRow(ev workflow.Event, started, completed *time.Time) struct {
	ev           workflow.Event
	started, end *time.Time
} {
	return struct {
		ev           workflow.Event
		started, end *time.Time
	}{ev, started, completed}
}

func ptrMinus(t time.Time, d time.Duration) *time.Time {
	v := t.Add(-d)
	return &v
}
