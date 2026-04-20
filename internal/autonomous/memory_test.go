package autonomous

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMemoryReadEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mem := NewMemoryStore(dir)

	content, err := mem.ReadMemory("architect", "owner/repo")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty initial memory, got %q", content)
	}
}

func TestFileMemoryWriteAndRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mem := NewMemoryStore(dir)

	if err := mem.WriteMemory("architect", "owner/repo", "noted"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}

	content, err := mem.ReadMemory("architect", "owner/repo")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if content != "noted" {
		t.Fatalf("expected %q, got %q", "noted", content)
	}
}

func TestFileMemoryWriteOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mem := NewMemoryStore(dir)

	if err := mem.WriteMemory("coder", "eloylp/agents", "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := mem.WriteMemory("coder", "eloylp/agents", "second"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	content, err := mem.ReadMemory("coder", "eloylp/agents")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if content != "second" {
		t.Fatalf("expected %q after overwrite, got %q", "second", content)
	}
}

func TestFileMemoryPathSanitization(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mem := NewMemoryStore(dir)

	// NormalizeToken sanitizes separators; the file must be created within dir.
	if err := mem.WriteMemory("../etc/passwd", "/tmp/../../evil", "malicious"); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	// Verify the written file lives within the base directory.
	cleanBase := filepath.Clean(dir)
	fb := mem.(*fileMemory)
	path, err := fb.ensureDir("../etc/passwd", "/tmp/../../evil")
	if err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath+string(filepath.Separator), cleanBase+string(filepath.Separator)) {
		t.Fatalf("memory path escaped base dir: %s", path)
	}
}

func TestFileMemoryIsolation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mem := NewMemoryStore(dir)

	if err := mem.WriteMemory("agentA", "repo", "memory-A"); err != nil {
		t.Fatalf("WriteMemory A: %v", err)
	}

	// agentB should see empty memory even though agentA has content.
	content, err := mem.ReadMemory("agentB", "repo")
	if err != nil {
		t.Fatalf("ReadMemory B: %v", err)
	}
	if content != "" {
		t.Fatalf("agentB should have no memory, got %q", content)
	}
}
