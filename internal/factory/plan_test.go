package factory

import (
	"context"
	"strings"
	"testing"
)

// open is one issue as GitHub reports it.
func open(number int, title string) Issue {
	return Issue{Number: number, Title: title, Body: "The body of issue " + title + ".", State: "open"}
}

// spec is a run of a spec with tickets.
func (h *harness) spec(number int, title string, subs []Issue, blockers map[int][]int) {
	h.github.issues = map[int]Issue{number: open(number, title)}
	h.github.subIssues = map[int][]Issue{number: subs}
	h.github.blockers = blockers
}

// lone is a run of one issue that is its own ticket.
func (h *harness) lone(number int, title string) {
	h.github.issues = map[int]Issue{number: open(number, title)}
	h.github.blockers = map[int][]int{number: nil}
}

// One ticket is the whole run: one worktree on the run's own branch, one coder card,
// and a review card that reads the coder's work. Its hook publishes: no card follows it.
func TestPlanOfALoneTicket(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")

	plan := h.plan(t, 7)

	want := []AddWorktreeStep{{Branch: "factory/7", Path: "/trees/7"}}
	if got := addedOf(plan); !equalWorktrees(got, want) {
		t.Errorf("worktrees = %v; want %v", got, want)
	}
	if got := actionsOf(plan); !equalStrings(got, []string{"pull", "add-worktree", "create-card", "create-card"}) {
		t.Errorf("steps = %v; want a pull, one worktree and two cards", got)
	}
	if got := keysOf(plan); strings.TrimSpace(got) != "code:7 review" {
		t.Errorf("cards = %q; want the coder and the review", got)
	}

	card := cardByKey(t, plan, "code:7")
	if card.Card.Title != "#7: The mobile menu" {
		t.Errorf("title = %q", card.Card.Title)
	}
	if card.Card.Assignee != "coder" {
		t.Errorf("assignee = %q", card.Card.Assignee)
	}
	// No skill is loaded into a coder card: its rules name the implement and tdd skills by path.
	if len(card.Card.Skills) != 0 {
		t.Errorf("skills = %v; want none, because the rules name implement and tdd by path", card.Card.Skills)
	}
	if card.Card.Workspace != "/trees/7" {
		t.Errorf("workspace = %q", card.Card.Workspace)
	}
	if len(card.Parents) != 0 {
		t.Errorf("parents = %v; want none", card.Parents)
	}

	review := cardByKey(t, plan, "review")
	if review.Card.Assignee != "qa" || !equalStrings(review.Card.Skills, []string{"code-review"}) {
		t.Errorf("review card = %+v", review.Card)
	}
	if review.Card.Workspace != "/trees/7" {
		t.Errorf("review workspace = %q; want the coder's", review.Card.Workspace)
	}
	if !equalStrings(review.Parents, []string{"code:7"}) {
		t.Errorf("review parents = %v; want the coder card", review.Parents)
	}
	if body := review.Body(nil); strings.Contains(body, "## Spec") || strings.Count(body, "## Ticket #7: The mobile menu") != 1 {
		t.Errorf("the review card of a lone ticket should carry the ticket once and no spec:\n%s", body)
	}

}

// Goal turns and a coder model reach the ticket's coder card and nothing else.
func TestPlanGoalAndModelOnlyReachTheCoderCard(t *testing.T) {
	h := newHarness(t)
	h.factory.Opts.GoalTurns = 4
	h.factory.Opts.CoderModel = "some-model"
	h.lone(7, "The mobile menu")

	plan := h.plan(t, 7)

	if card := cardByKey(t, plan, "code:7").Card; card.GoalTurns != 4 || card.Model != "some-model" {
		t.Errorf("coder card = %+v; want goal turns 4 and the model", card)
	}
	if card := cardByKey(t, plan, "review").Card; card.GoalTurns != 0 || card.Model != "" {
		t.Errorf("review card = %+v; want neither", card)
	}
}

