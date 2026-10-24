package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// metadata is what a worker reports, as the board hands it back.
func metadata(fields map[string]string) json.RawMessage {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return encoded
}

// fieldsOf is what the tests expect a worker to have reported.
func fieldsOf(ticket int, verification, commit string) map[string]string {
	return map[string]string{
		MetadataVerification: verification,
		MetadataBranch:       "factory/20-" + itoa(ticket),
		MetadataCommit:       commit,
	}
}

func itoa(number int) string {
	if number < 10 {
		return string(rune('0' + number))
	}
	return string(rune('0'+number/10)) + string(rune('0'+number%10))
}

// publishHarness is a run that is done and ready: two tickets, a review card that is
// done, worktrees with screenshots, and a branch with commits of its own.
func publishHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	h.board.cards = []BoardCard{
		{ID: "t_21", Title: "#21: the list", Status: statusDone, WorkspacePath: "/trees/20-21"},
		{ID: "t_22", Title: "#22: the form", Status: statusDone, WorkspacePath: "/trees/20-22"},
		{ID: "t_integrate", Title: "Integrate #20: Portfolio", Status: statusDone, WorkspacePath: "/trees/20"},
		{ID: "t_review", Title: "Review #20: Portfolio", Status: statusDone, WorkspacePath: "/trees/20"},
		{ID: "t_publish", Title: "Publish #20: Portfolio", Status: "todo", WorkspacePath: "/trees/20"},
	}
	h.board.details = map[string]BoardCardDetail{
		"t_21":        {Card: h.board.cards[0], Summary: "The list is there."},
		"t_22":        {Card: h.board.cards[1], Summary: "The form creates a Proyecto."},
		"t_integrate": {Card: h.board.cards[2], Summary: "Both branches merged clean."},
		"t_review":    {Card: h.board.cards[3], Summary: "Reviewed the branch: the list and the form are what the spec asks for."},
		"t_publish":   {Card: h.board.cards[4], Summary: ""},
	}
	h.board.runs = map[string][]CardRun{
		"t_21": {{Profile: "coder", Status: "done", Metadata: metadata(fieldsOf(21, "make verify (exit 0)", "1111111aaaaaaa"))}},
		"t_22": {{Profile: "coder", Status: "done", Metadata: metadata(fieldsOf(22, "make verify (exit 0)", "2222222bbbbbbb"))}},
		"t_integrate": {{Profile: "coder", Status: "done", Metadata: metadata(map[string]string{
			MetadataVerification: "make verify (exit 0)", MetadataBranch: "factory/20", MetadataCommit: "3333333ccccccc",
		})}},
		"t_review": {{Profile: "qa", Status: "done", Metadata: metadata(map[string]string{
			MetadataVerification: "make verify (exit 0)", MetadataBranch: "factory/20", MetadataCommit: "4444444ddddddd",
		})}},
		"t_publish": {},
	}
	h.git.diff = Diff{
		Files:      []string{"internal/web/portfolio.go", "internal/web/portfolio.templ"},
		Insertions: 120,
		Deletions:  8,
	}
	h.files.screenshots = []string{"/trees/20/21-list.png", "/trees/20/22-form.png"}
	return h
}

