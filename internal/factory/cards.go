package factory

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// The cards carry the rules, and the rules are these files, verbatim: the factory
// assembles text, and no model is asked to write any of it. Editing a rule is editing
// a file this program embeds.
//
//go:embed templates
var templates embed.FS

// The profiles the cards are assigned to, and how long a card may run.
const (
	assigneeCoder = "coder"
	assigneeQA    = "qa"

	runtimeCoder = 2700
)

// rulesData is what the embedded rules interpolate.
type rulesData struct {
	// Repo is owner/name on GitHub.
	Repo string
	// Issue is the issue the run belongs to.
	Issue int
	// Ticket is the ticket the card implements; only the coder rules use it.
	Ticket int
	// Branch is the branch the card works on.
	Branch string
}

// rules renders one embedded rules file.
func rules(name string, data rulesData) string {
	raw, err := templates.ReadFile("templates/" + name)
	if err != nil {
		panic(err)
	}
	parsed, err := template.New(name).Parse(string(raw))
	if err != nil {
		panic(err)
	}
	var out strings.Builder
	if err := parsed.Execute(&out, data); err != nil {
		panic(err)
	}
	return out.String()
}

// coderBody is one ticket card: the spec as context, the ticket verbatim, where the
// work happens, and the coder rules.
func (f *Factory) coderBody(run PlannedRun, ticket Ticket, branch string) string {
	issue := run.Spec.Number
	parts := []string{}
	if !run.Lone {
		parts = append(parts,
			section(fmt.Sprintf("Spec #%d (context only): %s", run.Spec.Number, run.Spec.Title), run.Spec.Body))
	}
	parts = append(parts,
		section(fmt.Sprintf("Ticket #%d: %s", ticket.Issue.Number, ticket.Issue.Title), ticket.Issue.Body))

	lines := []string{
		"Branch: " + branch,
		fmt.Sprintf("Preview port: %d", run.Ports[ticket.Issue.Number]),
	}
	if len(ticket.Blockers) > 0 {
		branches := make([]string, 0, len(ticket.Blockers))
		for _, blocker := range ticket.Blockers {
			branches = append(branches, ticketBranch(issue, blocker))
		}
		lines = append(lines, "Merge first: "+strings.Join(branches, ", "))
	}
	parts = append(parts, section("Your run", strings.Join(lines, "\n")))
	parts = append(parts, rules("coder-rules.txt", rulesData{
		Repo:   f.Opts.Repo,
		Issue:  issue,
		Ticket: ticket.Issue.Number,
		Branch: branch,
	}))
	return cardBody(parts...)
}

// integrationBody is the card where the ticket branches meet. It names the ticket
// cards by their board ids, which is what the coder reads with kanban_show when a
// merge conflicts.
func (f *Factory) integrationBody(run PlannedRun, ids map[string]string) string {
	issue := run.Spec.Number
	listed := make([]string, 0, len(run.Tickets))
	for _, ticket := range run.Tickets {
		listed = append(listed, fmt.Sprintf("#%d %s | branch %s | worktree %s | card %s",
			ticket.Issue.Number,
			ticket.Issue.Title,
			ticketBranch(issue, ticket.Issue.Number),
			ticketWorktree(f.Opts.Trees, issue, ticket.Issue.Number),
			cardRef(ids, cardKey(ticket.Issue.Number)),
		))
	}
	branch := branch(issue)
	return cardBody(
		section(fmt.Sprintf("Spec #%d: %s", issue, run.Spec.Title), run.Spec.Body),
		section("Your run", strings.Join(append(
			[]string{"Branch: " + branch},
			append(listed, fmt.Sprintf("Preview port: %d", run.Ports[issue]))...,
		), "\n")),
		rules("integration-rules.txt", rulesData{Repo: f.Opts.Repo, Issue: issue, Branch: branch}),
	)
}

// reviewBody is the review card: every ticket verbatim, because the reviewer has no
// GitHub access and cannot read them otherwise. A lone ticket is its own spec, so it is
// written once, as the ticket.
func (f *Factory) reviewBody(run PlannedRun) string {
	issue := run.Spec.Number
	parts := []string{}
	if !run.Lone {
		parts = append(parts, section(fmt.Sprintf("Spec #%d: %s", issue, run.Spec.Title), run.Spec.Body))
	}
	for _, ticket := range run.Tickets {
		parts = append(parts, section(fmt.Sprintf("Ticket #%d: %s", ticket.Issue.Number, ticket.Issue.Title), ticket.Issue.Body))
	}
	branch := branch(issue)
	parts = append(parts,
		section("Your run", fmt.Sprintf("Branch: %s\nPreview port: %d", branch, run.Ports[issue])),
		rules("review-rules.txt", rulesData{Repo: f.Opts.Repo, Issue: issue, Branch: branch}),
	)
	return cardBody(parts...)
}

// section is one `## Heading` block of a card body.
func section(heading, text string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "## " + heading
	}
	return "## " + heading + "\n\n" + text
}

// cardBody joins the parts of a card with a blank line between them, the way the cards
// read on the board.
func cardBody(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimRight(part, "\n"); part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n\n") + "\n"
}
