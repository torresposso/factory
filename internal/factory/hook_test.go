package factory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// payload is a hook's stdin, as Hermes wrote it on Basecamp (docs/hermes.md).
func payload(event, extra string) string {
	return `{"hook_event_name":"` + event + `","tool_name":null,"session_id":"","cwd":"/trees/20",` +
		`"extra":{` + extra + `}}`
}

func TestReadHookOfEachEvent(t *testing.T) {
	cases := []struct {
		in   string
		want Hook
	}{
		{
			payload(hookClaimed, `"task_id":"t_21","board":"laiacaliente-app","assignee":"coder","run_id":4`),
			Hook{Event: hookClaimed, Card: "t_21", Board: "laiacaliente-app", Run: 4},
		},
		{
			payload(hookBlocked, `"task_id":"t_21","board":"laiacaliente-app","run_id":4,"reason":"Which list?"`),
			Hook{Event: hookBlocked, Card: "t_21", Board: "laiacaliente-app", Run: 4, Reason: "Which list?"},
		},
		{
			payload(hookCompleted, `"task_id":"t_review","board":"laiacaliente-app","run_id":9,"summary":"Reviewed."`),
			Hook{Event: hookCompleted, Card: "t_review", Board: "laiacaliente-app", Run: 9, Summary: "Reviewed."},
		},
	}
	for _, c := range cases {
		got, err := ReadHook(strings.NewReader(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.want.Event, err)
			continue
		}
		if got != c.want {
			t.Errorf("hook = %+v; want %+v", got, c.want)
		}
	}
}

