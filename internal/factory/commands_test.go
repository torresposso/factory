package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a runner that runs nothing: it writes down every command and answers with
// what the test put in. The three CLI clients are the ones place a wrong flag could
// reach Basecamp from, and a flag the CLI does not have (or a `-` where it wants a path)
// fails nowhere in a fake — so every command's arguments are read here.
type recorder struct {
	calls   []record
	replies []reply
	err     error
}

// record is one command the client asked for.
type record struct {
	Name  string
	Args  []string
	Env   []string
	Stdin string
}

// reply is a canned answer, for the command whose leading arguments match.
type reply struct {
	match []string
	out   string
}

func (r *recorder) run(_ context.Context, env []string, stdin, name string, args ...string) (string, error) {
	r.calls = append(r.calls, record{Name: name, Args: args, Env: env, Stdin: stdin})
	if r.err != nil {
		return "", r.err
	}
	for _, answer := range r.replies {
		if hasPrefix(append([]string{name}, args...), answer.match) {
			return answer.out, nil
		}
	}
	return "", nil
}

// hasPrefix reports whether got starts with want. The last word of want only has to
// begin the argument it matches, because an API path carries its query in the same
// argument.
func hasPrefix(got, want []string) bool {
	if len(want) > len(got) {
		return false
	}
	for index, word := range want {
		if index == len(want)-1 {
			if !strings.HasPrefix(got[index], word) {
				return false
			}
			continue
		}
		if got[index] != word {
			return false
		}
	}
	return true
}

// command is one recorded command as a line, for a readable failure.
func command(call record) string { return call.Name + " " + strings.Join(call.Args, " ") }

