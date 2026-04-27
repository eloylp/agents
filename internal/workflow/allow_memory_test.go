package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// stubMemory is a minimal MemoryBackend implementation for tests. It records
// every Read/Write call so assertions can pin both load and persist behaviour.
type stubMemory struct {
	mu    sync.Mutex
	store map[string]string
	reads []string
	writes []memoryWrite
	readErr  error
	writeErr error
}

type memoryWrite struct {
	agent, repo, content string
}

func newStubMemory() *stubMemory { return &stubMemory{store: map[string]string{}} }

func (m *stubMemory) ReadMemory(agent, repo string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, agent+"/"+repo)
	if m.readErr != nil {
		return "", m.readErr
	}
	return m.store[agent+"/"+repo], nil
}

func (m *stubMemory) WriteMemory(agent, repo, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, memoryWrite{agent: agent, repo: repo, content: content})
	if m.writeErr != nil {
		return m.writeErr
	}
	m.store[agent+"/"+repo] = content
	return nil
}

func (m *stubMemory) readCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reads)
}

func (m *stubMemory) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.writes)
}

// TestEngineLoadsAndPersistsMemoryWhenAllowed pins the unified memory model
// for the workflow engine path. When AllowMemory is true (default or explicit)
// and a memory backend is wired, an event-driven run must:
//   - read the existing memory before render and inject it into the prompt
//   - persist the response's memory field after the run completes
//
// This is the symmetric implementation of the autonomous scheduler's gate so
// both surfaces honour the same per-agent flag.
func TestEngineLoadsAndPersistsMemoryWhenAllowed(t *testing.T) {
	t.Parallel()
	mem := newStubMemory()
	mem.store["arch-reviewer/owner/repo"] = "## prior-knowledge\n- last reviewed PR #41"

	runner := &stubRunner{runFn: func(req ai.Request) error {
		// Sanity: the previously stored memory must reach the prompt.
		if !strings.Contains(req.User, "last reviewed PR #41") {
			t.Errorf("user prompt missing prior memory; got:\n%s", req.User)
		}
		return nil
	}}
	runner.respFn = func(_ ai.Request) ai.Response {
		// Simulate the agent returning updated memory.
		return ai.Response{Memory: "## post-run\n- reviewed PR #42"}
	}

	cfg := newMemoryTestCfg(nil) // AllowMemory unset → IsAllowMemory()==true
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop())
	e.WithMemory(mem)

	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 42)
	if err := e.runAgent(context.Background(), ev, cfg.Agents[0], cfg, e.runners); err != nil {
		t.Fatalf("runAgent: %v", err)
	}

	if mem.readCount() != 1 {
		t.Errorf("ReadMemory call count = %d, want 1", mem.readCount())
	}
	if mem.writeCount() != 1 {
		t.Fatalf("WriteMemory call count = %d, want 1", mem.writeCount())
	}
	got := mem.writes[0]
	if got.agent != "arch-reviewer" || got.repo != "owner/repo" {
		t.Errorf("WriteMemory key = %s/%s, want arch-reviewer/owner/repo", got.agent, got.repo)
	}
	if !strings.Contains(got.content, "reviewed PR #42") {
		t.Errorf("WriteMemory content missing updated memory; got %q", got.content)
	}
}

// TestEngineSkipsMemoryWhenAllowMemoryFalse pins the off-switch: an agent
// flagged AllowMemory=false neither loads nor persists memory on the
// workflow-engine path, regardless of what the runner returns.
func TestEngineSkipsMemoryWhenAllowMemoryFalse(t *testing.T) {
	t.Parallel()
	mem := newStubMemory()
	mem.store["arch-reviewer/owner/repo"] = "## stale\n- this should not reach the prompt"

	runner := &stubRunner{runFn: func(req ai.Request) error {
		if strings.Contains(req.User, "this should not reach the prompt") {
			t.Errorf("user prompt leaked memory despite AllowMemory=false; got:\n%s", req.User)
		}
		return nil
	}}
	runner.respFn = func(_ ai.Request) ai.Response {
		return ai.Response{Memory: "## post-run\n- reviewed PR #42"}
	}

	ff := false
	cfg := newMemoryTestCfg(&ff)
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop())
	e.WithMemory(mem)

	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 42)
	if err := e.runAgent(context.Background(), ev, cfg.Agents[0], cfg, e.runners); err != nil {
		t.Fatalf("runAgent: %v", err)
	}

	if mem.readCount() != 0 {
		t.Errorf("ReadMemory should not be called when AllowMemory=false; got %d calls", mem.readCount())
	}
	if mem.writeCount() != 0 {
		t.Errorf("WriteMemory should not be called when AllowMemory=false; got %d calls", mem.writeCount())
	}
	// Confirm the previously stored memory is preserved (not clobbered).
	if mem.store["arch-reviewer/owner/repo"] != "## stale\n- this should not reach the prompt" {
		t.Errorf("pre-existing memory was overwritten despite AllowMemory=false: %q", mem.store["arch-reviewer/owner/repo"])
	}
}

// TestEngineNoMemoryBackendIsHarmless pins that an engine without a wired
// memory backend runs cleanly without trying to read or persist anything,
// regardless of AllowMemory's value. Tests here for the daemon-startup path
// where the binary may be running without persistent storage.
func TestEngineNoMemoryBackendIsHarmless(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{runFn: func(req ai.Request) error {
		// No memory should be injected when no backend is wired.
		if strings.Contains(req.User, "Existing memory:") {
			t.Errorf("memory section should not render when no backend is wired; got:\n%s", req.User)
		}
		return nil
	}}

	cfg := newMemoryTestCfg(nil) // AllowMemory unset → true
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop())
	// Note: e.WithMemory NOT called.

	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 42)
	if err := e.runAgent(context.Background(), ev, cfg.Agents[0], cfg, e.runners); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
}

// TestEngineMemoryWriteErrorDoesNotFailRun pins the fault-tolerance call: a
// memory persistence failure must be logged but not surface as a run error,
// since the agent's GitHub-side artifacts have already landed by that point.
// Failing the run would mask successful work.
func TestEngineMemoryWriteErrorDoesNotFailRun(t *testing.T) {
	t.Parallel()
	mem := newStubMemory()
	mem.writeErr = errors.New("disk full")

	runner := &stubRunner{}
	runner.respFn = func(_ ai.Request) ai.Response {
		return ai.Response{Memory: "## update", Artifacts: []ai.Artifact{}}
	}

	cfg := newMemoryTestCfg(nil)
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop())
	e.WithMemory(mem)

	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 42)
	if err := e.runAgent(context.Background(), ev, cfg.Agents[0], cfg, e.runners); err != nil {
		t.Fatalf("runAgent should not fail on memory-write error, got: %v", err)
	}
	if mem.writeCount() != 1 {
		t.Errorf("expected one WriteMemory attempt, got %d", mem.writeCount())
	}
}

// newMemoryTestCfg builds a config with one agent whose AllowMemory pointer
// is set per the argument (nil → default true; &false → explicit off).
func newMemoryTestCfg(allowMemory *bool) *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			Processor:  config.ProcessorConfig{MaxConcurrentAgents: 4},
			AIBackends: map[string]fleet.Backend{"claude": {Command: "claude"}},
		},
		Agents: []fleet.Agent{
			{Name: "arch-reviewer", Backend: "claude", Prompt: "Review.", AllowMemory: allowMemory},
		},
		Repos: []fleet.Repo{
			{Name: "owner/repo", Enabled: true, Use: []fleet.Binding{
				{Agent: "arch-reviewer", Labels: []string{"ai:review:arch-reviewer"}},
			}},
		},
	}
}