// Several tickets get a worktree each, on their own branch, plus the one where the
// branches meet; the cards follow the order: blockers first.
func TestPlanOfSeveralTickets(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form"), open(23, "the badge")},
		map[int][]int{21: nil, 22: {21}, 23: {21}},
	)

	plan := h.plan(t, 20)

	want := []AddWorktreeStep{
		{Branch: "factory/20-21", Path: "/trees/20-21"},
		{Branch: "factory/20-22", Path: "/trees/20-22"},
		{Branch: "factory/20-23", Path: "/trees/20-23"},
		{Branch: "factory/20", Path: "/trees/20"},
	}
	if got := addedOf(plan); !equalWorktrees(got, want) {
		t.Errorf("worktrees = %v; want %v", got, want)
	}

	wantKeys := []string{"code:21", "code:22", "code:23", "integrate", "review"}
	if got := keysOf(plan); strings.TrimSpace(got) != strings.Join(wantKeys, " ") {
		t.Errorf("cards = %q; want %v", got, wantKeys)
	}

	first := cardByKey(t, plan, "code:21")
	if len(first.Card.Skills) != 0 {
		t.Errorf("skills = %v; want none, because the rules name implement and tdd by path", first.Card.Skills)
	}
	if first.Card.Workspace != "/trees/20-21" {
		t.Errorf("workspace = %q", first.Card.Workspace)
	}
	if len(first.Parents) != 0 {
		t.Errorf("parents = %v; want none, so the ticket starts at once", first.Parents)
	}
	if !strings.Contains(first.Body(nil), "Preview port: 8100") {
		t.Errorf("the coder card does not carry the ticket's preview port:\n%s", first.Body(nil))
	}

	second := cardByKey(t, plan, "code:22")
	if !equalStrings(second.Parents, []string{"code:21"}) {
		t.Errorf("parents of #22 = %v; want the card of #21", second.Parents)
	}
	if !strings.Contains(second.Body(nil), "Merge first: factory/20-21") {
		t.Errorf("the coder card does not name the branch to merge first:\n%s", second.Body(nil))
	}

	integration := cardByKey(t, plan, "integrate")
	if !equalStrings(integration.Parents, []string{"code:21", "code:22", "code:23"}) {
		t.Errorf("integration parents = %v; want every coder card", integration.Parents)
	}
	if integration.Card.Workspace != "/trees/20" {
		t.Errorf("integration workspace = %q", integration.Card.Workspace)
	}
	review := cardByKey(t, plan, "review")
	if review.Card.Assignee != "qa" || !equalStrings(review.Card.Skills, []string{"code-review"}) {
		t.Errorf("review card = %+v", review.Card)
	}
	if !equalStrings(review.Parents, []string{"integrate"}) {
		t.Errorf("review parents = %v", review.Parents)
	}
}

// The idempotency keys are what makes a second run of the same spec return the same
// cards instead of a second set. They carry the board, so two repositories can never
// take each other's card.
func TestPlanCardsCarryTheIdempotencyKeysOfTheRoute(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)

	plan := h.plan(t, 20)

	want := map[string]string{
		"code:21":   "laiacaliente-app#20:code:21",
		"code:22":   "laiacaliente-app#20:code:22",
		"integrate": "laiacaliente-app#20:integrate",
		"review":    "laiacaliente-app#20:review",
	}
	for key, idempotency := range want {
		if got := cardByKey(t, plan, key).Card.IdempotencyKey; got != idempotency {
			t.Errorf("key of %s = %q; want %q", key, got, idempotency)
		}
	}
	for _, card := range cardsOf(plan) {
		if card.Card.IdempotencyKey == "" {
			t.Errorf("the card %s has no idempotency key: a second run would create a second card", card.Key)
		}
	}
}

// A closed sub-issue is work that is done, and a closed blocker does not block.
func TestPlanReadsOnlyOpenTicketsAndBlockers(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), {Number: 22, Title: "gone", State: "closed"}},
		map[int][]int{21: nil},
	)

	plan := h.plan(t, 20)

	if got := keysOf(plan); strings.Contains(got, "code:22") {
		t.Errorf("cards = %q; a closed sub-issue is not a ticket", got)
	}
	if got := keysOf(plan); !strings.Contains(got, "code:21") {
		t.Errorf("cards = %q; want the open ticket", got)
	}
}

