package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-units"
	"github.com/rs/zerolog"
)

type Docker struct {
	client *client.Client
	logger zerolog.Logger
}

func NewDocker(logger zerolog.Logger) (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Docker{client: cli, logger: logger}, nil
}

func (d *Docker) EnsureImage(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("runner image is required")
	}
	if _, _, err := d.client.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect runner image %q: %w", ref, err)
	}
	rc, err := d.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull runner image %q: %w", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("read runner image pull stream %q: %w", ref, err)
	}
	return nil
}

func (d *Docker) Diagnose(ctx context.Context, ref string) Diagnostic {
	if _, err := d.client.Ping(ctx); err != nil {
		return Diagnostic{Detail: "docker unavailable: " + err.Error()}
	}
	out := Diagnostic{DockerAvailable: true, Detail: "docker available"}
	if strings.TrimSpace(ref) == "" {
		out.Detail = "runner image is required"
		return out
	}
	if _, _, err := d.client.ImageInspectWithRaw(ctx, ref); err != nil {
		out.Detail = fmt.Sprintf("runner image %q not present locally: %v", ref, err)
		return out
	}
	out.ImageAvailable = true
	out.Detail = fmt.Sprintf("runner image %q present locally", ref)
	return out
}

func (d *Docker) Run(ctx context.Context, spec ContainerSpec) (ExitStatus, error) {
	if spec.Image == "" {
		return ExitStatus{}, errors.New("runner image is required")
	}
	if len(spec.Command) == 0 {
		return ExitStatus{}, errors.New("runner command is required")
	}
	if err := d.EnsureImage(ctx, spec.Image); err != nil {
		return ExitStatus{}, err
	}

	cfg := &container.Config{
		Image:        spec.Image,
		Cmd:          spec.Command,
		WorkingDir:   spec.WorkingDir,
		Env:          spec.Env,
		Labels:       spec.Labels,
		AttachStdout: spec.Stdout != nil,
		AttachStderr: spec.Stderr != nil,
		AttachStdin:  spec.Stdin != nil,
		OpenStdin:    spec.Stdin != nil,
		StdinOnce:    spec.Stdin != nil,
	}
	hostCfg, err := hostConfig(spec)
	if err != nil {
		return ExitStatus{}, err
	}
	created, err := d.client.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		return ExitStatus{}, fmt.Errorf("create runner container: %w", err)
	}
	containerID := created.ID
	remove := func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := d.client.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			d.logger.Warn().Err(err).Str("container", containerID).Msg("remove runner container")
		}
	}
	defer remove()

	attach, err := d.client.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  spec.Stdin != nil,
		Stdout: spec.Stdout != nil,
		Stderr: spec.Stderr != nil,
	})
	if err != nil {
		return ExitStatus{}, fmt.Errorf("attach runner container: %w", err)
	}
	defer attach.Close()

	copyDone := make(chan error, 1)
	go func() {
		if spec.Stdin != nil {
			_, _ = io.Copy(attach.Conn, spec.Stdin)
			_ = attach.CloseWrite()
		}
		stdout := spec.Stdout
		if stdout == nil {
			stdout = io.Discard
		}
		stderr := spec.Stderr
		if stderr == nil {
			stderr = io.Discard
		}
		_, err := stdcopy.StdCopy(stdout, stderr, attach.Reader)
		copyDone <- err
	}()

	if err := d.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return ExitStatus{}, fmt.Errorf("start runner container: %w", err)
	}
	waitCh, errCh := d.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	var status container.WaitResponse
	select {
	case <-ctx.Done():
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.client.ContainerStop(stopCtx, containerID, container.StopOptions{})
		return ExitStatus{}, ctx.Err()
	case err := <-errCh:
		if err != nil {
			return ExitStatus{}, fmt.Errorf("wait runner container: %w", err)
		}
	case status = <-waitCh:
	}
	if err := <-copyDone; err != nil && !errors.Is(err, io.EOF) {
		return ExitStatus{}, fmt.Errorf("copy runner output: %w", err)
	}
	return ExitStatus{Code: int(status.StatusCode)}, nil
}

func hostConfig(spec ContainerSpec) (*container.HostConfig, error) {
	cfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(spec.Policy.NetworkMode),
	}
	if spec.Policy.NetworkMode == "" {
		cfg.NetworkMode = "bridge"
	}
	filesystem := strings.ToLower(strings.TrimSpace(spec.Policy.Filesystem))
	switch filesystem {
	case "", "workspace-tmp":
	case "readonly-root", "workspace-ro":
		cfg.ReadonlyRootfs = true
	default:
		return nil, fmt.Errorf("unsupported filesystem policy %q", spec.Policy.Filesystem)
	}
	for _, m := range spec.Mounts {
		readOnly := m.ReadOnly || filesystem == "workspace-ro"
		if m.Target == RunnerTempMount {
			readOnly = false
		}
		if m.Tmpfs || m.Source == "" {
			cfg.Mounts = append(cfg.Mounts, mount.Mount{
				Type:     mount.TypeTmpfs,
				Target:   m.Target,
				ReadOnly: readOnly,
			})
			continue
		}
		cfg.Mounts = append(cfg.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: readOnly,
		})
	}
	if spec.Policy.PidsLimit > 0 {
		cfg.Resources.PidsLimit = &spec.Policy.PidsLimit
	}
	if spec.Policy.Memory != "" {
		mem, err := units.RAMInBytes(spec.Policy.Memory)
		if err != nil {
			return nil, fmt.Errorf("parse memory limit %q: %w", spec.Policy.Memory, err)
		}
		cfg.Resources.Memory = mem
	}
	if spec.Policy.CPUs != "" {
		cpus, err := strconv.ParseFloat(spec.Policy.CPUs, 64)
		if err != nil || cpus <= 0 {
			return nil, fmt.Errorf("parse cpu limit %q: %w", spec.Policy.CPUs, err)
		}
		cfg.Resources.NanoCPUs = int64(cpus * 1_000_000_000)
	}
	return cfg, nil
}
