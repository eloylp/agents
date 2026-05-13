package runtime

import (
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestHostConfigAppliesFilesystemPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		policy          string
		wantRootRO      bool
		wantWorkspaceRO bool
		wantTempRO      bool
		wantErr         bool
	}{
		{name: "default keeps mounts writable"},
		{name: "documented workspace temp policy", policy: "workspace-tmp"},
		{name: "workspace readonly", policy: "workspace-ro", wantRootRO: true, wantWorkspaceRO: true},
		{name: "readonly root policy", policy: "readonly-root", wantRootRO: true},
		{name: "rejects readonly alias", policy: "readonly", wantErr: true},
		{name: "rejects unknown", policy: "surprise", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := hostConfig(ContainerSpec{
				Policy: fleet.RuntimeConstraints{Filesystem: tc.policy},
				Mounts: []Mount{
					{Source: "/host/work", Target: "/workspace"},
					{Source: "/host/tmp", Target: "/tmp/agents-run"},
				},
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("hostConfig error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("hostConfig: %v", err)
			}
			if cfg.ReadonlyRootfs != tc.wantRootRO {
				t.Fatalf("ReadonlyRootfs = %v, want %v", cfg.ReadonlyRootfs, tc.wantRootRO)
			}
			if got := cfg.Mounts[0].ReadOnly; got != tc.wantWorkspaceRO {
				t.Fatalf("workspace ReadOnly = %v, want %v", got, tc.wantWorkspaceRO)
			}
			if got := cfg.Mounts[1].ReadOnly; got != tc.wantTempRO {
				t.Fatalf("temp ReadOnly = %v, want %v", got, tc.wantTempRO)
			}
		})
	}
}