// A ticket whose open blocker is not one of the tickets has no order, and the run stops
// before it touches anything.
func TestPlanRefusesABlockerOutsideTheRun(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list")},
		map[int][]int{21: {99}},
	)

	err := h.refused(t, 20)
	if !strings.Contains(err.Error(), "#99") {
		t.Errorf("error = %q", err)
	}
	if h.git.pulled || len(h.board.created) > 0 || len(h.board.archived) > 0 {
		t.Error("the factory touched something before refusing the run")
	}
}

// An issue with an open pull request is already done: a second run would open a second
// pull request for the same spec.
func TestPlanRefusesAnIssueWithAnOpenPullRequest(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.github.pulls = map[string][]PullRequest{
		"factory/7": {{Number: 12, State: "open", URL: "https://example.test/pull/12"}},
	}

	err := h.refused(t, 7)
	if !strings.Contains(err.Error(), "open pull request") || !strings.Contains(err.Error(), "https://example.test/pull/12") {
		t.Errorf("error = %q", err)
	}
}

// A card that is running or ready is a run in flight: planning again would archive the
// cards a worker is inside.
func TestPlanRefusesARunInFlight(t *testing.T) {
	for _, status := range []string{"running", "ready"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.lone(7, "The mobile menu")
			h.board.cards = []BoardCard{
				{ID: "t_worker", Title: "#7: The mobile menu", Status: status, WorkspacePath: "/trees/7"},
			}

			err := h.refused(t, 7)
			if !strings.Contains(err.Error(), status) || !strings.Contains(err.Error(), "t_worker") {
				t.Errorf("error = %q; want the card and its status", err)
			}
			if len(h.board.archived) > 0 {
				t.Error("the factory archived a card of a run in flight")
			}
		})
	}
}

// A blocked card is a worker asking a person for something and a card in review waits on a
// reviewer: planning again must not archive them as if they were leftovers.
func TestPlanRefusesACardNobodyHasAnswered(t *testing.T) {
	for _, status := range []string{"blocked", "review"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.lone(7, "The mobile menu")
			h.board.cards = []BoardCard{
				{ID: "t_asked", Title: "#7: The mobile menu", Status: status, WorkspacePath: "/trees/7"},
			}

			err := h.refused(t, 7)
			if !strings.Contains(err.Error(), "t_asked") || !strings.Contains(err.Error(), "archive") {
				t.Errorf("error = %q; want the card and how to archive it", err)
			}
			if len(h.board.archived) > 0 {
				t.Error("the factory archived a card nobody had answered")
			}
		})
	}
}

// An earlier run of the same issue leaves cards whose idempotency keys would return
// them, and folders whose paths the new worktrees need: both go, before anything is
// created.
func TestPlanClearsAnEarlierRunOfTheSameIssue(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{
		{Path: "/trees/7", Branch: "factory/7"},
		{Path: "/trees/7-3", Branch: "factory/7-3"},
	}
	h.board.cards = []BoardCard{
		{ID: "t_old", Title: "#7: The mobile menu", Status: "done", WorkspacePath: "/trees/7"},
		{ID: "t_old3", Title: "#3: another", Status: "done", WorkspacePath: "/trees/7-3"},
		{ID: "t_other", Title: "not ours", Status: "done", WorkspacePath: "/trees/70"},
	}

	plan := h.plan(t, 7)

	want := []string{"pull", "archive-cards", "remove-worktree", "remove-worktree", "add-worktree", "create-card", "create-card"}
	if got := actionsOf(plan); !equalStrings(got, want) {
		t.Errorf("steps = %v; want %v", got, want)
	}
	archive, ok := plan.Steps[1].(ArchiveCardsStep)
	if !ok || !equalStrings(archive.IDs, []string{"t_old", "t_old3"}) {
		t.Errorf("archived = %+v; want the two cards of #7, and not #70's", plan.Steps[1])
	}
	wantRemoved := []string{"/trees/7", "/trees/7-3"}
	got := make([]string, 0, len(removedOf(plan)))
	for _, tree := range removedOf(plan) {
		got = append(got, tree.Path)
	}
	if !equalStrings(got, wantRemoved) {
		t.Errorf("removed = %v; want %v", got, wantRemoved)
	}
}