func TestBoardCreatePassesWhatTheCLIAccepts(t *testing.T) {
	tools := &recorder{replies: []reply{{match: []string{"hermes"}, out: cardJSON}}}
	board := &CLIBoard{Board: "laiacaliente-app", commands: tools}

	created, err := board.Create(context.Background(), NewCard{
		Title:          "#21: the list",
		Body:           "## Ticket #21\n\nThe body.",
		Assignee:       "coder",
		Skills:         []string{"implement", "tdd"},
		Workspace:      "/trees/20-21",
		MaxRuntime:     2700,
		IdempotencyKey: "laiacaliente-app#20:code:21",
	}, []string{"t_parent"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	want := []string{
		"kanban", "--board", "laiacaliente-app",
		"create",
		// The title is a positional argument: `--title` belongs to `edit`, and passing
		// it here is an argparse error, after the worktrees have been made.
		"#21: the list",
		"--body-file", "-",
		"--assignee", "coder",
		"--workspace", "dir:/trees/20-21",
		"--max-runtime", "2700",
		"--completion-contract", "local-only",
		"--idempotency-key", "laiacaliente-app#20:code:21",
		"--parent", "t_parent",
		"--skill", "implement",
		"--skill", "tdd",
		"--json",
	}
	if got := tools.calls[0].Args; !equalStrings(got, want) {
		t.Errorf("create = %s\nwant   hermes %s", command(tools.calls[0]), strings.Join(want, " "))
	}
	for _, arg := range tools.calls[0].Args {
		if arg == "--title" {
			t.Error("create passes --title, which `hermes kanban create` does not have")
		}
	}
	if got := tools.calls[0].Stdin; got != "## Ticket #21\n\nThe body." {
		t.Errorf("the body went to stdin as %q", got)
	}
	if created.ID != "t_21" {
		t.Errorf("the created card = %+v", created)
	}
}

func TestBoardCreateWithoutParentsOrSkills(t *testing.T) {
	tools := &recorder{replies: []reply{{match: []string{"hermes"}, out: `{"id":"t_7"}`}}}
	board := &CLIBoard{Board: "b", commands: tools}

	if _, err := board.Create(context.Background(), NewCard{Title: "#7: x", Assignee: "coder"}, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, arg := range tools.calls[0].Args {
		if arg == "--parent" || arg == "--skill" {
			t.Errorf("create passes %s with nothing to pass: %s", arg, command(tools.calls[0]))
		}
	}
}

// A card the board reports without an id is a card the plan cannot use as a parent, and
// a parentless integration card would run too early.
func TestBoardCreateRefusesACardWithoutAnID(t *testing.T) {
	tools := &recorder{replies: []reply{{match: []string{"hermes"}, out: `{"title":"#21: the list"}`}}}
	board := &CLIBoard{Board: "b", commands: tools}

	if _, err := board.Create(context.Background(), NewCard{Title: "#21: the list", Assignee: "coder"}, nil); err == nil {
		t.Fatal("create: no error, want a card with no id to be refused")
	}
}

func TestBoardArchiveNamesEveryCard(t *testing.T) {
	tools := &recorder{}
	board := &CLIBoard{Board: "laiacaliente-app", commands: tools}

	if err := board.Archive(context.Background(), []string{"t_1", "t_2"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	want := "hermes kanban --board laiacaliente-app archive t_1 t_2"
	if got := command(tools.calls[0]); got != want {
		t.Errorf("archive = %q; want %q", got, want)
	}
}

// Archiving nothing is not a command: `hermes kanban archive` with no ids is an error.
func TestBoardArchiveOfNothingRunsNothing(t *testing.T) {
	tools := &recorder{}
	board := &CLIBoard{Board: "b", commands: tools}

	if err := board.Archive(context.Background(), nil); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(tools.calls) != 0 {
		t.Errorf("archive ran %s", command(tools.calls[0]))
	}
}

// The body of a pull request goes in as text: gh-axi reads `--body-file` as a path (there
// is no `-` for stdin), so a body passed that way fails after the branches are pushed.
func TestGitHubOpensThePullRequestWithTheBodyAsText(t *testing.T) {
	tools := &recorder{replies: []reply{{
		match: []string{"gh-axi", "pr", "create"},
		out:   "pull request created: PR#77 https://github.com/torresposso/laiacaliente-app/pull/77\n",
	}}}
	hub := &CLIGitHub{Repo: "torresposso/laiacaliente-app", Home: "/opt/data/profiles/publisher/home", commands: tools}
	body := "Closes #20\n\n## Summary\n\nThe «list» is there, and `make verify` passes.\n"

	url, err := hub.OpenPullRequest(context.Background(), NewPullRequest{
		Base: "main", Head: "factory/20", Title: "Portfolio", Body: body,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	want := []string{
		"pr", "create",
		"-R", "torresposso/laiacaliente-app",
		"--base", "main",
		"--head", "factory/20",
		"--title", "Portfolio",
		"--body", body,
	}
	if got := tools.calls[0].Args; !equalStrings(got, want) {
		t.Errorf("open = %s\nwant   gh-axi %s", command(tools.calls[0]), strings.Join(want, " "))
	}
	if call := command(tools.calls[0]); strings.Contains(call, "--body-file") {
		t.Errorf("the pull request's body is passed as a file path, which gh-axi reads off the disk: %s", call)
	}
	if got := tools.calls[0].Env; !equalStrings(got, []string{"HOME=/opt/data/profiles/publisher/home"}) {
		t.Errorf("HOME = %v; want the profile whose login can push", got)
	}
	if url != "https://github.com/torresposso/laiacaliente-app/pull/77" {
		t.Errorf("url = %q", url)
	}
}

// A comment's body is text too, and goes out under the login that can push.
func TestGitHubCommentsWithTheBodyAsText(t *testing.T) {
	tools := &recorder{}
	hub := &CLIGitHub{Repo: "torresposso/laiacaliente-app", Home: "/opt/data/profiles/publisher/home", commands: tools}
	body := "The factory's card `t_21` (#21: the list) is done.\n\n> The list is there."

	if err := hub.Comment(context.Background(), 21, body); err != nil {
		t.Fatal(err)
	}
	want := []string{"issue", "comment", "21", "-R", "torresposso/laiacaliente-app", "--body", body}
	if got := tools.calls[0]; got.Name != "gh-axi" || !equalStrings(got.Args, want) {
		t.Errorf("comment = %s\nwant      gh-axi %s", command(got), strings.Join(want, " "))
	}
	if got := tools.calls[0].Env; !equalStrings(got, []string{"HOME=/opt/data/profiles/publisher/home"}) {
		t.Errorf("HOME = %v; want the profile whose login can push", got)
	}
}

func TestGitHubReadsArePlainJSON(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"gh", "api", "repos/torresposso/laiacaliente-app/issues/20/sub_issues"}, out: `[[{"number":21,"state":"open"}],[{"number":22,"state":"closed"}]]`},
		{match: []string{"gh", "api", "repos/torresposso/laiacaliente-app/issues/20/dependencies/blocked_by"}, out: `[{"number":9,"state":"open"},{"number":8,"state":"closed"}]`},
		{match: []string{"gh", "api", "repos/torresposso/laiacaliente-app/pulls"}, out: `[{"number":30,"state":"closed","html_url":"https://x/pull/30","head":{"ref":"factory/20"}}]`},
		{match: []string{"gh", "api", "repos/torresposso/laiacaliente-app/issues/20"}, out: `{"number":20,"title":"Portfolio","state":"open"}`},
	}}
	hub := &CLIGitHub{Repo: "torresposso/laiacaliente-app", Home: "/home", commands: tools}

	subs, err := hub.SubIssues(context.Background(), 20)
	if err != nil {
		t.Fatalf("sub-issues: %v", err)
	}
	if len(subs) != 2 || subs[0].Number != 21 || subs[1].State != "closed" {
		t.Errorf("sub-issues = %+v; want both, open and closed", subs)
	}
	blockers, err := hub.BlockedBy(context.Background(), 20)
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0] != 9 {
		t.Errorf("blockers = %v; want the open one only", blockers)
	}
	pulls, err := hub.PullRequests(context.Background(), "factory/20", "all")
	if err != nil {
		t.Fatalf("pulls: %v", err)
	}
	if len(pulls) != 1 || pulls[0].State != "closed" || pulls[0].URL != "https://x/pull/30" {
		t.Errorf("pulls = %+v", pulls)
	}
	issue, err := hub.Issue(context.Background(), 20)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if issue.Title != "Portfolio" {
		t.Errorf("issue = %+v", issue)
	}

	wantCommands := []string{
		"gh api repos/torresposso/laiacaliente-app/issues/20/sub_issues --paginate --slurp",
		"gh api repos/torresposso/laiacaliente-app/issues/20/dependencies/blocked_by",
		"gh api repos/torresposso/laiacaliente-app/pulls?state=all&head=torresposso:factory/20&per_page=100",
		"gh api repos/torresposso/laiacaliente-app/issues/20",
	}
	got := make([]string, 0, len(tools.calls))
	for _, call := range tools.calls {
		got = append(got, command(call))
	}
	if !equalStrings(got, wantCommands) {
		t.Errorf("commands = %v\nwant      %v", got, wantCommands)
	}
	for _, call := range tools.calls {
		if call.Env[0] != "HOME=/home" {
			t.Errorf("a read ran without the profile's HOME: %s", command(call))
		}
	}
}

func TestGitCommands(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"git", "-C", "/clone", "worktree", "list"}, out: worktreePorcelain},
		{match: []string{"git", "-C", "/clone", "diff"}, out: numstat},
		{match: []string{"git", "-C", "/clone", "remote"}, out: "https://github.com/torresposso/laiacaliente-app.git\n"},
	}}
	git := &CLIGit{Clone: "/clone", Home: "/publisher", commands: tools}
	ctx := context.Background()

	if err := git.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if err := git.AddWorktree(ctx, "factory/20", "/trees/20"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := git.RemoveWorktree(ctx, "/trees/20-21"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := git.DeleteBranch(ctx, "factory/20-21"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := git.Push(ctx, "factory/20"); err != nil {
		t.Fatalf("push: %v", err)
	}
	trees, err := git.Worktrees(ctx)
	if err != nil {
		t.Fatalf("worktrees: %v", err)
	}
	diff, err := git.Diff(ctx, "factory/20")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	want := []string{
		"git -C /clone pull --ff-only origin main",
		"git -C /clone worktree add -b factory/20 /trees/20 origin/main",
		"git -C /clone worktree remove --force /trees/20-21",
		"git -C /clone branch -D factory/20-21",
		"git -C /clone push origin factory/20",
		"git -C /clone worktree list --porcelain",
		"git -C /clone diff --numstat origin/main...factory/20",
	}
	got := make([]string, 0, len(tools.calls))
	for _, call := range tools.calls {
		got = append(got, command(call))
	}
	if !equalStrings(got, want) {
		t.Errorf("commands = %v\nwant      %v", got, want)
	}
	// Pull and push talk to a private origin, so they are the commands that need the login.
	for _, call := range tools.calls {
		signing := strings.Contains(command(call), "push") || strings.Contains(command(call), "pull")
		if signing && !equalStrings(call.Env, []string{"HOME=/publisher"}) {
			t.Errorf("%s ran without the publisher's HOME: %v", command(call), call.Env)
		}
		if !signing && len(call.Env) != 0 {
			t.Errorf("%s ran with an environment %v; only pull and push need one", command(call), call.Env)
		}
	}
	if got := parseTrees(trees); !equalStrings(got, []string{"/clone main", "/trees/20 factory/20", "/trees/20-21 factory/20-21"}) {
		t.Errorf("worktrees = %v", got)
	}
	if len(diff.Files) != 3 || diff.Insertions != 22 || diff.Deletions != 3 {
		t.Errorf("diff = %+v; the binary file counts as nothing", diff)
	}
}

// parseTrees is a worktree as a line, for a readable failure.
func parseTrees(trees []Worktree) []string {
	lines := make([]string, 0, len(trees))
	for _, tree := range trees {
		lines = append(lines, tree.Path+" "+tree.Branch)
	}
	return lines
}

// The screenshots travel on a branch of their own, from a throwaway repository: the
// project's history is never touched.
func TestGitPublishesEvidenceFromAThrowawayRepository(t *testing.T) {
	dir := t.TempDir()
	screenshot := filepath.Join(dir, "21-list.png")
	if err := os.WriteFile(screenshot, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &recorder{replies: []reply{
		{match: []string{"git", "-C", "/clone", "remote"}, out: "https://github.com/torresposso/laiacaliente-app.git\n"},
	}}
	git := &CLIGit{Clone: "/clone", Home: "/publisher", commands: tools}

	if err := git.PublishEvidence(context.Background(), "evidence/20", []string{screenshot}); err != nil {
		t.Fatalf("publish evidence: %v", err)
	}

	commands := make([]string, 0, len(tools.calls))
	for _, call := range tools.calls {
		commands = append(commands, command(call))
	}
	first := "git -C /clone remote get-url origin"
	if commands[0] != first {
		t.Errorf("first command = %q; want %q", commands[0], first)
	}
	if len(commands) != 5 {
		t.Fatalf("commands = %v; want the url, init, add, commit and push", commands)
	}
	for _, want := range []string{
		" init -q -b main",
		" add .",
		" commit -q -m evidence for evidence/20",
		" push -f https://github.com/torresposso/laiacaliente-app.git HEAD:refs/heads/evidence/20",
	} {
		if !strings.Contains(commands[1], want) && !strings.Contains(commands[2], want) &&
			!strings.Contains(commands[3], want) && !strings.Contains(commands[4], want) {
			t.Errorf("no command carries %q: %v", want, commands)
		}
	}
	for _, call := range tools.calls[1:] {
		if !equalStrings(call.Env, []string{"HOME=/publisher"}) {
			t.Errorf("%s ran without the login that can push", command(call))
		}
	}
}

func TestBoardReadsWhatTheCLIReports(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"hermes", "kanban", "--board", "b", "list"}, out: `[{"id":"t_1","title":"#21: the list","status":"done","workspace_path":"/trees/20-21","skills":["implement"]}]`},
		{match: []string{"hermes", "kanban", "--board", "b", "show"}, out: `{"task":{"id":"t_1","status":"done"},"latest_summary":"The list is there.","parents":["t_0"],"children":[]}`},
		{match: []string{"hermes", "kanban", "--board", "b", "runs"}, out: `[{"profile":"coder","status":"done","metadata":{"verification":"make verify (exit 0)","branch":"factory/20-21","commit":"abc123","changed_files":["a.go"]}}]`},
	}}
	board := &CLIBoard{Board: "b", commands: tools}
	ctx := context.Background()

	cards, err := board.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cards) != 1 || cards[0].ID != "t_1" || cards[0].WorkspacePath != "/trees/20-21" {
		t.Errorf("cards = %+v", cards)
	}
	detail, err := board.Show(ctx, "t_1")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if detail.Summary != "The list is there." || !equalStrings(detail.Parents, []string{"t_0"}) {
		t.Errorf("detail = %+v", detail)
	}
	runs, err := board.Runs(ctx, "t_1")
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	fields, err := runs[0].Fields()
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if field(fields, MetadataVerification) != "make verify (exit 0)" || field(fields, MetadataCommit) != "abc123" {
		t.Errorf("fields = %v", fields)
	}
	if field(fields, "changed_files") != "" {
		t.Error("changed_files is a list, and the factory reads text: it must come back empty")
	}
	if field(fields, "nothing") != "" {
		t.Error("a key the worker left out must be empty")
	}

	want := []string{
		"hermes kanban --board b list --json",
		"hermes kanban --board b show t_1 --json",
		"hermes kanban --board b runs t_1 --json",
	}
	got := make([]string, 0, len(tools.calls))
	for _, call := range tools.calls {
		got = append(got, command(call))
	}
	if !equalStrings(got, want) {
		t.Errorf("commands = %v\nwant      %v", got, want)
	}
}

