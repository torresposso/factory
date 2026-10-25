package factory

import (
	"context"
	"testing"
)

// The fakes are what makes `make verify` need no network, no board and no environment:
// every test drives the factory through these, and never through git, gh or hermes.

type fakeGit struct {
	trees     []Worktree
	pulled    bool
	added     []Worktree
	removed   []Worktree
	deleted   []string
	pushed    []string
	diff      Diff
	published []publishedEvidence
	err       error
}

func (g *fakeGit) Pull(context.Context) error { g.pulled = true; return g.err }

func (g *fakeGit) AddWorktree(_ context.Context, branch, path string) error {
	g.added = append(g.added, Worktree{Branch: branch, Path: path})
	return g.err
}

func (g *fakeGit) RemoveWorktree(_ context.Context, path string) error {
	g.removed = append(g.removed, Worktree{Path: path})
	return g.err
}

func (g *fakeGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return g.err
}

func (g *fakeGit) Worktrees(context.Context) ([]Worktree, error) { return g.trees, g.err }

func (g *fakeGit) Push(_ context.Context, branch string) error {
	g.pushed = append(g.pushed, branch)
	return g.err
}

func (g *fakeGit) Diff(context.Context, string) (Diff, error) { return g.diff, g.err }

func (g *fakeGit) PublishEvidence(_ context.Context, ref string, files []string) error {
	if g.err != nil {
		return g.err
	}
	g.published = append(g.published, publishedEvidence{Ref: ref, Files: files})
	return nil
}

// publishedEvidence is one call to PublishEvidence, with the files it was given.
type publishedEvidence struct {
	Ref   string
	Files []string
}

// fakeWorkspaces is the worktrees' files, as the tests want them to be.
type fakeWorkspaces struct {
	screenshots []string
	err         error
}

func (w *fakeWorkspaces) Screenshots(context.Context, []string) ([]string, error) {
	return w.screenshots, w.err
}

type fakeGitHub struct {
	issues    map[int]Issue
	subIssues map[int][]Issue
	blockers  map[int][]int
	pulls     map[string][]PullRequest

	opened     []NewPullRequest
	openErr    error
	issuesErr  error
	comments   []comment
	commentErr error
	statuses   []status
	statusErr  error
}

// status is one move of an issue on its projects.
type status struct {
	Issue  int
	Status string
}

func (h *fakeGitHub) SetStatus(_ context.Context, number int, to string) error {
	if h.statusErr != nil {
		return h.statusErr
	}
	h.statuses = append(h.statuses, status{Issue: number, Status: to})
	return nil
}

// comment is one comment the factory added to an issue.
type comment struct {
	Issue int
	Body  string
}

func (h *fakeGitHub) Issue(_ context.Context, number int) (Issue, error) {
	if h.issuesErr != nil {
		return Issue{}, h.issuesErr
	}
	issue, ok := h.issues[number]
	if !ok {
		return Issue{}, errNotInFake
	}
	return issue, nil
}

func (h *fakeGitHub) SubIssues(_ context.Context, number int) ([]Issue, error) {
	if h.issuesErr != nil {
		return nil, h.issuesErr
	}
	return h.subIssues[number], nil
}

func (h *fakeGitHub) BlockedBy(_ context.Context, number int) ([]int, error) {
	if h.issuesErr != nil {
		return nil, h.issuesErr
	}
	return h.blockers[number], nil
}

func (h *fakeGitHub) PullRequests(_ context.Context, branch, state string) ([]PullRequest, error) {
	var found []PullRequest
	for _, pull := range h.pulls[branch] {
		if state == "all" || pull.State == state {
			found = append(found, pull)
		}
	}
	return found, nil
}

func (h *fakeGitHub) OpenPullRequest(_ context.Context, pr NewPullRequest) (string, error) {
	if h.openErr != nil {
		return "", h.openErr
	}
	h.opened = append(h.opened, pr)
	return "https://github.com/torresposso/laiacaliente-app/pull/77", nil
}

func (h *fakeGitHub) Comment(_ context.Context, number int, body string) error {
	if h.commentErr != nil {
		return h.commentErr
	}
	h.comments = append(h.comments, comment{Issue: number, Body: body})
	return nil
}

type fakeBoard struct {
	cards []BoardCard
	// others are the cards of the other boards on the machine.
	others   []BoardCard
	details  map[string]BoardCardDetail
	runs     map[string][]CardRun
	created  []createdCard
	archived []string
	err      error
}

