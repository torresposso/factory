// Package factory turns a GitHub spec and its tickets into Hermes kanban cards, and
// the finished branch back into one pull request. It is the Go program that replaced
// the orchestrator's prose prompt (README.md).
//
// Everything it touches outside the program — git, GitHub, the board, the disk —
// goes through four seams (Git, GitHub, Board and Workspaces), so the tests replace them with fakes
// and `go test ./...` needs no network, no board, no database and no environment.
package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Options is where a run happens. DetectOptions reads them from the checkout the
// factory is started in, and every one of them can be overridden by a flag.
type Options struct {
	// Repo is the GitHub repository, owner/name.
	Repo string
	// Clone is the base checkout. The run's worktrees are branches of it, and it is
	// where the branch is pushed from.
	Clone string
	// Trees is where the run's worktrees live, one directory per ticket.
	Trees string
	// Board is the kanban board. Never the current board: that file is shared by
	// every profile, and the factory always names the board it means.
	Board string
	// Home is HOME for the commands that talk to GitHub. The hermes user's default
	// HOME carries no gh login; the publisher profile's does.
	Home string
}

// DefaultHome is the publisher profile's home in Basecamp's container, whose gh login is
// the one that can push.
const DefaultHome = "/opt/data/profiles/publisher/home"

// DetectOptions reads where a run happens from the checkout dir is in, so one binary
// serves every repository: the repository is origin's, the clone is the main checkout
// (also from a worktree, which is where a card's hooks run), the worktrees sit
// beside it in <clone>-worktrees, and the board is named after the repository. The
// hooks read them from their card's workspace instead (HookOptions).
func DetectOptions(ctx context.Context, dir string) (Options, error) {
	return detectOptions(ctx, shell{}, dir)
}

func detectOptions(ctx context.Context, commands runner, dir string) (Options, error) {
	opts := Options{Home: DefaultHome}
	origin, err := commands.run(ctx, nil, "", "git", "-C", dir, "remote", "get-url", "origin")
	if err != nil {
		return opts, err
	}
	if opts.Repo, err = repoOf(strings.TrimSpace(origin)); err != nil {
		return opts, err
	}
	// The common dir is the main checkout's .git whichever worktree dir is in; the
	// top level would be the worktree itself.
	common, err := commands.run(ctx, nil, "", "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return opts, err
	}
	opts.Clone = filepath.Dir(strings.TrimSpace(common))
	opts.Trees = opts.Clone + "-worktrees"
	opts.Board = boardOf(opts.Repo)
	return opts, nil
}

// repoOf reads owner/name from a GitHub remote, in any of the shapes git accepts:
// https://github.com/o/n.git, git@github.com:o/n.git, ssh://git@github.com/o/n.
func repoOf(remote string) (string, error) {
	path := remote
	if _, rest, ok := strings.Cut(remote, "://"); ok {
		_, path, _ = strings.Cut(rest, "/")
	} else if _, rest, ok := strings.Cut(remote, ":"); ok {
		path = rest
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("origin %q is not a GitHub repository owner/name", remote)
	}
	return path, nil
}

// boardOf is the board slug a repository's cards live on: its name, lowercase, with
// whatever a slug cannot hold as a dash (torresposso.site is torresposso-site).
func boardOf(repo string) string {
	name := strings.ToLower(repo[strings.LastIndex(repo, "/")+1:])
	return strings.Map(func(char rune) rune {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			return char
		}
		return '-'
	}, name)
}

// Factory runs one phase. Its seams are what the tests replace; the CLI types behind
// them (CLIGit, CLIGitHub, CLIBoard, WorktreeFiles) run the real commands and read the
// real disk.
//
// Nothing here prints and nothing here asks whether it is a dry run: Plan reads the
// world and returns a list, Apply makes that list true, and the command decides which
// of the two a `--dry-run` gets. That is what makes a dry run safe to describe.
type Factory struct {
	Git        Git
	GitHub     GitHub
	Board      Board
	Workspaces Workspaces
	Opts       Options
	// URL is the pull request a publish opened or found; empty until Apply ran it.
	URL string

	cards map[string]string // card key -> board card id, filled as cards are created
}

// New wires the factory to the real git, GitHub and board.
func New(opts Options) *Factory {
	commands := shell{}
	return &Factory{
		Git:        &CLIGit{Clone: opts.Clone, Home: opts.Home, commands: commands},
		GitHub:     &CLIGitHub{Repo: opts.Repo, Home: opts.Home, commands: commands},
		Board:      &CLIBoard{Board: opts.Board, commands: commands},
		Workspaces: WorktreeFiles{},
		Opts:       opts,
	}
}

// Plan is every change one phase makes, in the order it makes them. Plan() reads
// GitHub and the board and returns this list; Apply() is what runs it.
type Plan struct {
	// Issue is the issue the run was asked for: a spec, or a lone ticket.
	Issue int
	// Steps are the changes, in order. Card steps name their parents by key, because
	// the board ids only exist once the earlier cards are created.
	Steps []Step
}

