package factory

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScreenshotDir is where the rules tell a card to save what it looked at, relative to
// its worktree. It is never committed: a screenshot travels on its own branch, or not
// at all.
const ScreenshotDir = "tmp/evidence"

// Workspaces is the files the cards left in their worktrees — the fourth seam, and the
// only one that runs no command. A screenshot is a file, and the pull request's Evidence
// section is made of them.
type Workspaces interface {
	// Screenshots lists the PNG files under tmp/evidence of the given worktrees, in
	// the order the directories were given. A worktree that is not there, or that has
	// no evidence, contributes nothing and is not an error.
	Screenshots(ctx context.Context, worktrees []string) ([]string, error)
}

// WorktreeFiles reads them off the disk.
type WorktreeFiles struct{}

func (WorktreeFiles) Screenshots(_ context.Context, worktrees []string) ([]string, error) {
	var found []string
	for _, worktree := range worktrees {
		dir := filepath.Join(worktree, ScreenshotDir)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var here []string
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".png") {
				continue
			}
			here = append(here, filepath.Join(dir, entry.Name()))
		}
		// Each worktree keeps its own order: the run's worktree comes first, and its
		// copy of a name is the one the integrator gathered.
		sort.Strings(here)
		found = append(found, here...)
	}
	return found, nil
}
