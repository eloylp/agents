package config

import (
	"strings"
	"testing"
)

// TestAgentIsAllowMemoryDefaultsTrue confirms that an AgentDef with no
// AllowMemory field set (the YAML "absent" case) reports true so existing
// agents authored before the field existed keep their previous behaviour.
func TestAgentIsAllowMemoryDefaultsTrue(t *testing.T) {
	t.Parallel()
	a := AgentDef{Name: "reviewer"}
	if !a.IsAllowMemory() {
		t.Errorf("AllowMemory nil should report true, got false")
	}
	tt := true
	a.AllowMemory = &tt
	if !a.IsAllowMemory() {
		t.Errorf("AllowMemory=&true should report true, got false")
	}
	ff := false
	a.AllowMemory = &ff
	if a.IsAllowMemory() {
		t.Errorf("AllowMemory=&false should report false, got true")
	}
}

// TestLoadAllowMemoryFalseRoundTrips confirms that an explicit
// `allow_memory: false` survives YAML loading and surfaces on the agent so the
// scheduler can gate memory load+persist accordingly.
func TestLoadAllowMemoryFalseRoundTrips(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	yaml := agentConfigYAML(`  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
    allow_memory: false`)
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, ok := cfg.AgentByName("reviewer")
	if !ok {
		t.Fatal("reviewer not found")
	}
	if a.AllowMemory == nil {
		t.Fatal("AllowMemory pointer should be non-nil after explicit false in YAML")
	}
	if a.IsAllowMemory() {
		t.Errorf("IsAllowMemory: got true, want false")
	}
}

// TestLoadAllowMemoryDefaultsTrueWhenAbsent confirms that omitting the field
// in YAML leaves AllowMemory nil and IsAllowMemory reports the documented
// default of true — the historical behaviour for autonomous agents.
func TestLoadAllowMemoryDefaultsTrueWhenAbsent(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	path := writeConfig(t, minimalYAML(""))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, ok := cfg.AgentByName("reviewer")
	if !ok {
		t.Fatal("reviewer not found")
	}
	if a.AllowMemory != nil {
		t.Errorf("AllowMemory should remain nil when absent from YAML, got pointer to %v", *a.AllowMemory)
	}
	if !a.IsAllowMemory() {
		t.Errorf("IsAllowMemory: got false, want true (default)")
	}
}

// TestLoadAllowMemoryTrueExplicitlyHonoured confirms that `allow_memory: true`
// also round-trips and is treated identically to the default — no surprises
// when an operator chooses to be explicit.
func TestLoadAllowMemoryTrueExplicitlyHonoured(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	yaml := agentConfigYAML(`  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
    allow_memory: true`)
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, ok := cfg.AgentByName("reviewer")
	if !ok {
		t.Fatal("reviewer not found")
	}
	if !a.IsAllowMemory() {
		t.Errorf("IsAllowMemory: got false, want true")
	}
}

// TestLoadAllowMemoryRejectsNonBoolean confirms that a non-boolean YAML value
// fails the parse cleanly rather than silently coercing. Note: yaml.v3 still
// honours YAML 1.1 boolean aliases (`yes`/`no`/`on`/`off`) and decodes them as
// true/false, so we use an unambiguously non-boolean string here.
func TestLoadAllowMemoryRejectsNonBoolean(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	yaml := agentConfigYAML(`  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
    allow_memory: maybe`)
	path := writeConfig(t, yaml)
	if _, err := Load(path); err == nil {
		t.Fatal("expected YAML parse error for non-boolean allow_memory, got nil")
	} else if !strings.Contains(err.Error(), "parse") && !strings.Contains(err.Error(), "bool") && !strings.Contains(err.Error(), "yaml") {
		t.Logf("error message: %v", err)
	}
}
