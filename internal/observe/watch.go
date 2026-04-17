package observe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MemoryChangeEvent is the SSE payload published when a memory file changes.
type MemoryChangeEvent struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
	Path  string `json:"path"` // relative to memory_dir, e.g. "coder/owner_repo.md"
}

// WatchMemoryDir polls dir every interval and publishes a MemoryChangeEvent to
// hub whenever a markdown file's modification time changes. The first scan
// seeds the baseline; only subsequent changes trigger publications. The goroutine
// runs until ctx is cancelled.
//
// If interval is <= 0, it defaults to 2 seconds.
func WatchMemoryDir(ctx context.Context, dir string, interval time.Duration, hub *SSEHub) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// known maps relative file path → last observed mtime.
	known := make(map[string]time.Time)

	scan := func() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			mtime := info.ModTime()
			rel, _ := filepath.Rel(dir, path)
			prev, seen := known[rel]
			known[rel] = mtime
			if seen && mtime.After(prev) {
				// File has been modified since last scan — publish.
				ev := buildMemoryChangeEvent(rel)
				if b, err := sseData(ev); err == nil {
					hub.Publish(b)
				}
			}
			return nil
		})
	}

	// Seed the baseline on first tick so subsequent changes are detectable.
	scan()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan()
		}
	}
}

// buildMemoryChangeEvent constructs a MemoryChangeEvent from a relative path
// such as "agentname/owner_repo.md".
func buildMemoryChangeEvent(rel string) MemoryChangeEvent {
	ev := MemoryChangeEvent{Path: rel}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 2 {
		ev.Agent = parts[0]
		ev.Repo = strings.TrimSuffix(parts[1], ".md")
	}
	return ev
}
