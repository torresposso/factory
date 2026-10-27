package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Plan reads the issue, its tickets and the board, checks the guards, and returns
// every change the run makes, in the order it makes them. It changes nothing: Apply
// runs the list, and a dry run prints it.
//
// The guards stop a run before it touches anything: an issue with an open pull request
// has nothing left to do, and a card that is running or ready is a run in flight, which
// planning again would cut in half.
func (f *Factory) Plan(ctx context.Context, issue int) (*Plan, error) {
	if !ValidWorker(f.Opts.Worker) {
		return nil, fmt.Errorf("unknown worker %q; the workers are %s", f.Opts.Worker, strings.Join(Workers, ", "))
	}
	run, err := f.readRun(ctx, issue)
	if err != nil {
		return nil, err
	}
	worktrees, err := f.Git.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	cards, err := f.Board.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := f.guards(ctx, issue, cards); err != nil {
		return nil, err
	}
	everywhere, err := f.Board.Everywhere(ctx)
	if err != nil {
		return nil, err
	}
	if run.Ports, err = previewPorts(run, takenPorts(everywhere)); err != nil {
		return nil, err
	}
	steps, err := f.steps(ctx, run, issue, worktrees, cards)
	if err != nil {
		return nil, err
	}
	return &Plan{Issue: issue, Steps: steps}, nil
}

// readRun finds out what the issue is: a spec with sub-issues, or a lone ticket.
func (f *Factory) readRun(ctx context.Context, issue int) (PlannedRun, error) {
	spec, err := f.GitHub.Issue(ctx, issue)
	if err != nil {
		return PlannedRun{}, err
	}
	subIssues, err := f.GitHub.SubIssues(ctx, issue)
	if err != nil {
		return PlannedRun{}, err
	}
	subIssues = openIssues(subIssues)

	// No open sub-issue: the issue is its own ticket and there is no spec above it. Its
	// blockers are still read, so that a ticket blocked by an issue nobody is building
	// stops the run instead of starting work that is not ready.
	if len(subIssues) == 0 {
		blockers, err := f.GitHub.BlockedBy(ctx, issue)
		if err != nil {
			return PlannedRun{}, err
		}
		tickets, err := order([]Ticket{{Issue: spec, Blockers: blockers}})
		if err != nil {
			return PlannedRun{}, err
		}
		return PlannedRun{Spec: spec, Lone: true, Tickets: tickets}, nil
	}

	tickets := make([]Ticket, 0, len(subIssues))
	for _, subIssue := range subIssues {
		blockers, err := f.GitHub.BlockedBy(ctx, subIssue.Number)
		if err != nil {
			return PlannedRun{}, err
		}
		tickets = append(tickets, Ticket{Issue: subIssue, Blockers: blockers})
	}
	ordered, err := order(tickets)
	if err != nil {
		return PlannedRun{}, err
	}
	return PlannedRun{Spec: spec, Tickets: ordered}, nil
}

// guards refuses a run that must not start.
func (f *Factory) guards(ctx context.Context, issue int, cards []BoardCard) error {
	open, err := f.GitHub.PullRequests(ctx, branch(issue), stateOpen)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return fmt.Errorf("#%d already has an open pull request: %s", issue, open[0].URL)
	}
	mine := f.runCards(issue, cards)
	for _, card := range mine {
		if isMoving(card) {
			return fmt.Errorf("a card of #%d is %s, so a run of it is in flight: %s (%s). "+
				"Wait for it, or stop it, before planning the run again", issue, card.Status, card.ID, card.Title)
		}
		// Planning again archives what earlier runs left, and a blocked or in-review card is
		// not leftover: somebody has not answered it yet.
		if isWaiting(card) {
			return fmt.Errorf("a card of #%d is %s, so it still holds work nobody has answered: %s (%s). "+
				"Answer it, or archive it yourself with `hermes kanban --board %s archive %s`, before planning the run again",
				issue, card.Status, card.ID, card.Title, f.Opts.Board, card.ID)
		}
	}
	return nil
}