// The worktrees and branches of earlier runs whose pull request is closed are done
// with, and the run clears them; a run whose pull request is still open is left alone,
// because it is somebody's work.
//
// The check is on the run's own branch — `factory/41` — which is the only branch a run
// ever opens a pull request from: the ticket branches stay on Basecamp, so a ticket
// worktree has no pull request of its own and would otherwise never be cleared.
func TestPlanRemovesFinishedRunsWhole(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{
		{Path: "/trees/41", Branch: "factory/41"},
		{Path: "/trees/41-2", Branch: "factory/41-2"},
		{Path: "/trees/41-3", Branch: "factory/41-3"},
		{Path: "/trees/42", Branch: "factory/42"},
		{Path: "/trees/42-1", Branch: "factory/42-1"},
		{Path: "/trees/notes", Branch: "notes"},
	}
	h.github.pulls = map[string][]PullRequest{
		"factory/41": {{Number: 50, State: "closed"}},
		"factory/42": {{Number: 52, State: "open"}},
	}

	plan := h.plan(t, 7)

	got := make([]string, 0, len(removedOf(plan)))
	for _, tree := range removedOf(plan) {
		got = append(got, tree.Path+" "+tree.Branch)
	}
	want := []string{
		"/trees/41 factory/41",
		"/trees/41-2 factory/41-2",
		"/trees/41-3 factory/41-3",
	}
	if !equalStrings(got, want) {
		t.Errorf("removed = %v\nwant      %v\n(#42 is open and `notes` is no run's)", got, want)
	}
}

// A run whose pull request was never opened is not a finished run: nothing says anybody
// is done with its worktrees, so they stay.
func TestPlanLeavesARunWithoutAPullRequestAlone(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{{Path: "/trees/41", Branch: "factory/41"}}

	plan := h.plan(t, 7)

	if len(removedOf(plan)) != 0 {
		t.Errorf("removed = %v; want none", removedOf(plan))
	}
}

// Two runs to clear are taken away lower first, so the same board state plans the same
// twice.
func TestPlanClearsSeveralRunsInOrder(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{
		{Path: "/trees/52", Branch: "factory/52"},
		{Path: "/trees/41-3", Branch: "factory/41-3"},
		{Path: "/trees/41", Branch: "factory/41"},
	}
	h.github.pulls = map[string][]PullRequest{
		"factory/41": {{Number: 50, State: "closed"}},
		"factory/52": {{Number: 51, State: "closed"}},
	}

	plan := h.plan(t, 7)

	got := make([]string, 0, len(removedOf(plan)))
	for _, tree := range removedOf(plan) {
		got = append(got, tree.Path)
	}
	want := []string{"/trees/41", "/trees/41-3", "/trees/52"}
	if !equalStrings(got, want) {
		t.Errorf("removed = %v; want %v", got, want)
	}
}

// A worktree outside the worktrees directory is no run's, however it is named.
func TestPlanIgnoresWorktreesOutsideTheDirectory(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{{Path: "/clone/.worktrees/41", Branch: "factory/41"}}

	plan := h.plan(t, 7)

	if len(removedOf(plan)) != 0 {
		t.Errorf("removed = %v; want none", removedOf(plan))
	}
}

