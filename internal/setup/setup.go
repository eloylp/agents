package setup

import (
	_ "embed"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
// When dryRun is false the prompt is piped into:
//
//	claude -p --dangerously-skip-permissions
//
// which launches an interactive Claude session that guides the user through
// the full setup flow.
func Run(runner CommandRunner, dryRun bool, stdout, stderr io.Writer) error {
	if dryRun {
		_, err := fmt.Fprint(stdout, embeddedPrompt)
		return err
	}
	return runner.Run(
		"claude",
		[]string{"-p", "--dangerously-skip-permissions"},
		strings.NewReader(embeddedPrompt),
		stdout,
		stderr,
	)
}
