package main

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// stubRunner satisfies ai.Runner for tests.
type stubRunner struct {
	calls int
}

func (s *stubRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	s.calls++
	return ai.Response{}, nil
}

// errRunner returns a configurable error on every Run call.
type errRunner struct {
	err error
}

func (r *errRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{}, r.err
}

func newMinimalEngine(queue workflow.EventEnqueuer) *workflow.Engine {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{MaxConcurrentAgents: 1},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Agents: []config.AgentDef{
			{Name: "worker", Backend: "claude", Prompt: "Do work."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Labels: []string{"run"}}},
			},
		},
	}
	runners := map[string]ai.Runner{"claude": &stubRunner{}}
	return workflow.NewEngine(cfg, runners, queue, zerolog.Nop())
}

func TestDrainDispatchesEmptyQueueReturnsImmediately(t *testing.T) {
	t.Parallel()
	dc := workflow.NewDataChannels(4)
	eng := newMinimalEngine(dc)
	if err := drainDispatches(context.Background(), dc, eng); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDrainDispatchesDrainsAllEnqueuedEvents(t *testing.T) {
	t.Parallel()
	dc := workflow.NewDataChannels(4)
	eng := newMinimalEngine(dc)

	// Push events that the engine will route but find no binding for
	// (kind "push" is not in the label bindings), so HandleEvent returns nil
	// without trying to contact an AI backend.
	for range 3 {
		ev := workflow.Event{
			Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true},
			Kind: "push",
		}
		if err := dc.PushEvent(context.Background(), ev); err != nil {
			t.Fatalf("PushEvent: %v", err)
		}
	}
	if got := dc.QueueStats().Buffered; got != 3 {
		t.Fatalf("expected 3 buffered events before drain, got %d", got)
	}

	if err := drainDispatches(context.Background(), dc, eng); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dc.QueueStats().Buffered; got != 0 {
		t.Fatalf("expected 0 buffered events after drain, got %d", got)
	}
}

// TestDrainDispatchesPropagatesHandleEventError verifies that an error returned
// by eng.HandleEvent is surfaced by drainDispatches rather than being swallowed.
func TestDrainDispatchesPropagatesHandleEventError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("runner failure")

	// Build an engine whose runner always errors so that any event routed to a
	// bound agent propagates the runner error through HandleEvent.
	dc := workflow.NewDataChannels(4)
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{MaxConcurrentAgents: 1},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Agents: []config.AgentDef{
			{Name: "worker", Backend: "claude", Prompt: "Do work."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				// "push" events match this binding so the worker agent is invoked.
				Use: []config.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
		},
	}
	runners := map[string]ai.Runner{"claude": &errRunner{err: sentinel}}
	eng := workflow.NewEngine(cfg, runners, dc, zerolog.Nop())

	ev := workflow.Event{
		Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true},
		Kind: "push",
	}
	if err := dc.PushEvent(context.Background(), ev); err != nil {
		t.Fatalf("PushEvent: %v", err)
	}

	err := drainDispatches(context.Background(), dc, eng)
	if err == nil {
		t.Fatal("expected error from drainDispatches, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

// TestDrainDispatchesDrainsChainedEvents verifies that events enqueued during
// a HandleEvent call (simulating chained dispatch) are also drained. The buffer
// is sized to MaxFanout*MaxDepth so no events are dropped mid-chain.
func TestDrainDispatchesDrainsChainedEvents(t *testing.T) {
	t.Parallel()

	// We simulate chaining by manually pushing a second wave of events via an
	// enqueuing runner: the first HandleEvent call causes the runner to push an
	// extra event into dc, which drainDispatches must then consume too.
	dc := workflow.NewDataChannels(8) // large enough for both waves

	var callCount int
	var enqueuingRunner ai.Runner = &enqueueOnFirstCallRunner{
		dc: dc,
		extraEv: workflow.Event{
			Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true},
			Kind: "push",
		},
		calls: &callCount,
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{MaxConcurrentAgents: 1},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Agents: []config.AgentDef{
			{Name: "worker", Backend: "claude", Prompt: "Do work."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []config.Binding{{Agent: "worker", Events: []string{"push"}}},
			},
		},
	}
	runners := map[string]ai.Runner{"claude": enqueuingRunner}
	eng := workflow.NewEngine(cfg, runners, dc, zerolog.Nop())

	// Seed one event; the runner will enqueue a second during its first call.
	ev := workflow.Event{
		Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true},
		Kind: "push",
	}
	if err := dc.PushEvent(context.Background(), ev); err != nil {
		t.Fatalf("PushEvent: %v", err)
	}

	if err := drainDispatches(context.Background(), dc, eng); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both the original event and the chained event must have been processed.
	if callCount < 2 {
		t.Fatalf("expected runner called at least twice (original + chained), got %d", callCount)
	}
	if got := dc.QueueStats().Buffered; got != 0 {
		t.Fatalf("expected 0 buffered events after drain, got %d", got)
	}
}

// enqueueOnFirstCallRunner pushes one extra event into dc on its first Run call
// so that drainDispatches must continue past the initially seeded events.
type enqueueOnFirstCallRunner struct {
	dc      *workflow.DataChannels
	extraEv workflow.Event
	calls   *int
}

func (r *enqueueOnFirstCallRunner) Run(ctx context.Context, _ ai.Request) (ai.Response, error) {
	*r.calls++
	if *r.calls == 1 {
		// Push a chained event that drainDispatches must pick up in its next iteration.
		_ = r.dc.PushEvent(ctx, r.extraEv)
	}
	return ai.Response{}, nil
}
