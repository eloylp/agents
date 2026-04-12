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
	called bool
}

func (c *captureRunner) Run(name string, args []string, _ io.Reader, _, _ io.Writer) error {
	c.called = true
	c.name = name
	c.args = args
	return nil
}

// errorRunner always returns the provided error.
type errorRunner struct{ err error }

func (e errorRunner) Run(_ string, _ []string, _ io.Reader, _, _ io.Writer) error { return e.err }

func TestRunCallsClaude(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	if err := setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.called {
		t.Fatal("expected runner to be called")
	}
	if runner.name != "claude" {
		t.Errorf("command: got %q, want %q", runner.name, "claude")
	}
}

func TestRunPassesExpectedFlags(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	_ = setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})

	if len(runner.args) < 1 {
		t.Fatal("expected at least one arg")
	}
	if runner.args[0] != "--dangerously-skip-permissions" {
		t.Errorf("args[0]: got %q, want %q", runner.args[0], "--dangerously-skip-permissions")
	}
}

func TestRunPassesPromptAsArg(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}

	_ = setup.Run(runner, false, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})

	// The embedded prompt must be the last argument, non-empty.
	if len(runner.args) < 2 {
		t.Fatal("expected at least two args: flag + prompt")
	}
	prompt := runner.args[len(runner.args)-1]
	if strings.TrimSpace(prompt) == "" {
		t.Error("embedded setup prompt passed as arg must not be empty")
	}
}

func TestRunForwardsStdin(t *testing.T) {
	t.Parallel()
	var capturedStdin io.Reader
	var forwardRunner forwardStdinRunner
	forwardRunner.capture = func(r io.Reader) { capturedStdin = r }

	fakeStdin := strings.NewReader("user input")
	_ = setup.Run(&forwardRunner, false, fakeStdin, &bytes.Buffer{}, &bytes.Buffer{})

	if capturedStdin != fakeStdin {
		t.Error("Run must forward the caller's stdin to the child process, not replace it")
	}
}

// forwardStdinRunner calls capture with the stdin passed to Run.
type forwardStdinRunner struct {
	capture func(io.Reader)
}

func (f *forwardStdinRunner) Run(_ string, _ []string, stdin io.Reader, _, _ io.Writer) error {
	f.capture(stdin)
	return nil
}

func TestRunDryRunDoesNotCallRunner(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}
	var stdout bytes.Buffer

	if err := setup.Run(runner, true, &bytes.Buffer{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.called {
		t.Error("runner must not be called in dry-run mode")
	}
}

func TestRunDryRunPrintsPromptToStdout(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer

	if err := setup.Run(&captureRunner{}, true, &bytes.Buffer{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("dry-run must print the setup prompt to stdout")
	}
}

func TestRunDryRunOutputMatchesPromptArg(t *testing.T) {
	t.Parallel()
	var dryOut bytes.Buffer
	_ = setup.Run(&captureRunner{}, true, &bytes.Buffer{}, &dryOut, &bytes.Buffer{})

	normal := &captureRunner{}
	_ = setup.Run(normal, false, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})

	promptArg := normal.args[len(normal.args)-1]
	if strings.TrimSpace(dryOut.String()) != strings.TrimSpace(promptArg) {
		t.Error("dry-run stdout must equal the prompt passed as arg in normal mode")
	}
}

func TestRunPropagatesRunnerError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("claude not found")

	err := setup.Run(errorRunner{err: sentinel}, false, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}