func TestPublishPushesAndOpensThePullRequest(t *testing.T) {
	h := publishHarness(t)

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []string{"publish-evidence", "push", "open-pull-request"}
	if got := actionsOf(plan); !equalStrings(got, want) {
		t.Fatalf("steps = %v; want %v", got, want)
	}
	evidence, ok := plan.Steps[0].(PublishEvidenceStep)
	if !ok || evidence.Ref != "evidence/20" || len(evidence.Files) != 2 {
		t.Errorf("evidence step = %+v; want two screenshots on evidence/20", plan.Steps[0])
	}
	push, ok := plan.Steps[1].(PushStep)
	if !ok || push.Branch != "factory/20" {
		t.Errorf("push step = %+v; want factory/20", plan.Steps[1])
	}
	pr, ok := plan.Steps[2].(OpenPullRequestStep)
	if !ok {
		t.Fatalf("the last step is %T; want the pull request", plan.Steps[2])
	}
	if pr.PullRequest.Base != "main" || pr.PullRequest.Head != "factory/20" {
		t.Errorf("pull request = %s -> %s; want main <- factory/20", pr.PullRequest.Base, pr.PullRequest.Head)
	}
	if pr.PullRequest.Title != "Portfolio" {
		t.Errorf("title = %q", pr.PullRequest.Title)
	}

	if err := h.factory.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !equalStrings(h.git.pushed, []string{"factory/20"}) {
		t.Errorf("pushed = %v", h.git.pushed)
	}
	if len(h.git.published) != 1 || h.git.published[0].Ref != "evidence/20" {
		t.Errorf("published = %+v", h.git.published)
	}
	if len(h.github.opened) != 1 {
		t.Fatalf("opened %d pull requests; want exactly one", len(h.github.opened))
	}
	if h.factory.URL != "https://github.com/torresposso/laiacaliente-app/pull/77" {
		t.Errorf("URL = %q", h.factory.URL)
	}
}

// The pull request closes the spec and every ticket of it, and never closes the same
// issue twice.
func TestPullRequestBodyClosesEveryTicketOnce(t *testing.T) {
	h := publishHarness(t)

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	body := plan.Steps[len(plan.Steps)-1].(OpenPullRequestStep).PullRequest.Body

	for _, want := range []string{"Closes #20", "Closes #21", "Closes #22"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %q:\n%s", want, body)
		}
	}
	if count := strings.Count(body, "Closes #20"); count != 1 {
		t.Errorf("the body closes #20 %d times", count)
	}
	if !strings.HasPrefix(body, "Closes #20\n") {
		t.Errorf("the body must start with the Closes lines:\n%s", body)
	}
}

// The four sections the route asked for, each from what the board and git reported.
func TestPullRequestBodySections(t *testing.T) {
	h := publishHarness(t)

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	body := plan.Steps[len(plan.Steps)-1].(OpenPullRequestStep).PullRequest.Body

	for _, want := range []string{
		"## Summary",
		"Reviewed the branch: the list and the form are what the spec asks for.",
		"What each ticket reported:",
		"- #21 the list: The list is there.",
		"## Evidence",
		"make verify (exit 0)",
		"commit 2222222b on `factory/20-22`",
		"the branch the pull request comes from",
		"## Merge danger",
		"2 file(s), +120/-8 against `main`.",
		"![21-list.png](https://github.com/torresposso/laiacaliente-app/blob/evidence/20/21-list.png?raw=true)",
		"![22-form.png](https://github.com/torresposso/laiacaliente-app/blob/evidence/20/22-form.png?raw=true)",
		"before the branches met",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Publish #20") {
		t.Errorf("the body reports the publisher card, which did nothing:\n%s", body)
	}
}

// Workers report verification as text, as a list or as an object of check and result,
// as run #43 did; each check becomes its own line under the card, and a card that left
// the key out says so.
func TestEvidenceListsEveryShapeOfVerification(t *testing.T) {
	h := publishHarness(t)
	raw := func(text string) json.RawMessage { return json.RawMessage(text) }
	h.board.runs["t_21"] = []CardRun{{Profile: "coder", Status: "done", Metadata: raw(`{
		"verification": ["make verify (exit 0)", "make preview at 390px: drawer opens"],
		"branch": "factory/20-21", "commit": "1111111aaaaaaa"}`)}}
	h.board.runs["t_review"] = []CardRun{{Profile: "qa", Status: "done", Metadata: raw(`{
		"verification": {"make_verify": "exit 0", "screenshots": 4, "go test": {"failed": 0}},
		"branch": "factory/20", "commit": "4444444ddddddd"}`)}}
	h.board.runs["t_integrate"] = []CardRun{{Profile: "coder", Status: "done", Metadata: raw(`{
		"branch": "factory/20", "commit": "3333333ccccccc"}`)}}

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	body := plan.Steps[len(plan.Steps)-1].(OpenPullRequestStep).PullRequest.Body

	for _, want := range []string{
		"- #21: the list — commit 1111111a on `factory/20-21`\n  - make verify (exit 0)\n  - make preview at 390px: drawer opens\n",
		"- #22: the form — commit 2222222b on `factory/20-22`\n  - make verify (exit 0)\n",
		"- Integrate #20: Portfolio — no verification reported, commit 3333333c on `factory/20`\n",
		"  - go test: {\"failed\":0}\n  - make_verify: exit 0\n  - screenshots: 4",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %q:\n%s", want, body)
		}
	}
}

