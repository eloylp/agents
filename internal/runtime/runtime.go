// Package runtime owns the execution-plane abstraction used to run agents in
// fresh containers. Workflow code should depend on these types rather than on
// Docker SDK types directly.
package runtime

import (
	"context"
	"io"

	"github.com/eloylp/agents/internal/fleet"
)

const RunnerTempMount = "/tmp/agents-run"

type ContainerSpec struct {
	Image      string
	Command    []string
	WorkingDir string
	Env        []string
	Labels     map[string]string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Policy     fleet.RuntimeConstraints
}

type ExitStatus struct {
	Code int
}

type Runner interface {
	EnsureImage(ctx context.Context, image string) error
	Run(ctx context.Context, spec ContainerSpec) (ExitStatus, error)
}

type Diagnostic struct {
	DockerAvailable bool
	ImageAvailable  bool
	Detail          string
}
