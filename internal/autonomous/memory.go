package autonomous

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type MemoryStore struct {
	baseDir string
	locks   map[string]*sync.Mutex
	mu      sync.Mutex
}

func NewMemoryStore(baseDir string) *MemoryStore {
	return &MemoryStore{
		baseDir: baseDir,
		locks:   make(map[string]*sync.Mutex),
	}
}

func (s *MemoryStore) WithLock(agent string, repo string, fn func(memoryPath string, memory string) error) error {
	lock := s.lockFor(agent, repo)
	lock.Lock()
	defer lock.Unlock()

	path, err := s.ensureMemoryFile(agent, repo)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read memory %s: %w", path, err)
	}
	return fn(path, string(content))
}

func (s *MemoryStore) ensureMemoryFile(agent string, repo string) (string, error) {
	dir := filepath.Join(s.baseDir, "autonomous", sanitize(agent), sanitize(repo))
	cleanBase := filepath.Clean(s.baseDir)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanDir+string(filepath.Separator), cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("memory path escapes base dir: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create memory dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "MEMORY.md")
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := os.WriteFile(path, []byte{}, 0o600); writeErr != nil {
			return "", fmt.Errorf("create memory file %s: %w", path, writeErr)
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("stat memory file %s: %w", path, err)
	}
	return path, nil
}

func (s *MemoryStore) lockFor(agent string, repo string) *sync.Mutex {
	key := fmt.Sprintf("%s|%s", sanitize(agent), sanitize(repo))
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock, ok := s.locks[key]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	s.locks[key] = lock
	return lock
}

func sanitize(value string) string {
	value = filepath.Clean(value)
	value = strings.TrimLeft(value, string(filepath.Separator))
	value = strings.ReplaceAll(value, "..", "_")
	value = strings.ReplaceAll(value, string(filepath.Separator), "_")
	value = strings.ReplaceAll(value, "\x00", "_")
	return value
}
