package runtime

import (
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestHostConfigAppliesRuntimeConstraints(t *testing.T) {
	t.Parallel()

	cfg, err := hostConfig(ContainerSpec{
		Policy: fleet.RuntimeConstraints{
			CPUs:        "0.5",
			Memory:      "512m",
			PidsLimit:   128,
			NetworkMode: "none",
		},
	})
	if err != nil {
		t.Fatalf("hostConfig: %v", err)
	}
	if string(cfg.NetworkMode) != "none" {
		t.Fatalf("NetworkMode = %q, want none", cfg.NetworkMode)
	}
	if cfg.Resources.NanoCPUs != 500_000_000 {
		t.Fatalf("NanoCPUs = %d, want 500000000", cfg.Resources.NanoCPUs)
	}
	if cfg.Resources.Memory == 0 {
		t.Fatal("Memory = 0, want parsed memory limit")
	}
	if cfg.Resources.PidsLimit == nil || *cfg.Resources.PidsLimit != 128 {
		t.Fatalf("PidsLimit = %v, want 128", cfg.Resources.PidsLimit)
	}
}
