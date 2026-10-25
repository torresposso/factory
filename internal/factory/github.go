package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// OpenPullRequest opens a pull request, asks its reviewers for a review, and returns
	// its address. A pull request that opened but whose review could not be asked for
	// still returns its address, beside the error.
	OpenPullRequest(ctx context.Context, pr NewPullRequest) (string, error)
	// Comment adds a comment to an issue: how the tickets say what the run is doing.
	Comment(ctx context.Context, number int, body string) error
	// SetStatus moves the issue to status on every project it is in whose Status field
	// has that option. An issue in no such project is left alone.
	SetStatus(ctx context.Context, number int, status string) error
	// Assign adds logins to the issue's assignees, keeping the ones it has.
	Assign(ctx context.Context, number int, logins []string) error
	// Label adds and removes labels on the issue, keeping the rest. A label the
	// repository does not have yet is made by GitHub; one the issue lacks is no
	// error to remove.
	Label(ctx context.Context, number int, add, remove []string) error
	// Login is the login the factory acts as: whose name its comments carry.
	Login(ctx context.Context) (string, error)
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
	// Reviewers are the logins asked for a review. The author is left out: GitHub
	// refuses to ask a pull request's author to review it.
	Reviewers []string
}

// CLIGitHub talks to GitHub through `gh api`, reads and writes alike: what it prints is
// the JSON this package parses, and what it sends is JSON on stdin, so a body with
// newlines and backticks arrives whole.
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

func (g *CLIGitHub) OpenPullRequest(ctx context.Context, pr NewPullRequest) (string, error) {
	out, err := g.send(ctx, "repos/"+g.Repo+"/pulls", map[string]string{
		"base":  pr.Base,
		"head":  pr.Head,
		"title": pr.Title,
		"body":  pr.Body,
	})
	if err != nil {
		return "", err
	}
	var opened struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(out), &opened); err != nil || opened.HTMLURL == "" {
		return "", fmt.Errorf("GitHub's answer to the new pull request names no address: %s", strings.TrimSpace(out))
	}
	var reviewers []string
	for _, login := range pr.Reviewers {
		if !strings.EqualFold(login, opened.User.Login) {
			reviewers = append(reviewers, login)
		}
	}
	if len(reviewers) > 0 {
		path := fmt.Sprintf("repos/%s/pulls/%d/requested_reviewers", g.Repo, opened.Number)
		if _, err := g.send(ctx, path, map[string][]string{"reviewers": reviewers}); err != nil {
			return opened.HTMLURL, fmt.Errorf("opened %s, but asking %s for a review failed: %w",
				opened.HTMLURL, strings.Join(reviewers, ", "), err)
		}
	}
	return opened.HTMLURL, nil
}

func (g *CLIGitHub) Comment(ctx context.Context, number int, body string) error {
	_, err := g.send(ctx, fmt.Sprintf("repos/%s/issues/%d/comments", g.Repo, number), map[string]string{"body": body})
	return err
}

func (g *CLIGitHub) Assign(ctx context.Context, number int, logins []string) error {
	_, err := g.send(ctx, fmt.Sprintf("repos/%s/issues/%d/assignees", g.Repo, number), map[string][]string{"assignees": logins})
	return err
}

// Label reads the issue's labels and puts the whole set back changed: GitHub answers a
// removal of a label the issue lacks with a 404, and one write says it all.
func (g *CLIGitHub) Label(ctx context.Context, number int, add, remove []string) error {
	path := fmt.Sprintf("repos/%s/issues/%d/labels", g.Repo, number)
	out, err := g.api(ctx, path, "--paginate", "--slurp")
	if err != nil {
		return err
	}
	var pages [][]struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return fmt.Errorf("reading the labels of #%d: %w", number, err)
	}
	var had []string
	for _, page := range pages {
		for _, label := range page {
			had = append(had, label.Name)
		}
	}
	labels := relabel(had, add, remove)
	if equalFoldSets(had, labels) {
		return nil
	}
	_, err = g.write(ctx, "PUT", path, map[string][]string{"labels": labels})
	return err
}

