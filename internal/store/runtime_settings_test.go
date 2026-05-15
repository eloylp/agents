package store_test

import (
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func TestRuntimeSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st := store.New(db)
	initial, err := st.ReadRuntimeSettings()
	if err != nil {
		t.Fatalf("ReadRuntimeSettings initial: %v", err)
	}
	if initial.RunnerImage != fleet.DefaultRunnerImage {
		t.Fatalf("initial runner image = %q, want %q", initial.RunnerImage, fleet.DefaultRunnerImage)
	}

	updated, err := st.WriteRuntimeSettings(fleet.RuntimeSettings{
		RunnerImage: "ghcr.io/example/custom-runner:v1",
		Constraints: fleet.RuntimeConstraints{
			CPUs:           "2",
			Memory:         "4g",
			PidsLimit:      256,
			TimeoutSeconds: 900,
			NetworkMode:    "bridge",
		},
	})
	if err != nil {
		t.Fatalf("WriteRuntimeSettings: %v", err)
	}

	got, err := st.ReadRuntimeSettings()
	if err != nil {
		t.Fatalf("ReadRuntimeSettings updated: %v", err)
	}
	if got != updated {
		t.Fatalf("runtime settings = %+v, want %+v", got, updated)
	}
}

func TestPatchRuntimeSettingsPreservesOmittedFields(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime-patch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st := store.New(db)
	_, err = st.WriteRuntimeSettings(fleet.RuntimeSettings{
		RunnerImage: "ghcr.io/example/custom-runner:v1",
		Constraints: fleet.RuntimeConstraints{
			CPUs:           "2",
			Memory:         "4g",
			PidsLimit:      256,
			TimeoutSeconds: 900,
			NetworkMode:    "bridge",
		},
	})
	if err != nil {
		t.Fatalf("WriteRuntimeSettings: %v", err)
	}

	timeout := 1200
	cpus := ""
	network := ""
	updated, err := st.PatchRuntimeSettings(store.RuntimeSettingsPatch{
		Constraints: store.RuntimeConstraintsPatch{
			CPUs:           &cpus,
			TimeoutSeconds: &timeout,
			NetworkMode:    &network,
		},
	})
	if err != nil {
		t.Fatalf("PatchRuntimeSettings: %v", err)
	}

	want := fleet.RuntimeSettings{
		RunnerImage: "ghcr.io/example/custom-runner:v1",
		Constraints: fleet.RuntimeConstraints{
			CPUs:           "",
			Memory:         "4g",
			PidsLimit:      256,
			TimeoutSeconds: 1200,
			NetworkMode:    "",
		},
	}
	if updated != want {
		t.Fatalf("patched runtime settings = %+v, want %+v", updated, want)
	}
}

func TestWorkspaceRunnerImageRoundTrip(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "workspace-runtime.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st := store.New(db)
	if _, err := st.UpsertWorkspace(fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	updated, err := st.SetWorkspaceRunnerImage("team-a", "ghcr.io/example/team-runner:v2")
	if err != nil {
		t.Fatalf("SetWorkspaceRunnerImage: %v", err)
	}
	if updated.RunnerImage != "ghcr.io/example/team-runner:v2" {
		t.Fatalf("updated runner image = %q", updated.RunnerImage)
	}
	read, err := st.ReadWorkspace("team-a")
	if err != nil {
		t.Fatalf("ReadWorkspace: %v", err)
	}
	if read.RunnerImage != updated.RunnerImage {
		t.Fatalf("read runner image = %q, want %q", read.RunnerImage, updated.RunnerImage)
	}
}
