package workflow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/eloylp/agents/internal/store"
)

type traceRecorderStub struct {
	mu    sync.Mutex
	spans []SpanInput
}

func (r *traceRecorderStub) RecordSpan(in SpanInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, in)
}

func (r *traceRecorderStub) last() (SpanInput, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.spans) == 0 {
		return SpanInput{}, false
	}
	return r.spans[len(r.spans)-1], true
}

func TestEngineBudgetExceededSkipsRunnerAndRecordsStatus(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(t, nil)
	e.WithBudgetStore(e.store)
	rec := &traceRecorderStub{}
	e.WithTraceRecorder(rec)

	if _, err := e.store.CreateTokenBudget(store.TokenBudget{
		ScopeKind:  "global",
		Period:     "daily",
		CapTokens:  1,
		AlertAtPct: 80,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create budget: %v", err)
	}
	if _, err := e.store.DB().Exec(`
		INSERT INTO traces (
			span_id, root_event_id, parent_span_id, agent, backend, repo, event_kind,
			started_at, finished_at, duration_ms, status,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		)
		VALUES ('existing', 'root-existing', '', 'arch-reviewer', 'claude', 'owner/repo', 'issues.labeled',
			datetime('now'), datetime('now'), 1, 'success', 1, 0, 0, 0)`,
	); err != nil {
		t.Fatalf("insert trace usage: %v", err)
	}

	err := e.HandleEvent(context.Background(), Event{
		ID:      "evt-budget",
		Kind:    "agents.run",
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Payload: map[string]any{"target_agent": "arch-reviewer"},
	})
	if err == nil || !strings.Contains(err.Error(), "token budget exceeded") {
		t.Fatalf("HandleEvent err = %v, want token budget exceeded", err)
	}
	if got := runner.callCount(); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	span, ok := rec.last()
	if !ok {
		t.Fatal("no trace span recorded")
	}
	if span.Status != "budget_exceeded" {
		t.Fatalf("span status = %q, want budget_exceeded", span.Status)
	}
	if span.Agent != "arch-reviewer" || span.Backend != "claude" || span.Repo != "owner/repo" {
		t.Fatalf("span identity = %+v", span)
	}
}
