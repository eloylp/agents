package store_test

import (
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// TestImportLoadAgentAllowMemoryDefaultsTrue verifies that an agent imported
// without an explicit AllowMemory field round-trips through SQLite as the
// documented default — IsAllowMemory() reports true on read. This protects
// against silently flipping the default when migrations or load paths change.
func TestImportLoadAgentAllowMemoryDefaultsTrue(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	in := minimalCfg()
	// Sanity: the fixture leaves AllowMemory nil for both seeded agents.
	for _, a := range in.Agents {
		if a.AllowMemory != nil {
			t.Fatalf("test fixture changed: %s already has AllowMemory set", a.Name)
		}
	}
	if err := store.Import(db, in); err != nil {
		t.Fatalf("import: %v", err)
	}
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out.Agents) == 0 {
		t.Fatal("expected agents after Load")
	}
	for _, a := range out.Agents {
		if !a.IsAllowMemory() {
			t.Errorf("agent %q: IsAllowMemory()=false after default import, want true", a.Name)
		}
	}
}

// TestImportLoadAgentAllowMemoryFalseRoundTrips verifies that an explicit
// non-nil false survives an Import → Load round trip — the row must store 0
// and the loader must materialise &false so IsAllowMemory() reports false.
func TestImportLoadAgentAllowMemoryFalseRoundTrips(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	in := minimalCfg()
	ff := false
	in.Agents[0].AllowMemory = &ff
	if err := store.Import(db, in); err != nil {
		t.Fatalf("import: %v", err)
	}
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var got *bool
	target := in.Agents[0].Name
	for _, a := range out.Agents {
		if a.Name == target {
			got = a.AllowMemory
		}
	}
	if got == nil {
		t.Fatalf("agent %q: AllowMemory pointer is nil after load, want non-nil &false", target)
	}
	if *got {
		t.Errorf("agent %q: AllowMemory=true after round-trip, want false", target)
	}
}

// TestUpsertAgentRoundTripsAllowMemoryFalse exercises the per-item
// UpsertAgent path used by REST/MCP CRUD: writing an agent with
// AllowMemory=&false should persist as 0 and reload as IsAllowMemory()=false.
func TestUpsertAgentRoundTripsAllowMemoryFalse(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	ff := false
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:        "coder",
		Backend:     "claude",
		Prompt:      "You write code.",
		Skills:      []string{"architect"},
		AllowMemory: &ff,
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var got *bool
	for _, a := range out.Agents {
		if a.Name == "coder" {
			got = a.AllowMemory
		}
	}
	if got == nil || *got {
		t.Errorf("coder.AllowMemory after upsert: got %v, want non-nil &false", got)
	}
}