// Step is one change. String() is the line the operator reads; Apply runs it.
type Step interface{ String() string }

// PullStep brings the base checkout up to date with origin/main.
type PullStep struct{}

// AddWorktreeStep creates a worktree holding a new branch off origin/main.
type AddWorktreeStep struct{ Branch, Path string }

// RemoveWorktreeStep removes a worktree, with the local branch that is checked out
// there. A run that starts clean does not care whether the folder had work in it.
type RemoveWorktreeStep struct{ Path, Branch string }

// ArchiveCardsStep archives the cards of an earlier run of the same issue. Archiving
// a parent releases its children, so a run is archived as a whole.
type ArchiveCardsStep struct{ IDs []string }

// CreateCardStep creates one card and remembers its board id under Key, so a later
// card can name it as a parent or in its body.
type CreateCardStep struct {
	// Key names the card inside this run: "code:57", "integrate", "review".
	Key string
	// Card is what the board is asked for. Its Body is empty: Body() builds it.
	Card NewCard
	// Parents are keys of the cards created earlier in this run.
	Parents []string
	// Body builds the card's body. It is a function because a card's body can name
	// the board ids of cards created earlier (the integration card lists them), and a
	// dry run has no ids: it sees the keys instead.
	Body func(ids map[string]string) string
}

// PushStep pushes a branch to origin.
type PushStep struct{ Branch string }

// PublishEvidenceStep pushes the run's screenshots to a branch of their own, from a
// throwaway repository, so the pull request can show them without the change carrying a
// single PNG. The branch is never merged.
type PublishEvidenceStep struct {
	// Ref is the branch the screenshots travel on: evidence/<issue>.
	Ref string
	// Files are the screenshots, absolute paths.
	Files []EvidenceImage
}

// OpenPullRequestStep opens the run's one pull request.
type OpenPullRequestStep struct{ PullRequest NewPullRequest }

// CommentStep comments on an issue: a ticket, or the spec.
type CommentStep struct {
	Issue int
	Body  string
}

// StatusStep moves an issue to a Status on the projects it is in.
type StatusStep struct {
	Issue  int
	Status string
}

func (PullStep) String() string { return "update the base checkout (git pull --ff-only origin main)" }

func (s AddWorktreeStep) String() string { return "worktree " + s.Branch + " at " + s.Path }

func (s RemoveWorktreeStep) String() string {
	return "remove worktree " + s.Path + " and branch " + s.Branch
}

func (s ArchiveCardsStep) String() string {
	return fmt.Sprintf("archive %d card(s): %s", len(s.IDs), strings.Join(s.IDs, ", "))
}

func (s CreateCardStep) String() string {
	return fmt.Sprintf("card %s: %s assigned to %s in %s", s.Key, s.Card.Title, s.Card.Assignee, s.Card.Workspace)
}

func (s PushStep) String() string { return "push " + s.Branch + " to origin" }

func (s PublishEvidenceStep) String() string {
	names := make([]string, 0, len(s.Files))
	for _, file := range s.Files {
		names = append(names, file.Name)
	}
	return fmt.Sprintf("push %d screenshot(s) to %s: %s", len(s.Files), s.Ref, strings.Join(names, ", "))
}

func (s OpenPullRequestStep) String() string {
	line := fmt.Sprintf("open the pull request %s -> %s", s.PullRequest.Head, s.PullRequest.Base)
	if len(s.PullRequest.Reviewers) > 0 {
		line += ", review asked of " + strings.Join(s.PullRequest.Reviewers, ", ")
	}
	return line
}

func (s CommentStep) String() string {
	first, _, _ := strings.Cut(s.Body, "\n")
	return fmt.Sprintf("comment on #%d: %s", s.Issue, first)
}

func (s StatusStep) String() string {
	return fmt.Sprintf("move #%d to %s on its projects", s.Issue, s.Status)
}

// Apply runs the plan, in order. The card ids a step needs are the ids of the cards the
// earlier steps created. A step that fails is reported as the step it was, because the
// ones before it are done and cannot be taken back.
func (f *Factory) Apply(ctx context.Context, plan *Plan) error {
	f.cards = map[string]string{}
	for _, step := range plan.Steps {
		if err := f.applyStep(ctx, step); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
	}
	return nil
}