func TestReadHookRefusesWhatItCannotUse(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      "kanban_task_completed",
		"another event": payload("pre_tool_call", `"task_id":"t_21","board":"b"`),
		"no card":       payload(hookCompleted, `"board":"b"`),
		"no board":      payload(hookCompleted, `"task_id":"t_21"`),
	} {
		if _, err := ReadHook(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// noteHarness is a run of #20 with its four cards on the board, and a card of somebody
// else's beside them.
func noteHarness(t *testing.T) *harness {
	h := publishHarness(t)
	h.board.details["t_other"] = BoardCardDetail{Card: BoardCard{ID: "t_other", Title: "Rotate the keys", WorkspacePath: "/somewhere"}}
	h.board.details["t_scratch"] = BoardCardDetail{Card: BoardCard{ID: "t_scratch", Title: "Scratch", WorkspacePath: "/trees/20"}}
	return h
}

func (h *harness) note(t *testing.T, hook Hook) comment {
	t.Helper()
	plan, err := h.factory.Note(context.Background(), hook)
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if err := h.factory.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(h.github.comments) != 1 {
		t.Fatalf("comments = %+v; want one", h.github.comments)
	}
	return h.github.comments[0]
}

// A ticket's card writes on its ticket; the integration and the review on the spec.
func TestNoteGoesToTheCardsIssue(t *testing.T) {
	for card, issue := range map[string]int{"t_21": 21, "t_22": 22, "t_integrate": 20, "t_review": 20} {
		h := noteHarness(t)
		got := h.note(t, Hook{Event: hookClaimed, Card: card, Board: "laiacaliente-app", Run: 3})
		if got.Issue != issue {
			t.Errorf("%s comments on #%d; want #%d", card, got.Issue, issue)
		}
		if !strings.Contains(got.Body, "`"+card+"`") || !strings.Contains(got.Body, "run 3") {
			t.Errorf("%s: body = %q; want the card and the run", card, got.Body)
		}
	}
}

func TestNoteOfALoneTicketGoesToTheTicket(t *testing.T) {
	h := newHarness(t)
	h.board.details = map[string]BoardCardDetail{
		"t_7": {Card: BoardCard{ID: "t_7", Title: "#7: The mobile menu", WorkspacePath: "/trees/7"}},
	}
	if got := h.note(t, Hook{Event: hookClaimed, Card: "t_7", Board: "laiacaliente-app"}); got.Issue != 7 {
		t.Errorf("comments on #%d; want #7", got.Issue)
	}
}

// What the worker said is quoted under the program's own line, every line of it.
func TestNoteQuotesTheWorker(t *testing.T) {
	h := noteHarness(t)
	got := h.note(t, Hook{Event: hookBlocked, Card: "t_21", Board: "laiacaliente-app", Reason: "Which list?\nThe spec names two."})
	if !strings.Contains(got.Body, "is blocked") || !strings.HasSuffix(got.Body, "\n\n> Which list?\n> The spec names two.") {
		t.Errorf("blocked body = %q", got.Body)
	}

	h = noteHarness(t)
	got = h.note(t, Hook{Event: hookCompleted, Card: "t_21", Board: "laiacaliente-app", Summary: "  The list is there.\n"})
	if !strings.Contains(got.Body, "is done") || !strings.HasSuffix(got.Body, "\n\n> The list is there.") {
		t.Errorf("completed body = %q", got.Body)
	}
}

func TestNoteSaysWhenTheWorkerSaidNothing(t *testing.T) {
	h := noteHarness(t)
	got := h.note(t, Hook{Event: hookCompleted, Card: "t_21", Board: "laiacaliente-app"})
	if !strings.HasSuffix(got.Body, "is done. Its worker reported no summary.") {
		t.Errorf("body = %q", got.Body)
	}
}

// The claimed hook fires for every card of every board: a card the plan did not write
// is nothing to note.
func TestNoteOfACardOutsideARun(t *testing.T) {
	for _, card := range []string{"t_other", "t_scratch"} {
		h := noteHarness(t)
		_, err := h.factory.Note(context.Background(), Hook{Event: hookClaimed, Card: card, Board: "laiacaliente-app"})
		if !errors.Is(err, ErrNotARunCard) {
			t.Errorf("%s: error = %v; want ErrNotARunCard", card, err)
		}
	}
}

func TestReviewDoneOnlyForTheReviewCardCompleting(t *testing.T) {
	cases := []struct {
		event, card string
		ok          bool
	}{
		{hookCompleted, "t_review", true},
		{hookBlocked, "t_review", false},
		{hookClaimed, "t_review", false},
		{hookCompleted, "t_integrate", false},
		{hookCompleted, "t_21", false},
		{hookCompleted, "t_other", false},
	}
	for _, c := range cases {
		h := noteHarness(t)
		issue, ok, err := h.factory.ReviewDone(context.Background(), Hook{Event: c.event, Card: c.card, Board: "laiacaliente-app"})
		if err != nil {
			t.Errorf("%s %s: %v", c.event, c.card, err)
		}
		if ok != c.ok || (ok && issue != 20) {
			t.Errorf("%s %s = #%d, %v; want %v", c.event, c.card, issue, ok, c.ok)
		}
	}
}

func TestReviewDoneOfACardTheBoardCannotShow(t *testing.T) {
	h := noteHarness(t)
	if _, _, err := h.factory.ReviewDone(context.Background(), Hook{Event: hookCompleted, Card: "t_gone", Board: "laiacaliente-app"}); err == nil {
		t.Error("no error for a card the board could not show")
	}
}

func TestPublishAndNoteLinksThePullRequest(t *testing.T) {
	h := publishHarness(t)
	if err := h.factory.PublishAndNote(context.Background(), 20); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(h.github.opened) != 1 {
		t.Errorf("opened %d pull requests; want one", len(h.github.opened))
	}
	want := comment{Issue: 20, Body: "The factory published this run: https://github.com/torresposso/laiacaliente-app/pull/77"}
	if len(h.github.comments) != 1 || h.github.comments[0] != want {
		t.Errorf("comments = %+v; want %+v", h.github.comments, want)
	}
}

// A review card's hook may not write the board, and listing it is a write: the run is
// read from the review card up through its parents.
func TestPublishFromTheReviewReadsTheRunThroughItsParents(t *testing.T) {
	h := publishHarness(t)
	for id, parents := range map[string][]string{"t_review": {"t_integrate"}, "t_integrate": {"t_21", "t_22"}, "t_22": {"t_21"}} {
		detail := h.board.details[id]
		detail.Parents = parents
		h.board.details[id] = detail
	}
	h.factory.Board = FromReview(listless{h.board}, "t_review")

	if err := h.factory.PublishAndNote(context.Background(), 20); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(h.github.opened) != 1 {
		t.Fatalf("opened %d pull requests; want one", len(h.github.opened))
	}
	if body := h.github.opened[0].Body; !strings.Contains(body, "The form creates a Proyecto.") || !strings.Contains(body, "The list is there.") {
		t.Errorf("the pull request does not report both tickets:\n%s", body)
	}
}

func TestFromReviewReportsACardItCannotShow(t *testing.T) {
	h := publishHarness(t)
	detail := h.board.details["t_review"]
	detail.Parents = []string{"t_gone"}
	h.board.details["t_review"] = detail
	if _, err := FromReview(h.board, "t_review").List(context.Background()); !errors.Is(err, errNotInFake) {
		t.Errorf("error = %v; want the board's", err)
	}
}

// listless is a board that refuses to be listed, as Hermes refuses a worker's hook.
type listless struct{ Board }

func (listless) List(context.Context) ([]BoardCard, error) {
	return nil, errors.New("kanban: delegate_task child contexts cannot mutate Kanban tasks or boards")
}

// The review card is already done, so a publish that fails says so on the spec, and
// still fails.
func TestPublishAndNoteSaysWhyItFailed(t *testing.T) {
	h := publishHarness(t)
	h.github.openErr = errors.New("gh-axi: no permission")

	err := h.factory.PublishAndNote(context.Background(), 20)
	if err == nil || !strings.Contains(err.Error(), "no permission") {
		t.Fatalf("error = %v; want the failed pull request", err)
	}
	if len(h.github.comments) != 1 {
		t.Fatalf("comments = %+v; want one", h.github.comments)
	}
	body := h.github.comments[0].Body
	if !strings.Contains(body, "could not publish") || !strings.Contains(body, "no permission") ||
		!strings.Contains(body, "factory publish --issue 20") {
		t.Errorf("body = %q; want the failure and the retry", body)
	}
}

func TestPublishAndNoteReportsAFailedComment(t *testing.T) {
	h := publishHarness(t)
	h.github.commentErr = errors.New("rate limited")
	if err := h.factory.PublishAndNote(context.Background(), 20); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v; want the failed comment", err)
	}
}

// The hook's card names where it runs: the board comes from the payload, the rest from
// the card's workspace, never from the hook's working directory.
func TestHookOptionsFromTheCardsWorkspace(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"hermes", "kanban", "--board", "torresposso-site", "show"}, out: `{"task":{"id":"t_21","title":"#21: the list","workspace_path":"/repos/torresposso.site-worktrees/12-21"}}`},
		{match: []string{"git", "-C", "/repos/torresposso.site-worktrees/12-21", "remote"}, out: "git@github.com:torresposso/torresposso.site.git\n"},
		{match: []string{"git", "-C", "/repos/torresposso.site-worktrees/12-21", "rev-parse"}, out: "/repos/torresposso.site/.git\n"},
	}}
	opts, err := hookOptions(context.Background(), tools, &Hook{Event: hookClaimed, Card: "t_21", Board: "torresposso-site"})
	if err != nil {
		t.Fatal(err)
	}
	want := Options{
		Repo:  "torresposso/torresposso.site",
		Clone: "/repos/torresposso.site",
		Trees: "/repos/torresposso.site-worktrees",
		Board: "torresposso-site",
		Home:  DefaultHome,
	}
	if opts != want {
		t.Errorf("opts = %+v; want %+v", opts, want)
	}
	if got := command(tools.calls[0]); got != "hermes kanban --board torresposso-site show t_21 --json" {
		t.Errorf("the card is read with %q", got)
	}
}

