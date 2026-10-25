package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The states GitHub reports a pull request in.
const (
	stateOpen   = "open"
	stateClosed = "closed"
)

// GitHub is the repository on GitHub: the spec the run reads, and the one pull request
// it opens at the end.
type GitHub interface {
	// Issue reads one issue.
	Issue(ctx context.Context, number int) (Issue, error)
	// SubIssues reads the issue's sub-issues, open and closed.
	SubIssues(ctx context.Context, number int) ([]Issue, error)
	// BlockedBy reads the numbers of the open issues that block this one.
	BlockedBy(ctx context.Context, number int) ([]int, error)
	// PullRequests reads the pull requests whose head is branch, newest first, in the
	// given state: open, closed or all.
	PullRequests(ctx context.Context, branch, state string) ([]PullRequest, error)
	// OpenPullRequest opens a pull request and returns its address.
	OpenPullRequest(ctx context.Context, pr NewPullRequest) (string, error)
	// Comment adds a comment to an issue: how the tickets say what the run is doing.
	Comment(ctx context.Context, number int, body string) error
}

// PullRequest is the little the factory reads about one.
type PullRequest struct {
	Number int
	State  string
	URL    string
	Head   string
}

// NewPullRequest is the pull request the factory opens.
type NewPullRequest struct {
	Base  string
	Head  string
	Title string
	Body  string
}

// CLIGitHub talks to GitHub through `gh` and `gh-axi`. Reads use `gh api`, whose output
// is the JSON this package parses; gh-axi renders its answers for a model to read,
// which is the right shape for a terminal and the wrong one for a parser. The pull
// request is opened with `gh-axi pr create`, as docs/hermes.md says.
type CLIGitHub struct {
	// Repo is owner/name.
	Repo string
	// Home is HOME for every call: reads and the push both need the login that only
	// the publisher profile's home carries.
	Home     string
	commands runner
}

func (g *CLIGitHub) Issue(ctx context.Context, number int) (Issue, error) {
	out, err := g.api(ctx, fmt.Sprintf("repos/%s/issues/%d", g.Repo, number))
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		return Issue{}, fmt.Errorf("reading issue #%d: %w", number, err)
	}
	return issue, nil
}

func (g *CLIGitHub) SubIssues(ctx context.Context, number int) ([]Issue, error) {
	// A spec with more tickets than one page of results must not lose tickets, so the
	// pages are asked for and flattened here.
	out, err := g.api(ctx, fmt.Sprintf("repos/%s/issues/%d/sub_issues", g.Repo, number), "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

func (g *CLIGitHub) BlockedBy(ctx context.Context, number int) ([]int, error) {
	out, err := g.api(ctx, fmt.Sprintf("repos/%s/issues/%d/dependencies/blocked_by", g.Repo, number))
	if err != nil {
		return nil, err
	}
	issues, err := parseIssues(out)
	if err != nil {
		return nil, err
	}
	var numbers []int
	for _, issue := range openIssues(issues) {
		numbers = append(numbers, issue.Number)
	}
	return numbers, nil
}

func (g *CLIGitHub) PullRequests(ctx context.Context, branch, state string) ([]PullRequest, error) {
	owner, _, ok := strings.Cut(g.Repo, "/")
	if !ok {
		return nil, fmt.Errorf("repo %q is not owner/name", g.Repo)
	}
	out, err := g.api(ctx, fmt.Sprintf("repos/%s/pulls?state=%s&head=%s:%s&per_page=100", g.Repo, state, owner, branch))
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number  int    `json:"number"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("reading pull requests of %s: %w", branch, err)
	}
	pulls := make([]PullRequest, 0, len(raw))
	for _, one := range raw {
		pulls = append(pulls, PullRequest{
			Number: one.Number,
			State:  one.State,
			URL:    one.HTMLURL,
			Head:   one.Head.Ref,
		})
	}
	return pulls, nil
}

// pullRequestURL finds the pull request's address in what gh-axi printed. gh-axi
// formats its answers for a model, so the address is picked out instead of parsed.
var pullRequestURL = regexp.MustCompile(`https://[^\s"'<>]+/pull/\d+`)

func (g *CLIGitHub) OpenPullRequest(ctx context.Context, pr NewPullRequest) (string, error) {
	// The body goes in as text, not through --body-file: gh-axi reads that flag as a
	// path (there is no `-` for stdin), and its arguments are handed to the command
	// directly, so a body with newlines and backticks arrives whole.
	out, err := g.commands.run(ctx, g.env(), "", "gh-axi", "pr", "create",
		"-R", g.Repo,
		"--base", pr.Base,
		"--head", pr.Head,
		"--title", pr.Title,
		"--body", pr.Body,
	)
	if err != nil {
		return "", err
	}
	url := pullRequestURL.FindString(out)
	if url == "" {
		return "", fmt.Errorf("gh-axi pr create said nothing that looks like a pull request address: %s", strings.TrimSpace(out))
	}
	return url, nil
}

func (g *CLIGitHub) Comment(ctx context.Context, number int, body string) error {
	// The body goes in as text for the same reason as the pull request's.
	_, err := g.commands.run(ctx, g.env(), "", "gh-axi", "issue", "comment", strconv.Itoa(number),
		"-R", g.Repo,
		"--body", body,
	)
	return err
}

// api runs one read against the GitHub API.
func (g *CLIGitHub) api(ctx context.Context, path string, extra ...string) (string, error) {
	args := append([]string{"api", path}, extra...)
	return g.commands.run(ctx, g.env(), "", "gh", args...)
}

func (g *CLIGitHub) env() []string {
	if g.Home == "" {
		return nil
	}
	return []string{"HOME=" + g.Home}
}

// parseIssues reads what `gh api` printed: one JSON array of issues, or the array of
// pages `--paginate --slurp` makes.
func parseIssues(out string) ([]Issue, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("reading issues: %w", err)
	}
	var issues []Issue
	for _, entry := range raw {
		entry = bytes.TrimSpace(entry)
		if len(entry) == 0 {
			continue
		}
		if entry[0] == '[' {
			var page []Issue
			if err := json.Unmarshal(entry, &page); err != nil {
				return nil, fmt.Errorf("reading a page of issues: %w", err)
			}
			issues = append(issues, page...)
			continue
		}
		var one Issue
		if err := json.Unmarshal(entry, &one); err != nil {
			return nil, fmt.Errorf("reading an issue: %w", err)
		}
		issues = append(issues, one)
	}
	return issues, nil
}
