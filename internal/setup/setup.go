package setup

import (
	_ "embed"
	"fmt"
	"io"
	"os/exec"
)

//go:embed prompt.md
var embeddedPrompt string

// CommandRunner executes an external command with the given name, args, and I/O streams.
// It is an interface so callers can inject a test double.
type CommandRunner interface {
	Run(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// NewCommandRunner returns a CommandRunner backed by os/exec.
func NewCommandRunner() CommandRunner {
	return execCommandRunner{}
}

type execCommandRunner struct{}

func (execCommandRunner) Run(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Run executes the agents setup wizard.
//
// When dryRun is true the embedded setup prompt is printed to stdout and the
// runner is not called — useful for inspecting the prompt or scripting.
//
// When dryRun is false the embedded prompt is passed as a positional argument to:
//
//	claude --dangerously-skip-permissions <prompt>
//
// Passing the prompt as a CLI argument (rather than via stdin) preserves the
// caller's stdin so the interactive Claude session can receive user input
// throughout the multi-phase setup flow.
func Run(runner CommandRunner, dryRun bool, stdin io.Reader, stdout, stderr io.Writer) error {
	if dryRun {
		_, err := fmt.Fprint(stdout, embeddedPrompt)
		return err
	}
	return runner.Run(
		"claude",
		[]string{"--dangerously-skip-permissions", embeddedPrompt},
		stdin,
		stdout,
		stderr,
	)
}