func TestHookOptionsOfACardWithoutACheckout(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"hermes"}, out: `{"task":{"id":"t_1","title":"Rotate the keys","workspace_path":""}}`},
	}}
	if _, err := hookOptions(context.Background(), tools, &Hook{Event: hookClaimed, Card: "t_1", Board: "default"}); !errors.Is(err, ErrNotARunCard) {
		t.Errorf("error = %v; want ErrNotARunCard", err)
	}
}

// The claimed payload names the gateway's current board, whichever board the card is on:
// the card is found on its own, and the hook is told where.
func TestHookOptionsFindTheCardOnItsBoard(t *testing.T) {
	missing := errors.New("hermes: no task t_51")
	tools := &recorder{replies: []reply{
		{match: []string{"hermes", "kanban", "--board", "torresposso-site", "show"}, err: missing},
		{match: []string{"hermes", "kanban", "boards", "list"}, out: `[{"slug":"default"},{"slug":"factory-sandbox"},{"slug":"torresposso-site"}]`},
		{match: []string{"hermes", "kanban", "--board", "default", "show"}, err: missing},
		{match: []string{"hermes", "kanban", "--board", "factory-sandbox", "show"}, out: `{"task":{"id":"t_51","title":"#51: the footer","workspace_path":"/repos/factory-sandbox-worktrees/50"}}`},
		{match: []string{"git", "-C", "/repos/factory-sandbox-worktrees/50", "remote"}, out: "git@github.com:torresposso/factory-sandbox.git\n"},
		{match: []string{"git", "-C", "/repos/factory-sandbox-worktrees/50", "rev-parse"}, out: "/repos/factory-sandbox/.git\n"},
	}}
	hook := Hook{Event: hookClaimed, Card: "t_51", Board: "torresposso-site"}
	opts, err := hookOptions(context.Background(), tools, &hook)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Board != "factory-sandbox" || hook.Board != "factory-sandbox" {
		t.Errorf("board = %q, hook board = %q; want factory-sandbox", opts.Board, hook.Board)
	}
	if opts.Repo != "torresposso/factory-sandbox" {
		t.Errorf("repo = %q", opts.Repo)
	}
}

func TestHookOptionsOfACardOnNoBoard(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"hermes", "kanban", "boards", "list"}, out: `[{"slug":"default"},{"slug":"factory-sandbox"}]`},
		{match: []string{"hermes", "kanban", "--board"}, err: errors.New("hermes: no task t_gone")},
	}}
	_, err := hookOptions(context.Background(), tools, &Hook{Event: hookClaimed, Card: "t_gone", Board: "default"})
	if err == nil || !strings.Contains(err.Error(), "no task t_gone") {
		t.Errorf("error = %v; want the board's own complaint", err)
	}
}
