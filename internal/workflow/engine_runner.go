package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/fleet"
	runtimeexec "github.com/eloylp/agents/internal/runtime"
)

// defaultRunnerFor builds the ai.Runner that drives the AI CLI for the
// named backend. Built per-event so that backend changes via CRUD take
// effect immediately on the next event without any reload chain. The
// construction is cheap: a struct holding command + env + timeouts.
//
// Tests override the runner via WithRunnerBuilder so they can observe
// what the engine asked the runner to do without spawning a real CLI.
func (e *Engine) defaultRunnerFor(workspaceID string, name string, b fleet.Backend) ai.Runner {
	var env map[string]string
	if b.LocalModelURL != "" {
		env = map[string]string{"ANTHROPIC_BASE_URL": b.LocalModelURL}
	}
	settings, err := e.store.ReadRuntimeSettings()
	if err != nil {
		return errorRunner{err: fmt.Errorf("read runtime settings: %w", err)}
	}
	if workspace, err := e.store.ReadWorkspace(workspaceID); err == nil && strings.TrimSpace(workspace.RunnerImage) != "" {
		settings.RunnerImage = strings.TrimSpace(workspace.RunnerImage)
	} else if err != nil {
		e.logger.Debug().Err(err).Str("workspace", workspaceID).Msg("read workspace runner override")
	}
	dockerRunner, err := e.dockerRuntime()
	if err != nil {
		return errorRunner{err: fmt.Errorf("create docker runtime: %w", err)}
	}
	policy := runtimeexec.ContainerSpec{Policy: settings.Constraints}
	timeoutSeconds := policy.Policy.TimeoutSeconds
	policy.Policy.TimeoutSeconds = 0
	if timeoutSeconds == 0 {
		timeoutSeconds = b.TimeoutSeconds
	}
	return ai.NewContainerCommandRunner(
		name, b.Command, env,
		timeoutSeconds, b.MaxPromptChars,
		dockerRunner, settings.RunnerImage, policy,
		e.logger,
	)
}

func (e *Engine) dockerRuntime() (runtimeexec.Runner, error) {
	e.dockerMu.Lock()
	defer e.dockerMu.Unlock()
	if e.dockerRunner != nil {
		return e.dockerRunner, nil
	}
	runner, err := runtimeexec.NewDocker(e.logger)
	if err != nil {
		return nil, err
	}
	e.dockerRunner = runner
	return runner, nil
}

func (e *Engine) Close() error {
	e.dockerMu.Lock()
	defer e.dockerMu.Unlock()
	if e.dockerRunner == nil {
		return nil
	}
	closer, ok := e.dockerRunner.(interface{ Close() error })
	if !ok {
		e.dockerRunner = nil
		return nil
	}
	err := closer.Close()
	e.dockerRunner = nil
	return err
}

type errorRunner struct {
	err error
}

func (r errorRunner) Run(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, r.err
}