func (f *Factory) applyStep(ctx context.Context, step Step) error {
	switch s := step.(type) {
	case PullStep:
		return f.Git.Pull(ctx)
	case AddWorktreeStep:
		return f.Git.AddWorktree(ctx, s.Branch, s.Path)
	case RemoveWorktreeStep:
		if err := f.Git.RemoveWorktree(ctx, s.Path); err != nil {
			return err
		}
		return f.Git.DeleteBranch(ctx, s.Branch)
	case ArchiveCardsStep:
		return f.Board.Archive(ctx, s.IDs)
	case CreateCardStep:
		card := s.Card
		card.Body = s.Body(f.cards)
		parents := make([]string, 0, len(s.Parents))
		for _, key := range s.Parents {
			id, ok := f.cards[key]
			if !ok {
				return fmt.Errorf("card %q was never created, so %q cannot be its parent", key, s.Key)
			}
			parents = append(parents, id)
		}
		created, err := f.Board.Create(ctx, card, parents)
		if err != nil {
			return err
		}
		f.cards[s.Key] = created.ID
		return nil
	case PushStep:
		return f.Git.Push(ctx, s.Branch)
	case PublishEvidenceStep:
		files := make([]string, 0, len(s.Files))
		for _, file := range s.Files {
			files = append(files, file.Path)
		}
		return f.Git.PublishEvidence(ctx, s.Ref, files)
	case OpenPullRequestStep:
		url, err := f.GitHub.OpenPullRequest(ctx, s.PullRequest)
		if url != "" {
			f.URL = url
		}
		return err
	case CommentStep:
		return f.GitHub.Comment(ctx, s.Issue, s.Body)
	case StatusStep:
		return f.GitHub.SetStatus(ctx, s.Issue, s.Status)
	default:
		return fmt.Errorf("unknown step %T", step)
	}
}

// cardRef is the board id of a card created earlier in this run, or its key when it
// has not been created: a dry run prints the keys it will use.
func cardRef(ids map[string]string, key string) string {
	if id, ok := ids[key]; ok {
		return id
	}
	return key
}

// Report is the machine-readable form of a plan, for `--json`. It is derived from the
// steps, so it can never say something the run would not do.
type Report struct {
	Issue       int                `json:"issue"`
	Board       string             `json:"board"`
	DryRun      bool               `json:"dry_run"`
	Worktrees   []WorktreeReport   `json:"worktrees"`
	Cards       []CardReport       `json:"cards"`
	PullRequest *PullRequestReport `json:"pull_request,omitempty"`
	Comments    []CommentReport    `json:"comments,omitempty"`
	Steps       []StepReport       `json:"steps"`
}

// CommentReport is a comment the plan adds to an issue, body and all.
type CommentReport struct {
	Issue int    `json:"issue"`
	Body  string `json:"body"`
}

// WorktreeReport is one worktree the plan creates.
type WorktreeReport struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// PullRequestReport is the pull request a publish opens, body and all: `--dry-run`
// prints it, which is how the body is read before it exists anywhere.
type PullRequestReport struct {
	Base      string   `json:"base"`
	Head      string   `json:"head"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Reviewers []string `json:"reviewers,omitempty"`
}

// CardReport is one card the plan creates, with the body it will carry.
type CardReport struct {
	Key               string   `json:"key"`
	Title             string   `json:"title"`
	Assignee          string   `json:"assignee"`
	Skills            []string `json:"skills"`
	Parents           []string `json:"parents"`
	Workspace         string   `json:"workspace"`
	MaxRuntimeSeconds int      `json:"max_runtime_seconds"`
	IdempotencyKey    string   `json:"idempotency_key"`
	Body              string   `json:"body"`
}

// StepReport is one change, as an action and the line the operator reads.
type StepReport struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

// NewReport renders a plan for `--json`.
func NewReport(plan *Plan, board string, dryRun bool) Report {
	report := Report{Issue: plan.Issue, Board: board, DryRun: dryRun}
	for _, step := range plan.Steps {
		report.Steps = append(report.Steps, StepReport{Action: actionOf(step), Text: step.String()})
		switch s := step.(type) {
		case AddWorktreeStep:
			report.Worktrees = append(report.Worktrees, WorktreeReport{Branch: s.Branch, Path: s.Path})
		case OpenPullRequestStep:
			report.PullRequest = &PullRequestReport{
				Base:      s.PullRequest.Base,
				Head:      s.PullRequest.Head,
				Title:     s.PullRequest.Title,
				Body:      s.PullRequest.Body,
				Reviewers: s.PullRequest.Reviewers,
			}
		case CommentStep:
			report.Comments = append(report.Comments, CommentReport{Issue: s.Issue, Body: s.Body})
		case CreateCardStep:
			report.Cards = append(report.Cards, CardReport{
				Key:               s.Key,
				Title:             s.Card.Title,
				Assignee:          s.Card.Assignee,
				Skills:            s.Card.Skills,
				Parents:           s.Parents,
				Workspace:         s.Card.Workspace,
				MaxRuntimeSeconds: s.Card.MaxRuntime,
				IdempotencyKey:    s.Card.IdempotencyKey,
				Body:              s.Body(nil),
			})
		}
	}
	return report
}

// WriteJSON prints a report.
func WriteJSON(out io.Writer, report Report) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func actionOf(step Step) string {
	switch step.(type) {
	case PullStep:
		return "pull"
	case AddWorktreeStep:
		return "add-worktree"
	case RemoveWorktreeStep:
		return "remove-worktree"
	case ArchiveCardsStep:
		return "archive-cards"
	case CreateCardStep:
		return "create-card"
	case PushStep:
		return "push"
	case PublishEvidenceStep:
		return "publish-evidence"
	case OpenPullRequestStep:
		return "open-pull-request"
	case CommentStep:
		return "comment"
	case StatusStep:
		return "set-status"
	default:
		return "unknown"
	}
}
