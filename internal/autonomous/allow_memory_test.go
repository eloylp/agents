package autonomous

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

// callCountingMemory tracks ReadMemory and WriteMemory invocations so tests
// can assert exactly when (and whether) the scheduler hits the memory store.
type callCountingMemory struct {
	mu     sync.Mutex
	reads  int
	writes int
	last   string
}

func (m *callCountingMemory) ReadMemory(_, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.last, nil
}

func (m *callCountingMemory) WriteMemory(_, _, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	m.last = content
	return nil
}

func (m *callCountingMemory) snapshot() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads, m.writes
}

// TestExecuteAgentRunSkipsMemoryWhenAllowMemoryFalse verifies that the
// scheduler does NOT call ReadMemory or WriteMemory when an agent has
// AllowMemory set to false, regardless of whether the runner returns a
// non-empty memory field. This is the core gate the issue introduces.
func TestExecuteAgentRunSkipsMemoryWhenAllowMemoryFalse(t *testing.T) {
	t.Parallel()

	ff := false
	cfg := baseCfg(func(c *config.Config) {
		c.Agents[0].AllowMemory = &ff
	})
	runner := &stubRunner{memory: "agent-returned-memory"}
	mem := &callCountingMemory{}

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, mem, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	reads, writes := mem.snapshot()
	if reads != 0 {
		t.Errorf("ReadMemory called %d times when allow_memory=false; want 0", reads)
	}
	if writes != 0 {
		t.Errorf("WriteMemory called %d times when allow_memory=false; want 0", writes)
	}

	// Sanity: the run still succeeded — the runner was invoked exactly once.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Errorf("runner calls = %d, want 1", runner.calls)
	}
}

// TestExecuteAgentRunLoadsAndPersistsWhenAllowMemoryDefault verifies that the
// scheduler calls ReadMemory before each run and WriteMemory after a non-empty
// response when AllowMemory is left at its default (nil → IsAllowMemory()=true).
// Pre-existing autonomous agents must keep this behaviour after the toggle
// lands.
func TestExecuteAgentRunLoadsAndPersistsWhenAllowMemoryDefault(t *testing.T) {
	t.Parallel()

	cfg := baseCfg(nil) // AllowMemory left nil → defaults to true
	runner := &stubRunner{memory: "agent-returned-memory"}
	mem := &callCountingMemory{}

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, mem, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	reads, writes := mem.snapshot()
	if reads != 1 {
		t.Errorf("ReadMemory calls = %d, want 1", reads)
	}
	if writes != 1 {
		t.Errorf("WriteMemory calls = %d, want 1 (runner returned non-empty memory)", writes)
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if mem.last != "agent-returned-memory" {
		t.Errorf("persisted memory = %q, want %q", mem.last, "agent-returned-memory")
	}
}

// TestExecuteAgentRunSkipsMemoryEvenWhenRunnerReturnsContent ensures that
// allow_memory=false truly is a hard runtime gate: even if the agent's
// response carries a non-empty Memory string, no WriteMemory call is made.
// This protects the contract that the toggle does not depend on the agent
// prompt cooperating.
func TestExecuteAgentRunSkipsMemoryEvenWhenRunnerReturnsContent(t *testing.T) {
	t.Parallel()

	ff := false
	cfg := baseCfg(func(c *config.Config) {
		c.Agents[0].AllowMemory = &ff
	})
	runner := &stubRunner{memory: "would-be-persisted"}
	mem := &callCountingMemory{last: "pre-existing"}

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, mem, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	if _, writes := mem.snapshot(); writes != 0 {
		t.Errorf("WriteMemory called %d times despite allow_memory=false; want 0", writes)
	}
	// Pre-existing memory must be untouched — the gate must not delete it.
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if mem.last != "pre-existing" {
		t.Errorf("memory mutated to %q despite allow_memory=false; pre-existing %q must be preserved",
			mem.last, "pre-existing")
	}
}
