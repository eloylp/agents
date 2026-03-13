package autonomous

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryStoreCreatesPerRepoMemory(t *testing.T) {
	dir := t.TempDir()
	store := NewMemoryStore(dir)
	var capturedPath string
	err := store.WithLock("architect", "owner/repo", func(memoryPath string, memory string) error {
		capturedPath = memoryPath
		if memory != "" {
			t.Fatalf("expected empty initial memory, got %q", memory)
		}
		return os.WriteFile(memoryPath, []byte("noted"), 0o644)
	})
	if err != nil {
		t.Fatalf("with lock: %v", err)
	}
	if capturedPath == "" {
		t.Fatalf("expected memory path")
	}
	if !strings.Contains(capturedPath, "owner_repo") {
		t.Fatalf("expected repo segment sanitized in memory path, got %s", capturedPath)
	}
	content, err := os.ReadFile(capturedPath)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if string(content) != "noted" {
		t.Fatalf("expected memory write to persist, got %q", string(content))
	}

	// Second call should read existing content without recreating.
	err = store.WithLock("architect", "owner/repo", func(memoryPath string, memory string) error {
		if memory != "noted" {
			t.Fatalf("expected existing memory content, got %q", memory)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("with lock second call: %v", err)
	}
}

func TestMemoryStoreRejectsPathEscapes(t *testing.T) {
	dir := t.TempDir()
	store := NewMemoryStore(dir)
	var path string
	err := store.WithLock("../etc/passwd", "/tmp/../../evil", func(memoryPath string, memory string) error {
		path = memoryPath
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cleanBase := filepath.Clean(dir)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath+string(filepath.Separator), cleanBase+string(filepath.Separator)) {
		t.Fatalf("memory path escaped base dir: %s", path)
	}
}