// Apply is what makes the plan true, and it is where a card learns the board ids of the
// cards created before it: a parent and the integration card's list.
func TestApplyResolvesTheCardIds(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	if err := h.factory.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !h.git.pulled {
		t.Error("the base checkout was never updated")
	}
	wantTrees := []string{"factory/20-21 /trees/20-21", "factory/20-22 /trees/20-22", "factory/20 /trees/20"}
	gotTrees := make([]string, 0, len(h.git.added))
	for _, tree := range h.git.added {
		gotTrees = append(gotTrees, tree.Branch+" "+tree.Path)
	}
	if !equalStrings(gotTrees, wantTrees) {
		t.Errorf("worktrees = %v; want %v", gotTrees, wantTrees)
	}

	if len(h.board.created) != 4 {
		t.Fatalf("created %d cards; want 4", len(h.board.created))
	}
	byKey := map[string]createdCard{}
	for _, created := range h.board.created {
		byKey[created.Card.IdempotencyKey] = created
	}
	second := byKey["laiacaliente-app#20:code:22"]
	if !equalStrings(second.Parents, []string{"t_laiacaliente-app#20:code:21"}) {
		t.Errorf("parents of #22 = %v; want the board id of the card of #21", second.Parents)
	}
	integration := byKey["laiacaliente-app#20:integrate"]
	if !equalStrings(integration.Parents, []string{
		"t_laiacaliente-app#20:code:21",
		"t_laiacaliente-app#20:code:22",
	}) {
		t.Errorf("integration parents = %v", integration.Parents)
	}
	for _, id := range []string{"t_laiacaliente-app#20:code:21", "t_laiacaliente-app#20:code:22"} {
		if !strings.Contains(integration.Card.Body, "card "+id) {
			t.Errorf("the integration card does not list %s:\n%s", id, integration.Card.Body)
		}
	}
	for _, created := range h.board.created {
		if created.Card.Workspace == "" {
			t.Errorf("the card %q has no workspace", created.Card.Title)
		}
		if created.Card.Body == "" {
			t.Errorf("the card %q has no body", created.Card.Title)
		}
	}
}

// Planning reads GitHub and the board and changes nothing: the list it returns is the
// whole of what a run would do, which is what makes `--dry-run` free.
func TestPlanChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.git.trees = []Worktree{{Path: "/trees/7", Branch: "factory/7"}}
	h.board.cards = []BoardCard{{ID: "t_old", Status: "done", WorkspacePath: "/trees/7"}}

	plan := h.plan(t, 7)
	if len(plan.Steps) == 0 {
		t.Fatal("the plan has no steps")
	}

	if h.git.pulled {
		t.Error("planning updated the base checkout")
	}
	if len(h.git.added) > 0 || len(h.git.removed) > 0 || len(h.git.deleted) > 0 {
		t.Error("planning touched the worktrees")
	}
	if len(h.board.created) > 0 || len(h.board.archived) > 0 {
		t.Error("planning touched the board")
	}
}

// The report says exactly what the plan does, and nothing the plan does not.
func TestReportMatchesThePlan(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	report := NewReport(plan, h.factory.Opts.Board, true)

	if report.Issue != 20 || !report.DryRun || report.Board != "laiacaliente-app" {
		t.Errorf("report = %+v", report)
	}
	if len(report.Steps) != len(plan.Steps) {
		t.Errorf("the report has %d steps and the plan %d", len(report.Steps), len(plan.Steps))
	}
	if len(report.Cards) != 4 || len(report.Worktrees) != 3 {
		t.Errorf("report = %d cards, %d worktrees; want 4 and 3", len(report.Cards), len(report.Worktrees))
	}
	for _, card := range report.Cards {
		if card.Body == "" || card.IdempotencyKey == "" || card.Key == "" {
			t.Errorf("the report's card is incomplete: %+v", card)
		}
	}
}

func equalWorktrees(got, want []AddWorktreeStep) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// The preview ports are the machine's: a live card on any board keeps its port and its
// PocketBase port, and a done card gives both back.
func TestPlanPreviewPortsAvoidLiveCardsOnEveryBoard(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	h.board.others = []BoardCard{
		{ID: "t_a", Status: statusRunning, Body: "## Your run\n\nBranch: factory/3\nPreview port: 8100"},
		{ID: "t_b", Status: statusDone, Body: "Preview port: 8101"},
		{ID: "t_c", Status: statusBlocked, Body: "Preview port: 7102"},
	}
	plan := h.plan(t, 20)

	for key, want := range map[string]string{
		"code:21":   "Preview port: 8101",
		"code:22":   "Preview port: 8103",
		"integrate": "Preview port: 8104",
		"review":    "Preview port: 8104",
	} {
		if body := yourRun(cardByKey(t, plan, key).Body(nil)); !strings.Contains(body, want) {
			t.Errorf("card %s: want %q in\n%s", key, want, body)
		}
	}
}