// steps is the plan itself: the base checkout, the leftovers of earlier runs, this
// issue's own leftovers, the worktrees, and the cards.
func (f *Factory) steps(ctx context.Context, run PlannedRun, issue int, worktrees []Worktree, cards []BoardCard) ([]Step, error) {
	steps := []Step{PullStep{}}

	// Runs whose pull request is closed are done with: their folders and branches go,
	// whatever they left behind.
	finished, err := f.finishedWorktrees(ctx, worktrees, issue)
	if err != nil {
		return nil, err
	}
	for _, tree := range finished {
		steps = append(steps, RemoveWorktreeStep{Path: tree.Path, Branch: tree.Branch})
	}

	// An earlier run of THIS issue may have left cards and folders, and its idempotency
	// keys would return its cards instead of creating new ones: the run would look
	// planned and nothing would happen. Both go.
	mine := f.runCards(issue, cards)
	if len(mine) > 0 {
		ids := make([]string, 0, len(mine))
		for _, card := range mine {
			ids = append(ids, card.ID)
		}
		steps = append(steps, ArchiveCardsStep{IDs: ids})
	}
	for _, tree := range f.runWorktreeList(issue, worktrees) {
		steps = append(steps, RemoveWorktreeStep{Path: tree.Path, Branch: tree.Branch})
	}

	steps = append(steps, f.worktreeSteps(run, issue)...)
	return append(steps, f.cardSteps(run, issue)...), nil
}

// finishedWorktrees are the worktrees of other runs whose pull request is closed,
// merged or abandoned: either way the folders and the branches are done with.
//
// The check is on the run's own branch, `factory/<M>`, because that is the only branch
// a run ever opens a pull request from: the ticket branches stay on Basecamp. So the
// worktrees are grouped by the run they belong to, and a finished run is taken away
// whole — its run worktree and the ticket worktrees under it — rather than leaving the
// ticket worktrees behind forever.
func (f *Factory) finishedWorktrees(ctx context.Context, worktrees []Worktree, issue int) ([]Worktree, error) {
	groups := map[int][]Worktree{}
	var runs []int
	for _, worktree := range worktrees {
		tree, other, ok := f.runOfWorktree(worktree)
		if !ok || other == issue {
			continue
		}
		if _, seen := groups[other]; !seen {
			runs = append(runs, other)
		}
		groups[other] = append(groups[other], tree)
	}
	// Lower issue first: a plan for the same board state reads the same twice.
	sort.Ints(runs)

	var finished []Worktree
	for _, run := range runs {
		pulls, err := f.GitHub.PullRequests(ctx, branch(run), "all")
		if err != nil {
			return nil, err
		}
		if len(pulls) > 0 && pulls[0].State == stateClosed {
			// The run's own worktree first: `git worktree list` reports creation order,
			// and a plan for the same board state must read the same twice.
			group := groups[run]
			sort.Slice(group, func(a, b int) bool { return group[a].Path < group[b].Path })
			finished = append(finished, group...)
		}
	}
	return finished, nil
}

// runOfWorktree reads a worktree as one of a run's. The branch comes from the
// directory name, which is how a run names its worktrees: `56` holds factory/56 and
// `56-3` holds factory/56-3. A worktree the factory did not make belongs to no run.
func (f *Factory) runOfWorktree(worktree Worktree) (Worktree, int, bool) {
	if filepath.Dir(worktree.Path) != filepath.Clean(f.Opts.Trees) {
		return Worktree{}, 0, false
	}
	name := filepath.Base(worktree.Path)
	issue, ok := issueOf(name)
	if !ok {
		return Worktree{}, 0, false
	}
	return Worktree{Path: worktree.Path, Branch: "factory/" + name}, issue, true
}

// runWorktreeList is this run's worktrees, by path: `git worktree list` reports creation
// order, and a plan for the same board state must read the same twice.
func (f *Factory) runWorktreeList(issue int, worktrees []Worktree) []Worktree {
	var mine []Worktree
	for _, worktree := range worktrees {
		tree, other, ok := f.runOfWorktree(worktree)
		if ok && other == issue {
			mine = append(mine, tree)
		}
	}
	sort.Slice(mine, func(a, b int) bool { return mine[a].Path < mine[b].Path })
	return mine
}

// runCards is this run's cards: the ones whose workspace is one of its worktrees.
// `list` does not report parents, so the workspace is what identifies a run's cards.
func (f *Factory) runCards(issue int, cards []BoardCard) []BoardCard {
	var mine []BoardCard
	for _, card := range cards {
		if f.isRunWorkspace(issue, card.WorkspacePath) {
			mine = append(mine, card)
		}
	}
	return mine
}

