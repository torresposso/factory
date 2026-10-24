package factory

import (
	"context"
	"fmt"
)

// Clean takes away what a finished run left behind: its worktrees, with their branches,
// and the cards of it that never got done. The done cards stay on the board, as the
// record of what the run did; planning the issue again archives them.
//
// It refuses unless the run is really finished. Its pull request must exist and be
// closed — merged or abandoned, either way the branch has been read — and no card of it
// may be running or ready, because a card in flight is a worker inside the folder this
// would remove. A run that never opened a pull request is not cleaned here: planning it
// again (`factory plan`) clears its leftovers first, which is what starting afresh
// means.
func (f *Factory) Clean(ctx context.Context, issue int) (*Plan, error) {
	pulls, err := f.GitHub.PullRequests(ctx, branch(issue), "all")
	if err != nil {
		return nil, err
	}
	if len(pulls) == 0 {
		return nil, fmt.Errorf("#%d has no pull request on %s, so nothing says the run is finished. "+
			"To start it over instead, run `factory plan` for it: that clears its cards and worktrees first",
			issue, branch(issue))
	}
	if pulls[0].State != stateClosed {
		return nil, fmt.Errorf("the pull request of #%d is still %s: %s", issue, pulls[0].State, pulls[0].URL)
	}

	cards, err := f.Board.List(ctx)
	if err != nil {
		return nil, err
	}
	mine := f.runCards(issue, cards)
	for _, card := range mine {
		if isMoving(card) {
			return nil, fmt.Errorf("a card of #%d is %s, so the run is not finished: %s (%s). "+
				"Wait for it, or stop it, before cleaning the run", issue, card.Status, card.ID, card.Title)
		}
	}

	worktrees, err := f.Git.Worktrees(ctx)
	if err != nil {
		return nil, err
	}

	// The cards first: they are what the board shows. Every card that is not done goes
	// together, so no child is left waiting on a parent that will never finish.
	steps := []Step{}
	var ids []string
	for _, card := range mine {
		if card.Status != statusDone {
			ids = append(ids, card.ID)
		}
	}
	if len(ids) > 0 {
		steps = append(steps, ArchiveCardsStep{IDs: ids})
	}
	for _, tree := range f.runWorktreeList(issue, worktrees) {
		steps = append(steps, RemoveWorktreeStep{Path: tree.Path, Branch: tree.Branch})
	}
	return &Plan{Issue: issue, Steps: steps}, nil
}
