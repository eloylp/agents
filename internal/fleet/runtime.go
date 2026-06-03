package fleet

import "strings"

const (
	DefaultRunnerImage          = "ghcr.io/eloylp/agents-runner:latest"
	DefaultRunnerTimeoutSeconds = 3600
)

// RuntimeSettings describes how the daemon starts ephemeral agent runner
// containers. It is mutable fleet state because operators need to change the
// runner image and resource policy without rebuilding the daemon image.
type RuntimeSettings struct {
	RunnerImage            string                                `yaml:"runner_image,omitempty" json:"runner_image"`
	Constraints            RuntimeConstraints                    `yaml:"constraints,omitempty" json:"constraints"`
	SelfImprovementAnalyst SelfImprovementAnalystRuntimeSettings `yaml:"self_improvement_analyst,omitempty" json:"self_improvement_analyst,omitempty"`
}

// RuntimeConstraints are applied to each runner container where the selected
// runtime supports them.
type RuntimeConstraints struct {
	CPUs           string `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	Memory         string `yaml:"memory,omitempty" json:"memory,omitempty"`
	PidsLimit      int64  `yaml:"pids_limit,omitempty" json:"pids_limit,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	NetworkMode    string `yaml:"network_mode,omitempty" json:"network_mode,omitempty"`
}

// SelfImprovementAnalystRuntimeSettings optionally pins the internal catalog
// analyst to a backend/model pair. Empty backend keeps the existing automatic
// backend selection; empty model lets the runner/backend default or inferred
// fleet model apply.
type SelfImprovementAnalystRuntimeSettings struct {
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
	Model   string `yaml:"model,omitempty" json:"model,omitempty"`
}

func NormalizeRuntimeSettings(s *RuntimeSettings) {
	s.RunnerImage = strings.TrimSpace(s.RunnerImage)
	if s.RunnerImage == "" {
		s.RunnerImage = DefaultRunnerImage
	}
	if s.Constraints.TimeoutSeconds == 0 {
		s.Constraints.TimeoutSeconds = DefaultRunnerTimeoutSeconds
	}
	s.Constraints.CPUs = strings.TrimSpace(s.Constraints.CPUs)
	s.Constraints.Memory = strings.TrimSpace(s.Constraints.Memory)
	s.Constraints.NetworkMode = strings.TrimSpace(s.Constraints.NetworkMode)
	s.SelfImprovementAnalyst.Backend = NormalizeBackendName(s.SelfImprovementAnalyst.Backend)
	s.SelfImprovementAnalyst.Model = strings.TrimSpace(s.SelfImprovementAnalyst.Model)
}
