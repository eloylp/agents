package fleet

import "strings"

const DefaultRunnerImage = "ghcr.io/eloylp/agents-runner:latest"

// RuntimeSettings describes how the daemon starts ephemeral agent runner
// containers. It is mutable fleet state because operators need to change the
// runner image and resource policy without rebuilding the daemon image.
type RuntimeSettings struct {
	RunnerImage string             `yaml:"runner_image,omitempty" json:"runner_image"`
	Constraints RuntimeConstraints `yaml:"constraints,omitempty" json:"constraints"`
}

// RuntimeConstraints are applied to each runner container where the selected
// runtime supports them.
type RuntimeConstraints struct {
	CPUs           string `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	Memory         string `yaml:"memory,omitempty" json:"memory,omitempty"`
	PidsLimit      int64  `yaml:"pids_limit,omitempty" json:"pids_limit,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	NetworkMode    string `yaml:"network_mode,omitempty" json:"network_mode,omitempty"`
	Filesystem     string `yaml:"filesystem,omitempty" json:"filesystem,omitempty"`
}

func NormalizeRuntimeSettings(s *RuntimeSettings) {
	s.RunnerImage = strings.TrimSpace(s.RunnerImage)
	if s.RunnerImage == "" {
		s.RunnerImage = DefaultRunnerImage
	}
	s.Constraints.CPUs = strings.TrimSpace(s.Constraints.CPUs)
	s.Constraints.Memory = strings.TrimSpace(s.Constraints.Memory)
	s.Constraints.NetworkMode = strings.TrimSpace(s.Constraints.NetworkMode)
	s.Constraints.Filesystem = strings.TrimSpace(s.Constraints.Filesystem)
}