// worktreeSteps makes the run's worktrees. One ticket gets the one worktree its branch
// lives on; several tickets get one each, and one more where the branches meet.
func (f *Factory) worktreeSteps(run PlannedRun, issue int) []Step {
	if run.single() {
		return []Step{AddWorktreeStep{Branch: branch(issue), Path: runWorktree(f.Opts.Trees, issue)}}
	}
	steps := make([]Step, 0, len(run.Tickets)+1)
	for _, ticket := range run.Tickets {
		steps = append(steps, AddWorktreeStep{
			Branch: ticketBranch(issue, ticket.Issue.Number),
			Path:   ticketWorktree(f.Opts.Trees, issue, ticket.Issue.Number),
		})
	}
	return append(steps, AddWorktreeStep{Branch: branch(issue), Path: runWorktree(f.Opts.Trees, issue)})
}

// cardSteps is the run's cards, in the order the board needs them: a ticket's card
// after the cards it is blocked by (which the ticket order already guarantees), then
// the card where the branches meet (when there are several), and the review. Every run
// has a review card: the coder never reviews its own work. Nothing follows it: the
// review's completed hook publishes the run (docs/adr/0002).
func (f *Factory) cardSteps(run PlannedRun, issue int) []Step {
	steps := f.ticketCards(run, issue)
	if !run.single() {
		steps = append(steps, f.integrationCard(run, issue))
	}
	return append(steps, f.reviewCard(run, issue))
}

// ticketCards is one coder card per ticket, in the order the tickets came.
func (f *Factory) ticketCards(run PlannedRun, issue int) []Step {
	steps := make([]Step, 0, len(run.Tickets))
	for _, ticket := range run.Tickets {
		number := ticket.Issue.Number
		// One ticket works in the run's own worktree, where the review card reads it.
		workspace := ticketWorktree(f.Opts.Trees, issue, number)
		if run.single() {
			workspace = runWorktree(f.Opts.Trees, issue)
		}
		parents := make([]string, 0, len(ticket.Blockers))
		for _, blocker := range ticket.Blockers {
			parents = append(parents, cardKey(blocker))
		}
		workBranch := ticketBranch(issue, number)
		if run.single() {
			workBranch = branch(issue)
		}
		steps = append(steps, CreateCardStep{
			Key: cardKey(number),
			Card: NewCard{
				Title:          fmt.Sprintf("#%d: %s", number, ticket.Issue.Title),
				Assignee:       assigneeCoder,
				Workspace:      workspace,
				MaxRuntime:     runtimeCoder,
				IdempotencyKey: idempotencyKey(f.Opts.Board, issue, fmt.Sprintf("code:%d", number)),
			},
			Parents: parents,
			Body: func(map[string]string) string {
				return f.coderBody(run, ticket, workBranch)
			},
		})
	}
	return steps
}

// integrationCard brings the ticket branches into the run's branch.
func (f *Factory) integrationCard(run PlannedRun, issue int) Step {
	parents := make([]string, 0, len(run.Tickets))
	for _, ticket := range run.Tickets {
		parents = append(parents, cardKey(ticket.Issue.Number))
	}
	return CreateCardStep{
		Key: "integrate",
		Card: NewCard{
			Title:          fmt.Sprintf("Integrate #%d: %s", issue, run.Spec.Title),
			Assignee:       assigneeCoder,
			Workspace:      runWorktree(f.Opts.Trees, issue),
			MaxRuntime:     runtimeCoder,
			IdempotencyKey: idempotencyKey(f.Opts.Board, issue, "integrate"),
		},
		Parents: parents,
		Body:    func(ids map[string]string) string { return f.integrationBody(run, ids) },
	}
}

// reviewCard is the card that reviews the run's branch against the spec. It is the
// only card that sees the whole run, which is what it is for. It follows the
// integration, or the coder card when the run is one ticket.
func (f *Factory) reviewCard(run PlannedRun, issue int) Step {
	parents := []string{"integrate"}
	if run.single() {
		parents = []string{cardKey(run.Tickets[0].Issue.Number)}
	}
	return CreateCardStep{
		Key: "review",
		Card: NewCard{
			Title:          fmt.Sprintf("Review #%d: %s", issue, run.Spec.Title),
			Assignee:       assigneeQA,
			Skills:         []string{"code-review"},
			Workspace:      runWorktree(f.Opts.Trees, issue),
			MaxRuntime:     runtimeCoder,
			IdempotencyKey: idempotencyKey(f.Opts.Board, issue, "review"),
		},
		Parents: parents,
		Body:    func(map[string]string) string { return f.reviewBody(run) },
	}
}