// The two shapes `git worktree list --porcelain` and `git diff --numstat` come in.
const worktreePorcelain = `worktree /clone
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /trees/20
HEAD 2222222222222222222222222222222222222222
branch refs/heads/factory/20

worktree /trees/20-21
HEAD 3333333333333333333333333333333333333333
branch refs/heads/factory/20-21
`

const numstat = `12	2	internal/web/portfolio.go
10	1	internal/web/portfolio.templ
-	-	internal/web/static/logo.png
`

const cardJSON = `{
  "id": "t_21",
  "title": "#21: the list",
  "status": "todo",
  "assignee": "coder",
  "workspace_path": "/trees/20-21"
}`

// Everywhere reads the board list, then each board by its slug: the preview ports are the
// machine's, and every board is a separate database the CLI only reads when named.
func TestBoardEverywhereReadsEveryBoard(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"hermes", "kanban", "boards", "list"}, out: `[{"slug":"laiacaliente-app"},{"slug":"factory-sandbox"}]`},
		{match: []string{"hermes", "kanban", "--board", "laiacaliente-app", "list"}, out: `[{"id":"t_1","status":"running","body":"Preview port: 8100"}]`},
		{match: []string{"hermes", "kanban", "--board", "factory-sandbox", "list"}, out: `[{"id":"t_2","status":"todo","body":"Preview port: 8101"}]`},
	}}
	board := &CLIBoard{Board: "laiacaliente-app", commands: tools}

	cards, err := board.Everywhere(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].ID != "t_1" || cards[1].ID != "t_2" {
		t.Errorf("cards = %+v; want t_1 and t_2", cards)
	}
	want := []string{
		"hermes kanban boards list --json",
		"hermes kanban --board laiacaliente-app list --json",
		"hermes kanban --board factory-sandbox list --json",
	}
	if len(tools.calls) != len(want) {
		t.Fatalf("calls = %d; want %d", len(tools.calls), len(want))
	}
	for index, call := range tools.calls {
		if got := command(call); got != want[index] {
			t.Errorf("call %d = %q; want %q", index, got, want[index])
		}
	}
}

// From a worktree the clone is still the main checkout: git's common dir is the main
// checkout's .git, and the hooks run in the run's worktree.
func TestDetectOptionsFromAWorktree(t *testing.T) {
	tools := &recorder{replies: []reply{
		{match: []string{"git", "-C", "/repos/torresposso.site-worktrees/12", "remote"}, out: "git@github.com:torresposso/torresposso.site.git\n"},
		{match: []string{"git", "-C", "/repos/torresposso.site-worktrees/12", "rev-parse"}, out: "/repos/torresposso.site/.git\n"},
	}}
	opts, err := detectOptions(context.Background(), tools, "/repos/torresposso.site-worktrees/12")
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
	if got := command(tools.calls[1]); got != "git -C /repos/torresposso.site-worktrees/12 rev-parse --path-format=absolute --git-common-dir" {
		t.Errorf("the clone is read with %q", got)
	}
}