// One ticket is the whole run: the review card after its coder card is the one that has
// to be done, and the one the body quotes.
func TestPublishOfALoneTicket(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	h.board.cards = []BoardCard{
		{ID: "t_code", Title: "#7: The mobile menu", Status: statusDone, WorkspacePath: "/trees/7"},
		{ID: "t_review", Title: "Review #7: The mobile menu", Status: statusDone, WorkspacePath: "/trees/7"},
		{ID: "t_publish", Title: "Publish #7: The mobile menu", Status: "todo", WorkspacePath: "/trees/7"},
	}
	h.board.details = map[string]BoardCardDetail{
		"t_code":    {Card: h.board.cards[0], Summary: "The drawer opens on mobile."},
		"t_review":  {Card: h.board.cards[1], Summary: "Reviewed; the drawer closes on the overlay."},
		"t_publish": {Card: h.board.cards[2]},
	}
	h.board.runs = map[string][]CardRun{
		"t_code": {{Profile: "coder", Status: "done", Metadata: metadata(map[string]string{
			MetadataVerification: "make verify (exit 0)", MetadataBranch: "factory/7", MetadataCommit: "abcdef1234567890",
		})}},
	}
	h.git.diff = Diff{Files: []string{"internal/web/layout.templ"}, Insertions: 12, Deletions: 3}

	plan, err := h.factory.Publish(context.Background(), 7)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if got := actionsOf(plan); !equalStrings(got, []string{"push", "open-pull-request"}) {
		t.Errorf("steps = %v; want no evidence step and no card step", got)
	}
	body := plan.Steps[1].(OpenPullRequestStep).PullRequest.Body
	if strings.Count(body, "Closes #") != 1 {
		t.Errorf("a lone ticket closes itself once:\n%s", body)
	}
	if !strings.Contains(body, "Reviewed; the drawer closes on the overlay.") {
		t.Errorf("the body does not quote the review card:\n%s", body)
	}
}

// The review card is what says the branch is ready. Anything else in flight, or a card
// that never ran, means the pull request is not due yet.
func TestPublishRefusesAnUnfinishedRun(t *testing.T) {
	h := publishHarness(t)
	h.board.cards[3].Status = "running"
	h.board.details["t_review"] = BoardCardDetail{Card: h.board.cards[3], Summary: ""}

	_, err := h.factory.Publish(context.Background(), 20)
	if err == nil {
		t.Fatal("publish: no error, want the running review card to refuse it")
	}
	if !strings.Contains(err.Error(), "t_review") || !strings.Contains(err.Error(), "running") {
		t.Errorf("error = %q; want the card and its status", err)
	}
	if len(h.github.opened) > 0 || len(h.git.pushed) > 0 {
		t.Error("publish pushed something for a run that is not finished")
	}
}

func TestPublishRefusesARunWithNoReviewCard(t *testing.T) {
	h := publishHarness(t)
	h.board.cards = h.board.cards[:1]

	_, err := h.factory.Publish(context.Background(), 20)
	if err == nil {
		t.Fatal("publish: no error, want a missing review card to refuse it")
	}
	if !strings.Contains(err.Error(), "no review card") {
		t.Errorf("error = %q", err)
	}
}

// An open pull request means the run is published: publishing again reports it instead
// of opening a second one.
func TestPublishStopsWhenThePullRequestIsAlreadyOpen(t *testing.T) {
	h := publishHarness(t)
	h.github.pulls = map[string][]PullRequest{
		"factory/20": {{Number: 42, State: "open", URL: "https://example.test/pull/42"}},
	}

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("steps = %v; want none", plan.Steps)
	}
	if h.factory.URL != "https://example.test/pull/42" {
		t.Errorf("URL = %q; want the open pull request", h.factory.URL)
	}
}

