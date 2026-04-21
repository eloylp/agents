package autonomous

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eloylp/agents/internal/ai"
)

// MemoryBackend is the interface satisfied by both the file-based and
// SQLite-backed memory implementations. The scheduler calls ReadMemory before
// each run to inject existing memory into the prompt, and WriteMemory after
// each run to persist the agent's returned memory.
type MemoryBackend interface {
	ReadMemory(agent, repo string) (string, error)
	WriteMemory(agent, repo, content string) error
}

// fileMemory is a MemoryBackend that stores per-agent, per-repo memory as a
// plain text file under a base directory. It is used when the daemon is
// started with --config (YAML path). The daemon — not the agent — owns all
// read and write operations; agents never touch the filesystem for memory.
type fileMemory struct {
	baseDir string
}

// NewMemoryStore returns a file-based MemoryBackend rooted at baseDir.
// The name is preserved for backward compatibility with existing call sites.
func NewMemoryStore(baseDir string) MemoryBackend {
	return &fileMemory{baseDir: baseDir}
}

func (m *fileMemory) ReadMemory(agent, repo string) (string, error) {
	path, err := m.ensureDir(agent, repo)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read memory %s: %w", path, err)
	}
	return string(data), nil
}

func (m *fileMemory) WriteMemory(agent, repo, content string) error {
	path, err := m.ensureDir(agent, repo)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write memory %s: %w", path, err)
	}
	return nil
}

func (m *fileMemory) ensureDir(agent, repo string) (string, error) {
	dir := filepath.Join(m.baseDir, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	cleanBase := filepath.Clean(m.baseDir)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanDir+string(filepath.Separator), cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("memory path escapes base dir: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create memory dir %s: %w", dir, err)
	}
	return filepath.Join(dir, "MEMORY.md"), nil
}
