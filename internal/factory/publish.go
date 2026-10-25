package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// The title prefixes the plan writes on a run's cards. `hermes kanban list --json`
// reports neither parents nor idempotency keys, so a phase that reads a run back has
// the card's title and its workspace to go on, and the plan writes both.
func ticketCardTitle(ticket int) string { return fmt.Sprintf("#%d: ", ticket) }

func integrateCardTitle(issue int) string { return fmt.Sprintf("Integrate #%d: ", issue) }

func reviewCardTitle(issue int) string { return fmt.Sprintf("Review #%d: ", issue) }

// runCard is one card of a run as the board reports it: what the card did, and what its
// worker said it verified.
type runCard struct {
	Card         BoardCard
	Summary      string
	Verification []string
	Branch       string
	Commit       string
}

// EvidenceImage is one screenshot a card left in its worktree. Name is the file's name,
// which is what the evidence branch carries: the directories are per ticket and the
// files are named after the ticket, so the name is already unique.
type EvidenceImage struct {
	Path string
	Name string
}

// Publish pushes the run's branch and opens its one pull request: the run's last card
// to be done, the pull request of `factory/<issue>` to not exist yet, and the branch to
// have commits of its own.
//
// It is idempotent in the only way that matters: an open pull request for the branch
// means the run is published, so the plan is empty and the address is reported instead
// of a second pull request being opened.
func (f *Factory) Publish(ctx context.Context, issue int) (*Plan, error) {
	run, err := f.readRun(ctx, issue)
	if err != nil {
		return nil, err
	}

	pulls, err := f.GitHub.PullRequests(ctx, branch(issue), "all")
	if err != nil {
		return nil, err
	}
	if len(pulls) > 0 {
		if pulls[0].State == stateOpen {
			f.URL = pulls[0].URL
			return &Plan{Issue: issue}, nil
		}
		return nil, fmt.Errorf("the pull request of #%d is already closed: %s", issue, pulls[0].URL)
	}

	cards, err := f.Board.List(ctx)
	if err != nil {
		return nil, err
	}
	mine := orderRunCards(run, f.runCards(issue, cards))
	gate, err := publishGate(run, mine)
	if err != nil {
		return nil, err
	}
	if gate.Status != statusDone {
		return nil, fmt.Errorf("the %s card of #%d is %s, not done, so the branch is not ready to publish: %s (%s)",
			cardRole(run, gate), issue, gate.Status, gate.ID, gate.Title)
	}

	diff, err := f.Git.Diff(ctx, branch(issue))
	if err != nil {
		return nil, err
	}
	if len(diff.Files) == 0 {
		return nil, fmt.Errorf("%s has no commits of its own against origin/main: there is nothing to publish", branch(issue))
	}

	reported, err := f.reportCards(ctx, mine)
	if err != nil {
		return nil, err
	}
	images, err := f.evidence(ctx, run)
	if err != nil {
		return nil, err
	}

	// The screenshots go up first, on a branch of their own, so the pull request's
	// Evidence section can link them without the change carrying a single PNG.
	steps := []Step{}
	if len(images) > 0 {
		steps = append(steps, PublishEvidenceStep{Ref: evidenceRef(issue), Files: images})
	}
	steps = append(steps,
		PushStep{Branch: branch(issue)},
		OpenPullRequestStep{PullRequest: NewPullRequest{
			Base:      "main",
			Head:      branch(issue),
			Title:     pullRequestTitle(run.Spec.Title),
			Body:      f.pullRequestBody(run, reported, images, gate, diff),
			Reviewers: f.reviewers(),
		}},
	)
	// The run is in review now, on the spec and every ticket the pull request closes.
	for _, number := range runIssues(run) {
		steps = append(steps, LabelStep{Issue: number, Add: []string{labelInReview}, Remove: []string{labelInProgress}})
	}
	return &Plan{Issue: issue, Steps: steps}, nil
}

// specMark is how a spec's title says it is a spec. It tells the issues apart; on the
// pull request, which is the change itself, it says nothing.
var specMark = regexp.MustCompile(`(?i)\s*\(spec\)\s*$`)

// pullRequestTitle is the spec's title without its mark.
func pullRequestTitle(spec string) string {
	if title := specMark.ReplaceAllString(spec, ""); title != "" {
		return title
	}
	return spec
}

// reviewers are who the pull request asks for a review: the repository's owner, who
// merges it. The pull request comes from the factory's own login, so without the ask
// nothing tells the owner it is waiting.
func (f *Factory) reviewers() []string {
	if owner := f.owner(); owner != "" {
		return []string{owner}
	}
	return nil
}

