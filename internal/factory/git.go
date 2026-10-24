package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is the clone the run works in. The factory never touches the base checkout's
// working tree: it only adds and removes worktrees of it, and pushes branches.
type Git interface {
	// Pull brings the clone's main up to date with origin/main.
	Pull(ctx context.Context) error
	// AddWorktree creates a worktree at path holding a new branch off origin/main.
	AddWorktree(ctx context.Context, branch, path string) error
	// RemoveWorktree removes the worktree at path (--force).
	RemoveWorktree(ctx context.Context, path string) error
	// DeleteBranch deletes a local branch of the clone (-D).
	DeleteBranch(ctx context.Context, branch string) error
	// Worktrees lists the clone's worktrees.
	Worktrees(ctx context.Context) ([]Worktree, error)
	// Push pushes a branch of the clone to origin.
	Push(ctx context.Context, branch string) error
	// Diff reads what a branch changes against origin/main.
	Diff(ctx context.Context, branch string) (Diff, error)
	// PublishEvidence pushes files to ref, from a throwaway repository, so a branch
	// that is never merged can carry them.
	PublishEvidence(ctx context.Context, ref string, files []string) error
}

// Worktree is one git worktree of the clone.
type Worktree struct {
	// Path is where the worktree is, absolute.
	Path string
	// Branch is the branch checked out there, without refs/heads/. A detached
	// worktree has none, and belongs to no run.
	Branch string
}

// Diff is what one branch changes against the base.
type Diff struct {
	Files      []string
	Insertions int
	Deletions  int
}

// CLIGit runs git in the clone. Home, when it is set, is HOME for the commands that talk
// to origin (pull, push, the evidence push): the repository is private, git signs in
// through the gh credential helper, and only the publisher profile's home carries that
// login. Every other command runs with the environment the factory was started with.
type CLIGit struct {
	Clone    string
	Home     string
	commands runner
}

func (g *CLIGit) Pull(ctx context.Context) error {
	_, err := g.commands.run(ctx, g.pushEnv(), "", "git", "-C", g.Clone, "pull", "--ff-only", "origin", "main")
	return err
}

func (g *CLIGit) AddWorktree(ctx context.Context, branch, path string) error {
	_, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "worktree", "add", "-b", branch, path, "origin/main")
	return err
}

func (g *CLIGit) RemoveWorktree(ctx context.Context, path string) error {
	_, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "worktree", "remove", "--force", path)
	return err
}

func (g *CLIGit) DeleteBranch(ctx context.Context, branch string) error {
	_, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "branch", "-D", branch)
	return err
}

func (g *CLIGit) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out), nil
}

func (g *CLIGit) Push(ctx context.Context, branch string) error {
	_, err := g.commands.run(ctx, g.pushEnv(), "", "git", "-C", g.Clone, "push", "origin", branch)
	return err
}

func (g *CLIGit) Diff(ctx context.Context, branch string) (Diff, error) {
	// The three dots compare against the branch point, so a merge of main into the
	// branch does not show up as a change of its own.
	out, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "diff", "--numstat", "origin/main..."+branch)
	if err != nil {
		return Diff{}, err
	}
	return parseNumstat(out), nil
}

// PublishEvidence makes a throwaway repository, puts the files in it and pushes one
// commit to ref: the project's history is never touched, and the branch can be deleted
// afterwards without anything else noticing. This is the route's own trick, kept
// because GitHub takes no image upload through its API.
func (g *CLIGit) PublishEvidence(ctx context.Context, ref string, files []string) error {
	origin, err := g.commands.run(ctx, nil, "", "git", "-C", g.Clone, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "factory-evidence-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, file := range files {
		if err := copyFile(file, filepath.Join(dir, filepath.Base(file))); err != nil {
			return err
		}
	}
	steps := [][]string{
		{"-C", dir, "init", "-q", "-b", "main"},
		{"-C", dir, "add", "."},
		{"-C", dir, "-c", "user.name=Hermes", "-c", "user.email=hermes@users.noreply.github.com",
			"commit", "-q", "-m", "evidence for " + ref},
		{"-C", dir, "push", "-f", strings.TrimSpace(origin), "HEAD:refs/heads/" + ref},
	}
	for _, args := range steps {
		if _, err := g.commands.run(ctx, g.pushEnv(), "", "git", args...); err != nil {
			return err
		}
	}
	return nil
}

// copyFile puts one screenshot in the throwaway repository.
func copyFile(from, to string) error {
	content, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, content, 0o644)
}

func (g *CLIGit) pushEnv() []string {
	if g.Home == "" {
		return nil
	}
	return []string{"HOME=" + g.Home}
}

// parseWorktrees reads `git worktree list --porcelain`, whose blocks are the
// worktrees and their lines the fields.
func parseWorktrees(out string) []Worktree {
	var trees []Worktree
	var current Worktree
	flush := func() {
		if current.Path != "" {
			trees = append(trees, current)
		}
		current = Worktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current.Path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		}
	}
	flush()
	return trees
}

// parseNumstat reads `git diff --numstat`: added, deleted and path, tab separated.
// A binary file reports a dash instead of a count.
func parseNumstat(out string) Diff {
	var diff Diff
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		diff.Files = append(diff.Files, fields[2])
		diff.Insertions += atoi(fields[0])
		diff.Deletions += atoi(fields[1])
	}
	return diff
}

// atoi reads a count, and counts a dash (a binary file) as nothing.
func atoi(field string) int {
	value := 0
	for _, digit := range field {
		if digit < '0' || digit > '9' {
			return 0
		}
		value = value*10 + int(digit-'0')
	}
	return value
}

// runner runs one external command. Every command any of the CLI clients makes goes
// through it, so a test can record the flags without running anything — which is what
// keeps a wrong flag from reaching Basecamp.
type runner interface {
	// run runs one command and returns its standard output. env is added on top of the
	// environment the factory was started with; stdin is what the command reads, and it
	// is always set, so a command that wants to prompt fails instead of hanging.
	run(ctx context.Context, env []string, stdin, name string, args ...string) (string, error)
}

// shell runs the commands the CLI clients are made of.
type shell struct{}

func (shell) run(ctx context.Context, env []string, stdin, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = mergeEnv(os.Environ(), env)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, message)
	}
	return stdout.String(), nil
}

// mergeEnv overlays the overrides on the base environment without repeating a key: a
// key that appears twice is not reliably the one the program reads.
func mergeEnv(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	var order []string
	for _, entry := range append(append([]string{}, base...), overrides...) {
		key, _, _ := strings.Cut(entry, "=")
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = entry
	}
	merged := make([]string, 0, len(order))
	for _, key := range order {
		merged = append(merged, values[key])
	}
	return merged
}