// A closed pull request is a published run: opening another one would be a second pull
// request for the same work.
func TestPublishRefusesAClosedPullRequest(t *testing.T) {
	h := publishHarness(t)
	h.github.pulls = map[string][]PullRequest{
		"factory/20": {{Number: 42, State: "closed", URL: "https://example.test/pull/42"}},
	}

	_, err := h.factory.Publish(context.Background(), 20)
	if err == nil {
		t.Fatal("publish: no error, want the closed pull request to refuse it")
	}
	if !strings.Contains(err.Error(), "already closed") {
		t.Errorf("error = %q", err)
	}
}

// A branch with nothing of its own is a run that never got anywhere, and an empty pull
// request would be worse than an error.
func TestPublishRefusesAnEmptyBranch(t *testing.T) {
	h := publishHarness(t)
	h.git.diff = Diff{}

	_, err := h.factory.Publish(context.Background(), 20)
	if err == nil {
		t.Fatal("publish: no error, want a branch with no commits to be refused")
	}
	if !strings.Contains(err.Error(), "no commits of its own") {
		t.Errorf("error = %q", err)
	}
	if len(h.git.pushed) > 0 {
		t.Error("publish pushed an empty branch")
	}
}

// The screenshots the integrator gathered in the run's worktree are the ones that go up,
// and a name only travels once.
func TestEvidencePrefersTheRunWorktree(t *testing.T) {
	h := publishHarness(t)
	h.files.screenshots = []string{
		"/trees/20/21-list.png",
		"/trees/20/22-form.png",
		"/trees/20-21/21-list.png",
	}

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	evidence := plan.Steps[0].(PublishEvidenceStep)

	got := make([]string, 0, len(evidence.Files))
	for _, file := range evidence.Files {
		got = append(got, file.Name)
	}
	if !equalStrings(got, []string{"21-list.png", "22-form.png"}) {
		t.Errorf("evidence = %v; want the run worktree's copy of each name", got)
	}
	if evidence.Files[0].Path != "/trees/20/21-list.png" {
		t.Errorf("the file used is %q; want the run worktree's", evidence.Files[0].Path)
	}
}

// A run with no screenshots still opens its pull request.
func TestPublishWithoutEvidence(t *testing.T) {
	h := publishHarness(t)
	h.files.screenshots = nil

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := actionsOf(plan); !equalStrings(got, []string{"push", "open-pull-request"}) {
		t.Errorf("steps = %v; want no evidence step", got)
	}
	body := plan.Steps[1].(OpenPullRequestStep).PullRequest.Body
	if strings.Contains(body, "Screenshots,") {
		t.Errorf("the body promises screenshots there are none of:\n%s", body)
	}
}

// The merge danger names the paths that play by other rules: a migration that has run on
// production does not go backwards, and the image and the CI are rebuilt on merge.
func TestMergeDangerNamesTheRiskyPaths(t *testing.T) {
	h := publishHarness(t)
	h.git.diff = Diff{
		Files:      []string{"internal/web/portfolio.go", "pocketbase/pb_migrations/1760000200_create_portfolio.go", "Dockerfile", ".github/workflows/verify.yml"},
		Insertions: 200,
		Deletions:  10,
	}

	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	body := plan.Steps[len(plan.Steps)-1].(OpenPullRequestStep).PullRequest.Body

	for _, want := range []string{
		"4 file(s), +200/-10 against `main`.",
		"the production image",
		"the CI",
		"the database",
		"`pocketbase/pb_migrations/1760000200_create_portfolio.go`",
		"`Dockerfile`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Nothing here changes") {
		t.Errorf("the body says nothing is risky, and a migration is:\n%s", body)
	}
	if !strings.Contains(body, "does not go backwards") {
		t.Errorf("the body does not say what a migration costs to undo:\n%s", body)
	}
}

