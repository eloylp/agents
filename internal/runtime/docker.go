package runtime

import (
	"context"
	"errors"

	"github.com/docker/docker/client"
)

var ErrDockerRunNotImplemented = errors.New("docker runtime run is not wired yet")

type Docker struct {
	client *client.Client
}

func NewDocker() (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Docker{client: cli}, nil
}

func (d *Docker) EnsureImage(context.Context, string) error {
	return nil
}

func (d *Docker) Run(context.Context, ContainerSpec) (ExitStatus, error) {
	return ExitStatus{}, ErrDockerRunNotImplemented
}
