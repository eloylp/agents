package setup_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/setup"
)

// captureRunner records the command invocation without executing anything.
type captureRunner struct {
	name   string
	args   []string
	stdin  string
	called bool
}

func (c *captureRunner) Run(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
	c.called = true
	c.name = name
	c.args = args
	b, _ := io.ReadAll(stdin)
	c.stdin = string(b)
	return nil
}

// errorRunner always returns the provided error.
type errorRunner struct{ err error }

func (e errorRunner) Run(_ string, _ []string, _ io.Reader, _, _ io.Writer) error { return e.err }

func TestRunCallsClaude(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	if err := setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.called {
		t.Fatal("expected runner to be called")
	}
	if runner.name != "claude" {
		t.Errorf("command: got %q, want %q", runner.name, "claude")
	}
}

func TestRunPassesExpectedArgs(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	_ = setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{})

	want := []string{"-p", "--dangerously-skip-permissions"}
	if len(runner.args) != len(want) {
		t.Fatalf("args length: got %d, want %d", len(runner.args), len(want))
	}
	for i, a := range want {
		if runner.args[i] != a {
			t.Errorf("args[%d]: got %q, want %q", i, runner.args[i], a)
		}
	}
}

func TestRunPipesNonEmptyPrompt(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	_ = setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{})

	if strings.TrimSpace(runner.stdin) == "" {
		t.Error("embedded setup prompt must not be empty")
	}
}

func TestRunDryRunDoesNotCallRunner(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}
	var stdout bytes.Buffer

	if err := setup.Run(runner, true, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.called {
		t.Error("runner must not be called in dry-run mode")
	}
}

func TestRunDryRunPrintsPromptToStdout(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer

	if err := setup.Run(&captureRunner{}, true, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("dry-run must print the setup prompt to stdout")
	}
}

func TestRunDryRunOutputMatchesPromptPipedToRunner(t *testing.T) {
	t.Parallel()
	var dryOut bytes.Buffer
	_ = setup.Run(&captureRunner{}, true, &dryOut, &bytes.Buffer{})

	normal := &captureRunner{}
	_ = setup.Run(normal, false, &bytes.Buffer{}, &bytes.Buffer{})

	if strings.TrimSpace(dryOut.String()) != strings.TrimSpace(normal.stdin) {
		t.Error("dry-run stdout must equal the prompt piped to the runner in normal mode")
	}
}

func TestRunPropagatesRunnerError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("claude not found")

	err := setup.Run(errorRunner{err: sentinel}, false, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}
