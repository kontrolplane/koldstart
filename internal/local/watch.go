package local

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// skipped are the directories a change in does not rebuild anything, and sources the files that
// go into a build: a change to a log or an editor's backup is no reason to rebuild.
var (
	skipped = []string{"node_modules", ".koldstart", ".git", "dist", "bin"}
	sources = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".mjs", ".cjs", ".jsx", ".json", ".go", ".mod", ".sum"}
)

// Watch calls changed when a file under root changes, looking every interval, until ctx ends.
func Watch(ctx context.Context, root string, interval time.Duration, changed func()) {
	last := snapshot(root)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := snapshot(root)
			if now != last {
				last = now
				changed()
			}
		}
	}
}

// snapshot joins the path and modification time of each source file under root, so two
// snapshots differ when a source changed.
func snapshot(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root {
			if slices.Contains(skipped, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !slices.Contains(sources, filepath.Ext(p)) {
			return nil
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			b.WriteString(p)
			b.WriteString(info.ModTime().String())
		}
		return nil
	})
	return b.String()
}