// owner is the repository's owner: who merges, and who answers a blocked card.
func (f *Factory) owner() string {
	owner, _, _ := strings.Cut(f.Opts.Repo, "/")
	return owner
}

// runIssues are the issues a run's pull request closes: the spec, then its tickets.
// A lone ticket is its own spec.
func runIssues(run PlannedRun) []int {
	issues := []int{run.Spec.Number}
	for _, ticket := range run.Tickets {
		if ticket.Issue.Number != run.Spec.Number {
			issues = append(issues, ticket.Issue.Number)
		}
	}
	return issues
}

// publishGate is the card whose `done` says the branch is ready to publish: the review
// card, which every run has.
func publishGate(run PlannedRun, cards []BoardCard) (BoardCard, error) {
	for _, card := range cards {
		if strings.HasPrefix(card.Title, reviewCardTitle(run.Spec.Number)) {
			return card, nil
		}
	}
	return BoardCard{}, fmt.Errorf("the run of #%d has no review card on the board: nothing says the branch is ready to publish",
		run.Spec.Number)
}

// cardRole is what a card of the run is, in the operator's words.
func cardRole(run PlannedRun, card BoardCard) string {
	switch {
	case strings.HasPrefix(card.Title, reviewCardTitle(run.Spec.Number)):
		return "review"
	case strings.HasPrefix(card.Title, integrateCardTitle(run.Spec.Number)):
		return "integration"
	default:
		return "coder"
	}
}

// orderRunCards puts a run's cards in the order the plan created them: the tickets in
// their order, then integration and review. A card of another run, or one the
// plan did not write, sorts last.
func orderRunCards(run PlannedRun, cards []BoardCard) []BoardCard {
	rank := func(card BoardCard) int {
		for index, ticket := range run.Tickets {
			if strings.HasPrefix(card.Title, ticketCardTitle(ticket.Issue.Number)) {
				return index
			}
		}
		issue := run.Spec.Number
		switch {
		case strings.HasPrefix(card.Title, integrateCardTitle(issue)):
			return len(run.Tickets)
		case strings.HasPrefix(card.Title, reviewCardTitle(issue)):
			return len(run.Tickets) + 1
		default:
			return len(run.Tickets) + 2
		}
	}
	ordered := append([]BoardCard{}, cards...)
	sort.SliceStable(ordered, func(a, b int) bool { return rank(ordered[a]) < rank(ordered[b]) })
	return ordered
}

