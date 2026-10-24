package factory

import (
	"context"
	"strings"
	"testing"
)

// cleanHarness is a run that is finished: its pull request is closed, its cards are
// in status, and it left the worktrees of a two-ticket run behind.
func cleanHarness(t *testing.T, status string) *harness {
	t.Helper()
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.github.pulls = map[string][]PullRequest{
		"factory/7": {{Number: 30, State: "closed", URL: "https://example.test/pull/30"}},
	}
	h.board.cards = []BoardCard{
		{ID: "t_code", Title: "#7: The mobile menu", Status: status, WorkspacePath: "/trees/7"},
		{ID: "t_publish", Title: "Publish #7", Status: status, WorkspacePath: "/trees/7"},
		{ID: "t_other", Title: "another run", Status: "running", WorkspacePath: "/trees/70"},
	}
	h.git.trees = []Worktree{
		{Path: "/trees/7", Branch: "factory/7"},
		{Path: "/trees/7-3", Branch: "factory/7-3"},
		{Path: "/trees/70", Branch: "factory/70"},
	}
	return h
}

// The done cards are the record of what the run did: they stay on the board, and only
// the worktrees go.
func TestCleanKeepsTheDoneCardsAndRemovesTheWorktrees(t *testing.T) {
	h := cleanHarness(t, "done")

	plan, err := h.factory.Clean(context.Background(), 7)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}

	want := []string{"remove-worktree", "remove-worktree"}
	if got := actionsOf(plan); !equalStrings(got, want) {
		t.Errorf("steps = %v; want %v", got, want)
	}
	got := make([]string, 0, len(removedOf(plan)))
	for _, tree := range removedOf(plan) {
		got = append(got, tree.Path+" "+tree.Branch)
	}
	if !equalStrings(got, []string{"/trees/7 factory/7", "/trees/7-3 factory/7-3"}) {
		t.Errorf("removed = %v; want the run's worktrees only", got)
	}
	if err := h.factory.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(h.board.archived) > 0 {
		t.Errorf("archived = %v; want the done cards kept", h.board.archived)
	}
}

// A card that never got done is not a record of anything: it goes, so the board shows
// no work waiting on a run that is over.
func TestCleanArchivesTheCardsThatNeverGotDone(t *testing.T) {
	h := cleanHarness(t, "done")
	h.board.cards[1].Status = "todo"

	plan, err := h.factory.Clean(context.Background(), 7)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}

	want := []string{"archive-cards", "remove-worktree", "remove-worktree"}
	if got := actionsOf(plan); !equalStrings(got, want) {
		t.Errorf("steps = %v; want %v", got, want)
	}
	archive, ok := plan.Steps[0].(ArchiveCardsStep)
	if !ok || !equalStrings(archive.IDs, []string{"t_publish"}) {
		t.Errorf("archived = %+v; want the card that is not done, and no other", plan.Steps[0])
	}
}

// The list comes first, and the deletion after it: what clean is about to take away is
// on the screen before the first card is archived.
func TestCleanListsBeforeItDeletes(t *testing.T) {
	h := cleanHarness(t, "blocked")

	plan, err := h.factory.Clean(context.Background(), 7)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}

	if _, ok := plan.Steps[0].(ArchiveCardsStep); !ok {
		t.Errorf("the first step is %T; want the cards archived first", plan.Steps[0])
	}
	for index, step := range plan.Steps {
		if _, ok := step.(CreateCardStep); ok {
			t.Errorf("step %d creates a card: clean only takes away", index)
		}
		if _, ok := step.(PullStep); ok {
			t.Errorf("step %d updates the base checkout: clean does not touch it", index)
		}
	}
	// Nothing was touched to plan it.
	if len(h.board.archived) > 0 || len(h.git.removed) > 0 {
		t.Error("planning a clean already deleted something")
	}
	if err := h.factory.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !equalStrings(h.board.archived, []string{"t_code", "t_publish"}) {
		t.Errorf("archived = %v", h.board.archived)
	}
	if len(h.git.removed) != 2 || h.git.removed[0].Path != "/trees/7" {
		t.Errorf("removed = %v", h.git.removed)
	}
}

func TestCleanRefusesAnOpenPullRequest(t *testing.T) {
	h := cleanHarness(t, "done")
	h.github.pulls["factory/7"] = []PullRequest{{Number: 30, State: "open", URL: "https://example.test/pull/30"}}

	_, err := h.factory.Clean(context.Background(), 7)
	if err == nil {
		t.Fatal("clean: no error, want the open pull request to refuse it")
	}
	if !strings.Contains(err.Error(), "open") || !strings.Contains(err.Error(), "https://example.test/pull/30") {
		t.Errorf("error = %q", err)
	}
	if len(h.board.archived) > 0 || len(h.git.removed) > 0 {
		t.Error("clean deleted something, and its pull request is still open")
	}
}

// A run that never opened a pull request is not finished, it is unfinished: nothing
// says anybody has read the branch, so its worktrees stay.
func TestCleanRefusesARunWithoutAPullRequest(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{{Path: "/trees/7", Branch: "factory/7"}}

	_, err := h.factory.Clean(context.Background(), 7)
	if err == nil {
		t.Fatal("clean: no error, want a run without a pull request to be refused")
	}
	if !strings.Contains(err.Error(), "no pull request") || !strings.Contains(err.Error(), "factory plan") {
		t.Errorf("error = %q; want it to say what to do instead", err)
	}
}

// A card that is running or ready is a worker inside the folder clean would remove.
func TestCleanRefusesARunInFlight(t *testing.T) {
	for _, status := range []string{"running", "ready"} {
		t.Run(status, func(t *testing.T) {
			h := cleanHarness(t, status)

			_, err := h.factory.Clean(context.Background(), 7)
			if err == nil {
				t.Fatal("clean: no error, want a card in flight to refuse it")
			}
			if !strings.Contains(err.Error(), status) || !strings.Contains(err.Error(), "t_code") {
				t.Errorf("error = %q; want the card and its status", err)
			}
			if len(h.board.archived) > 0 || len(h.git.removed) > 0 {
				t.Error("clean deleted something of a run in flight")
			}
		})
	}
}

// Another run's cards and worktrees are not this run's to remove, whatever state they
// are in, and even a running card of another run must not stop this one's clean.
func TestCleanLeavesOtherRunsAlone(t *testing.T) {
	h := cleanHarness(t, "done")

	plan, err := h.factory.Clean(context.Background(), 7)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}

	for _, step := range plan.Steps {
		text := step.String()
		if strings.Contains(text, "/trees/70") || strings.Contains(text, "t_other") || strings.Contains(text, "factory/70") {
			t.Errorf("clean touches another run: %s", text)
		}
	}
}

// A finished run with nothing left is not an error, and not a plan either.
func TestCleanOfARunWithNothingLeft(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.github.pulls = map[string][]PullRequest{"factory/7": {{Number: 30, State: "closed"}}}

	plan, err := h.factory.Clean(context.Background(), 7)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("steps = %v; want none", plan.Steps)
	}
}