func TestMergeDangerNamesThePocketBaseImage(t *testing.T) {
	got := mergeDangerBody(Diff{Files: []string{"pocketbase/Dockerfile"}, Insertions: 2, Deletions: 2})

	if !strings.Contains(got, "the production image") || !strings.Contains(got, "`pocketbase/Dockerfile`") {
		t.Errorf("merge danger = %q", got)
	}
}

// The patterns are the stack's, so they find the same kinds of file in any repository
// built on it, at any depth.
func TestMergeDangerPatternsAreTheStacks(t *testing.T) {
	for file, want := range map[string]string{
		"Dockerfile.site":                 "the production image",
		"services/api/Dockerfile":         "the production image",
		"db/migrations/0001_init.sql":     "the database",
		"pocketbase/pb_migrations/1_a.go": "the database",
		"assets/styles.css":               "the asset pipeline",
		"internal/web/static/app.css":     "the committed static files",
		"static/favicon.svg":              "the committed static files",
		"package-lock.json":               "the dependencies",
		".github/workflows/verify.yml":    "the CI",
	} {
		got := riskyPaths([]string{file})
		if len(got) != 1 || got[0] != want {
			t.Errorf("riskyPaths(%q) = %v; want [%s]", file, got, want)
		}
	}
	for _, file := range []string{"internal/web/home.templ", "docs/migrations.md", "web/assets/logo.svg", "staticcheck.conf"} {
		if got := riskyPaths([]string{file}); len(got) != 0 {
			t.Errorf("riskyPaths(%q) = %v; want nothing", file, got)
		}
	}
}

func TestMergeDangerOfAnOrdinaryChange(t *testing.T) {
	diff := Diff{Files: []string{"internal/web/home.templ"}, Insertions: 3, Deletions: 1}

	got := mergeDangerBody(diff)

	if !strings.Contains(got, "Nothing here changes the image") || !strings.Contains(got, "Undoing it is reverting the merge.") {
		t.Errorf("merge danger = %q", got)
	}
	if strings.Contains(got, "Railway") || strings.Contains(got, "PocketBase") {
		t.Errorf("merge danger names one repository's deploy: %q", got)
	}
}

// The cards of a run are read back in the order the plan created them, which is the
// order their summaries read in.
func TestOrderRunCardsFollowsThePlan(t *testing.T) {
	run := PlannedRun{
		Spec:    Issue{Number: 20},
		Tickets: []Ticket{{Issue: Issue{Number: 21}}, {Issue: Issue{Number: 22}}},
	}
	cards := []BoardCard{
		{ID: "publish", Title: "Publish #20: Portfolio"},
		{ID: "review", Title: "Review #20: Portfolio"},
		{ID: "second", Title: "#22: the form"},
		{ID: "integrate", Title: "Integrate #20: Portfolio"},
		{ID: "first", Title: "#21: the list"},
	}

	ordered := orderRunCards(run, cards)
	ids := make([]string, 0, len(ordered))
	for _, card := range ordered {
		ids = append(ids, card.ID)
	}
	if !equalStrings(ids, []string{"first", "second", "integrate", "review", "publish"}) {
		t.Errorf("order = %v", ids)
	}
}

// A worker's summary arrives as several lines: a bullet with a line break in it is a
// broken bullet, so the metadata is flattened to one line.
func TestOneLineFlattensWhatAWorkerWrote(t *testing.T) {
	got := oneLine("make verify (exit 0)\nand `make preview` looked right\n")
	if got != "make verify (exit 0) and `make preview` looked right" {
		t.Errorf("oneLine = %q", got)
	}
}

// The report of a publish carries the body, which is how the pull request is read before
// it exists anywhere.
func TestReportOfAPublishCarriesTheBody(t *testing.T) {
	h := publishHarness(t)
	plan, err := h.factory.Publish(context.Background(), 20)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	report := NewReport(plan, h.factory.Opts.Board, true)

	if report.PullRequest == nil {
		t.Fatal("the report has no pull request")
	}
	if report.PullRequest.Head != "factory/20" || !strings.Contains(report.PullRequest.Body, "## Evidence") {
		t.Errorf("report = %+v", report.PullRequest)
	}
}