// reportCards reads what each card of the run did: its last summary, and the metadata
// its worker reported, which is where the verification, the branch and the commit are.
func (f *Factory) reportCards(ctx context.Context, cards []BoardCard) ([]runCard, error) {
	reported := make([]runCard, 0, len(cards))
	for _, card := range cards {
		detail, err := f.Board.Show(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		one := runCard{Card: card, Summary: detail.Summary}
		runs, err := f.Board.Runs(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			fields, err := runs[len(runs)-1].Fields()
			if err != nil {
				return nil, err
			}
			one.Verification = checks(fields, MetadataVerification)
			one.Branch = field(fields, MetadataBranch)
			one.Commit = field(fields, MetadataCommit)
		}
		reported = append(reported, one)
	}
	return reported, nil
}

// checks reads what a worker says it verified. Workers write it as text, as a list, or
// as an object of check and result, so each shape becomes one line per check, an
// object's in key order; an empty value is no check.
func checks(fields map[string]any, key string) []string {
	switch value := fields[key].(type) {
	case string:
		if text := oneLine(value); text != "" {
			return []string{text}
		}
	case []any:
		lines := make([]string, 0, len(value))
		for _, item := range value {
			if text := oneLine(display(item)); text != "" {
				lines = append(lines, text)
			}
		}
		return lines
	case map[string]any:
		keys := make([]string, 0, len(value))
		for name := range value {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, name := range keys {
			lines = append(lines, oneLine(name+": "+display(value[name])))
		}
		return lines
	}
	return nil
}

// display is one metadata value as a reader sees it: text as it is, anything else as
// the JSON the worker wrote.
func display(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// field reads one metadata value the workers report as text; a key the worker left out,
// or one it wrote as a list, is empty rather than a surprise.
func field(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// evidence finds the screenshots the cards left. The run's own worktree is where the
// integrator gathers them, so its copy of a name wins over a ticket worktree's.
func (f *Factory) evidence(ctx context.Context, run PlannedRun) ([]EvidenceImage, error) {
	dirs := []string{runWorktree(f.Opts.Trees, run.Spec.Number)}
	for _, ticket := range run.Tickets {
		dirs = append(dirs, ticketWorktree(f.Opts.Trees, run.Spec.Number, ticket.Issue.Number))
	}
	found, err := f.Workspaces.Screenshots(ctx, dirs)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(found))
	images := make([]EvidenceImage, 0, len(found))
	for _, file := range found {
		name := path.Base(file)
		if seen[name] {
			continue
		}
		seen[name] = true
		images = append(images, EvidenceImage{Path: file, Name: name})
	}
	sort.Slice(images, func(a, b int) bool { return images[a].Name < images[b].Name })
	return images, nil
}

// evidenceRef is the branch the screenshots travel on. It is never merged and never
// carries the project's history: a throwaway repository pushes one commit to it.
func evidenceRef(issue int) string { return fmt.Sprintf("evidence/%d", issue) }

// evidenceURL is where a screenshot can be seen from the pull request. It is the one
// place the factory builds a GitHub address instead of asking for it.
func (f *Factory) evidenceURL(image EvidenceImage, issue int) string {
	name := (&url.URL{Path: image.Name}).EscapedPath()
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s?raw=true", f.Opts.Repo, evidenceRef(issue), name)
}

// pullRequestBody is what the pull request says: what it closes, what changed, what
// backs it, and what merging costs. Every section is built from what the board and git
// reported, and no model writes a word of it.
func (f *Factory) pullRequestBody(run PlannedRun, cards []runCard, images []EvidenceImage, gate BoardCard, diff Diff) string {
	issue := run.Spec.Number
	closes := []string{fmt.Sprintf("Closes #%d", issue)}
	for _, ticket := range run.Tickets {
		if ticket.Issue.Number != issue {
			closes = append(closes, fmt.Sprintf("Closes #%d", ticket.Issue.Number))
		}
	}

	parts := []string{strings.Join(closes, "\n")}
	parts = append(parts, section("Summary", f.summaryBody(run, cards)))
	parts = append(parts, section("Evidence", f.evidenceBody(run, cards, images, gate)))
	parts = append(parts, section("Merge danger", mergeDangerBody(diff)))
	return cardBody(parts...)
}

// summaryBody is what changed, in the last card's words, with one line per ticket.
func (f *Factory) summaryBody(run PlannedRun, cards []runCard) string {
	lines := []string{}
	if last := lastCardSummary(run, cards); last != "" {
		lines = append(lines, last)
	} else {
		lines = append(lines, "The cards reported no summary. Read the diff.")
	}
	perTicket := []string{}
	for _, ticket := range run.Tickets {
		for _, card := range cards {
			if card.Card.Title != ticketCardTitle(ticket.Issue.Number)+ticket.Issue.Title || card.Summary == "" {
				continue
			}
			perTicket = append(perTicket, fmt.Sprintf("- #%d %s: %s",
				ticket.Issue.Number, ticket.Issue.Title, oneLine(card.Summary)))
		}
	}
	if len(perTicket) > 0 {
		lines = append(lines, "What each ticket reported:", strings.Join(perTicket, "\n"))
	}
	return strings.Join(lines, "\n\n")
}

// lastCardSummary is the run's own account of itself: the review card read the whole
// branch, so it speaks last; when it left no summary, the coder card of a lone ticket
// does.
func lastCardSummary(run PlannedRun, cards []runCard) string {
	for _, card := range cards {
		if strings.HasPrefix(card.Card.Title, reviewCardTitle(run.Spec.Number)) && card.Summary != "" {
			return card.Summary
		}
	}
	for _, card := range cards {
		if strings.HasPrefix(card.Card.Title, ticketCardTitle(run.Spec.Number)) && card.Summary != "" {
			return card.Summary
		}
	}
	return ""
}

// evidenceBody is what backs the change: the verification each card ran, the commits,
// and the screenshots — which were taken on each ticket's own branch, before merging,
// and are never presented as the merged result.
func (f *Factory) evidenceBody(run PlannedRun, cards []runCard, images []EvidenceImage, gate BoardCard) string {
	lines := []string{}
	for _, card := range cards {
		lines = append(lines, fmt.Sprintf("- %s — %s", card.Card.Title, verified(card, card.Card.ID == gate.ID)))
		for _, check := range card.Verification {
			lines = append(lines, "  - "+check)
		}
	}
	if len(images) > 0 {
		links := make([]string, 0, len(images))
		for _, image := range images {
			links = append(links, fmt.Sprintf("![%s](%s)", image.Name, f.evidenceURL(image, run.Spec.Number)))
		}
		lines = append(lines,
			"",
			"Screenshots, taken on each ticket's own branch while it worked and before the branches met:",
			strings.Join(links, "\n"))
	}
	return strings.Join(lines, "\n")
}

// verified is one card's line of evidence: the commit it left, and whether it reported
// any check; the checks themselves are listed under the line.
func verified(card runCard, last bool) string {
	parts := []string{}
	if len(card.Verification) == 0 {
		parts = append(parts, "no verification reported")
	}
	switch {
	case card.Commit != "" && card.Branch != "":
		parts = append(parts, fmt.Sprintf("commit %s on `%s`", short(card.Commit), card.Branch))
	case card.Commit != "":
		parts = append(parts, "commit "+short(card.Commit))
	}
	if last {
		parts = append(parts, "the branch the pull request comes from")
	}
	return strings.Join(parts, ", ")
}

// mergeDangerBody is what a reader has to know before merging: how big the change is,
// and whether it touches a path that deploys, migrates or is otherwise not undone by
// the revert alone.
func mergeDangerBody(diff Diff) string {
	danger := riskyPaths(diff.Files)
	lines := []string{fmt.Sprintf("%d file(s), +%d/-%d against `main`.", len(diff.Files), diff.Insertions, diff.Deletions)}
	if len(danger) > 0 {
		lines = append(lines, fmt.Sprintf("Touches %s: %s.",
			strings.Join(danger, ", "),
			strings.Join(riskyFiles(diff.Files), ", ")))
	} else {
		lines = append(lines, "Nothing here changes the image, the CI, the dependencies or the database.")
	}
	if contains(danger, "the database") {
		lines = append(lines, "A migration that has already run on production does not go backwards by itself: undoing this is a new migration, not a revert.")
	} else {
		lines = append(lines, "Undoing it is reverting the merge.")
	}
	return strings.Join(lines, "\n")
}

// contains reports whether a list of what a diff touches names one of them.
func contains(list []string, want string) bool {
	for _, entry := range list {
		if entry == want {
			return true
		}
	}
	return false
}

// risky is what a path is, when it plays by different rules. The patterns are the
// stack's, not one repository's: `**` is any number of directories, none included. The
// order matters: it is the order the sentence reads.
var risky = []struct {
	what    string
	pattern string
}{
	{what: "the production image", pattern: "**/Dockerfile*"},
	{what: "the CI", pattern: ".github/**"},
	{what: "the database", pattern: "**/*migrations/**"},
	{what: "the dependencies", pattern: "go.mod"},
	{what: "the dependencies", pattern: "go.sum"},
	{what: "the dependencies", pattern: "package.json"},
	{what: "the dependencies", pattern: "package-lock.json"},
	{what: "the asset pipeline", pattern: "assets/**"},
	{what: "the committed static files", pattern: "**/static/**"},
}

// riskyPaths names the kinds of path a diff touches, in the order above and once each.
func riskyPaths(files []string) []string {
	var found []string
	seen := map[string]bool{}
	for _, entry := range risky {
		if seen[entry.what] {
			continue
		}
		for _, file := range files {
			if matchGlob(entry.pattern, file) {
				found = append(found, entry.what)
				seen[entry.what] = true
				break
			}
		}
	}
	return found
}

// riskyFiles is the files behind the kinds: the ones a reader has to look at.
func riskyFiles(files []string) []string {
	var found []string
	for _, file := range files {
		for _, entry := range risky {
			if matchGlob(entry.pattern, file) {
				found = append(found, "`"+file+"`")
				break
			}
		}
	}
	return found
}

// matchGlob matches a slash-separated path against a pattern whose segments are
// path.Match patterns, and where a `**` segment matches any number of segments.
func matchGlob(pattern, file string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pattern, file []string) bool {
	if len(pattern) == 0 {
		return len(file) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(file); skip++ {
			if matchSegments(pattern[1:], file[skip:]) {
				return true
			}
		}
		return false
	}
	if len(file) == 0 {
		return false
	}
	if ok, err := path.Match(pattern[0], file[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], file[1:])
}

// oneLine flattens what a worker wrote: metadata and summaries arrive as several lines,
// and a bullet with a line break in it is a broken bullet.
func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

// short is the first characters of a commit, which is what git prints and what a reader
// can look up.
func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