// relabel is had without remove and with add, in had's order and then add's. Names
// match without case, as GitHub's do.
func relabel(had, add, remove []string) []string {
	labels := []string{}
	keep := func(name string) {
		for _, label := range labels {
			if strings.EqualFold(label, name) {
				return
			}
		}
		labels = append(labels, name)
	}
	for _, name := range had {
		if !containsFold(remove, name) {
			keep(name)
		}
	}
	for _, name := range add {
		keep(name)
	}
	return labels
}

func containsFold(names []string, name string) bool {
	for _, one := range names {
		if strings.EqualFold(one, name) {
			return true
		}
	}
	return false
}

func equalFoldSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, name := range a {
		if !containsFold(b, name) {
			return false
		}
	}
	return true
}

func (g *CLIGitHub) Login(ctx context.Context) (string, error) {
	out, err := g.api(ctx, "user")
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(out), &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("GitHub's answer names no login: %s", strings.TrimSpace(out))
	}
	return user.Login, nil
}

// The issue's project items, each with its project's Status field, and the move of one.
const (
	projectItemsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      projectItems(first: 20, includeArchived: false) {
        nodes {
          id
          project {
            id
            field(name: "Status") { ... on ProjectV2SingleSelectField { id options { id name } } }
          }
        }
      }
    }
  }
}`
	setStatusMutation = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) {
    projectV2Item { id }
  }
}`
)

func (g *CLIGitHub) SetStatus(ctx context.Context, number int, status string) error {
	owner, name, ok := strings.Cut(g.Repo, "/")
	if !ok {
		return fmt.Errorf("repo %q is not owner/name", g.Repo)
	}
	out, err := g.graphQL(ctx, projectItemsQuery, map[string]any{"owner": owner, "name": name, "number": number})
	if err != nil {
		return err
	}
	var answer struct {
		Data struct {
			Repository struct {
				Issue struct {
					ProjectItems struct {
						Nodes []struct {
							ID      string `json:"id"`
							Project struct {
								ID    string `json:"id"`
								Field *struct {
									ID      string `json:"id"`
									Options []struct {
										ID   string `json:"id"`
										Name string `json:"name"`
									} `json:"options"`
								} `json:"field"`
							} `json:"project"`
						} `json:"nodes"`
					} `json:"projectItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		return fmt.Errorf("reading the projects of #%d: %w", number, err)
	}
	var errs []error
	for _, item := range answer.Data.Repository.Issue.ProjectItems.Nodes {
		field := item.Project.Field
		if field == nil {
			continue
		}
		for _, option := range field.Options {
			if !strings.EqualFold(option.Name, status) {
				continue
			}
			if _, err := g.graphQL(ctx, setStatusMutation, map[string]any{
				"project": item.Project.ID, "item": item.ID, "field": field.ID, "option": option.ID,
			}); err != nil {
				errs = append(errs, err)
			}
			break
		}
	}
	return errors.Join(errs...)
}

// api runs one read against the GitHub API.
func (g *CLIGitHub) api(ctx context.Context, path string, extra ...string) (string, error) {
	args := append([]string{"api", path}, extra...)
	return g.commands.run(ctx, g.env(), "", "gh", args...)
}

// send posts fields to the GitHub API as JSON on stdin.
func (g *CLIGitHub) send(ctx context.Context, path string, fields any) (string, error) {
	return g.write(ctx, "POST", path, fields)
}

// write sends fields to the GitHub API with method, as JSON on stdin.
func (g *CLIGitHub) write(ctx context.Context, method, path string, fields any) (string, error) {
	input, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return g.commands.run(ctx, g.env(), string(input), "gh", "api", "-X", method, path, "--input", "-")
}

// graphQL runs one query or mutation. `gh api graphql` exits non-zero when GitHub
// answers with errors, so a failed move is an error here too.
func (g *CLIGitHub) graphQL(ctx context.Context, query string, variables map[string]any) (string, error) {
	return g.send(ctx, "graphql", map[string]any{"query": query, "variables": variables})
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
