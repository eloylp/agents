package autonomous

import (
	"os"
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
}