// createdCard is a card the factory asked for, with the parents it resolved.
type createdCard struct {
	Card    NewCard
	Parents []string
}

func (b *fakeBoard) List(context.Context) ([]BoardCard, error) { return b.cards, b.err }

func (b *fakeBoard) Everywhere(context.Context) ([]BoardCard, error) {
	return append(append([]BoardCard{}, b.cards...), b.others...), b.err
}

func (b *fakeBoard) Show(_ context.Context, id string) (BoardCardDetail, error) {
	if detail, ok := b.details[id]; ok {
		return detail, nil
	}
	return BoardCardDetail{}, errNotInFake
}

func (b *fakeBoard) Runs(_ context.Context, id string) ([]CardRun, error) { return b.runs[id], b.err }

func (b *fakeBoard) Create(_ context.Context, card NewCard, parents []string) (BoardCard, error) {
	if b.err != nil {
		return BoardCard{}, b.err
	}
	b.created = append(b.created, createdCard{Card: card, Parents: parents})
	return BoardCard{ID: "t_" + card.IdempotencyKey, Title: card.Title, Status: "todo"}, nil
}

func (b *fakeBoard) Archive(_ context.Context, ids []string) error {
	b.archived = append(b.archived, ids...)
	return b.err
}

var errNotInFake = &notInFake{}

type notInFake struct{}

func (*notInFake) Error() string { return "the test did not put that in the fake" }

// harness is a factory the tests drive.
type harness struct {
	factory *Factory
	git     *fakeGit
	github  *fakeGitHub
	board   *fakeBoard
	files   *fakeWorkspaces
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	opts := Options{
		Repo:  "torresposso/laiacaliente-app",
		Clone: "/clone",
		Trees: "/trees",
		Board: "laiacaliente-app",
		Home:  DefaultHome,
	}
	h := &harness{git: &fakeGit{}, github: &fakeGitHub{}, board: &fakeBoard{}, files: &fakeWorkspaces{}}
	h.factory = &Factory{Git: h.git, GitHub: h.github, Board: h.board, Workspaces: h.files, Opts: opts}
	return h
}

// plan plans a run and fails the test if the guards refuse it.
func (h *harness) plan(t *testing.T, issue int) *Plan {
	t.Helper()
	plan, err := h.factory.Plan(context.Background(), issue)
	if err != nil {
		t.Fatalf("plan #%d: %v", issue, err)
	}
	return plan
}

// refused plans a run that must not start.
func (h *harness) refused(t *testing.T, issue int) error {
	t.Helper()
	if _, err := h.factory.Plan(context.Background(), issue); err == nil {
		t.Fatalf("plan #%d: no error, want the guard to refuse it", issue)
	} else {
		return err
	}
	return nil
}

// cardsOf are the card steps of a plan, in order.
func cardsOf(plan *Plan) []CreateCardStep {
	var cards []CreateCardStep
	for _, step := range plan.Steps {
		if card, ok := step.(CreateCardStep); ok {
			cards = append(cards, card)
		}
	}
	return cards
}

// cardByKey finds one card step.
func cardByKey(t *testing.T, plan *Plan, key string) CreateCardStep {
	t.Helper()
	for _, card := range cardsOf(plan) {
		if card.Key == key {
			return card
		}
	}
	t.Fatalf("the plan has no card %q; it has %s", key, keysOf(plan))
	return CreateCardStep{}
}

func keysOf(plan *Plan) string {
	keys := ""
	for _, card := range cardsOf(plan) {
		keys += card.Key + " "
	}
	return keys
}

// addedOf are the worktrees a plan creates, in order.
func addedOf(plan *Plan) []AddWorktreeStep {
	var trees []AddWorktreeStep
	for _, step := range plan.Steps {
		if tree, ok := step.(AddWorktreeStep); ok {
			trees = append(trees, tree)
		}
	}
	return trees
}

// removedOf are the worktrees a plan removes, in order.
func removedOf(plan *Plan) []RemoveWorktreeStep {
	var trees []RemoveWorktreeStep
	for _, step := range plan.Steps {
		if tree, ok := step.(RemoveWorktreeStep); ok {
			trees = append(trees, tree)
		}
	}
	return trees
}

// actionsOf is the plan as `--json` reports it.
func actionsOf(plan *Plan) []string {
	var actions []string
	for _, step := range plan.Steps {
		actions = append(actions, actionOf(step))
	}
	return actions
}
