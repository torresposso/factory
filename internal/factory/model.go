package factory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Issue is one GitHub issue: the spec, or one of its tickets.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

// Ticket is one issue the run implements, with the open issues that block it. A
// blocker that is not one of the run's tickets has no order and is refused.
type Ticket struct {
	Issue    Issue
	Blockers []int
}

// PlannedRun is one issue as the factory reads it.
type PlannedRun struct {
	// Spec is the issue the run was asked for.
	Spec Issue
	// Lone is true when the issue has no open sub-issues, so it is its own ticket and
	// there is no spec above it.
	Lone bool
	// Tickets are the run's tickets, in the order their cards are created: every
	// ticket after the tickets that block it, ties by lower number.
	Tickets []Ticket
	// Ports are the preview ports, by issue number: one per ticket, and one for the
	// spec, which the integration and the review share. A lone ticket is its own spec,
	// so its coder and its review share one.
	Ports map[int]int
}

// single is true when the run has one ticket: one coder card on the run's own branch
// and no integration; the review card follows the coder card directly.
func (r PlannedRun) single() bool { return len(r.Tickets) == 1 }

// branch is the branch the run's pull request comes from, and the branch where the
// ticket branches meet.
func branch(issue int) string { return fmt.Sprintf("factory/%d", issue) }

// ticketBranch is the branch one ticket is implemented on.
func ticketBranch(issue, ticket int) string { return fmt.Sprintf("factory/%d-%d", issue, ticket) }

// runWorktree is where the ticket branches meet.
func runWorktree(trees string, issue int) string {
	return filepath.Join(trees, strconv.Itoa(issue))
}

// ticketWorktree is where one ticket is implemented.
func ticketWorktree(trees string, issue, ticket int) string {
	return filepath.Join(trees, fmt.Sprintf("%d-%d", issue, ticket))
}

// The preview ports. A card serves the site on its port and PocketBase on port+1000, so
// a port is free only when neither of the two is taken. The cards of every board on the
// machine count, because every repository's cards preview on the same host.
const (
	firstPreviewPort = 8100
	lastPreviewPort  = 8999
	pocketBaseOffset = 1000
)

// reservedPorts are ports something other than a card listens on: the preview a person
// serves by hand (8191, and 9191 beside it), the Hermes API and webhooks, and the dashboard.
var reservedPorts = map[int]bool{8191: true, 9191: true, 8642: true, 8644: true, 9119: true}

// previewPortLine is how a card body names its preview port.
var previewPortLine = regexp.MustCompile(`(?m)^Preview port: (\d+)$`)

// takenPorts are the ports the live cards preview on, with their PocketBase ports.
func takenPorts(cards []BoardCard) map[int]bool {
	taken := map[int]bool{}
	for port := range reservedPorts {
		taken[port] = true
	}
	for _, card := range cards {
		if card.Status == statusDone {
			continue
		}
		for _, match := range previewPortLine.FindAllStringSubmatch(card.Body, -1) {
			port, err := strconv.Atoi(match[1])
			if err != nil {
				continue
			}
			taken[port] = true
			taken[port+pocketBaseOffset] = true
		}
	}
	return taken
}

// previewPorts gives every ticket of the run, and then the spec, the first free port
// from 8100. It marks what it hands out, so no two cards of the run share a port they
// could both be serving at once.
func previewPorts(run PlannedRun, taken map[int]bool) (map[int]int, error) {
	numbers := make([]int, 0, len(run.Tickets)+1)
	for _, ticket := range run.Tickets {
		numbers = append(numbers, ticket.Issue.Number)
	}
	numbers = append(numbers, run.Spec.Number)

	ports := map[int]int{}
	next := firstPreviewPort
	for _, number := range numbers {
		if _, ok := ports[number]; ok {
			continue
		}
		for next <= lastPreviewPort && (taken[next] || taken[next+pocketBaseOffset]) {
			next++
		}
		if next > lastPreviewPort {
			return nil, fmt.Errorf("no preview port is free between %d and %d", firstPreviewPort, lastPreviewPort)
		}
		ports[number] = next
		taken[next] = true
		taken[next+pocketBaseOffset] = true
		next++
	}
	return ports, nil
}

// cardKey names a card inside a run, so a later card can point at it before the board
// has given it an id.
func cardKey(ticket int) string { return fmt.Sprintf("code:%d", ticket) }

// idempotencyKey is the key the board dedups on. It carries the board, so a card can
// never be mistaken for one of another repository's run.
func idempotencyKey(board string, issue int, kind string) string {
	return fmt.Sprintf("%s#%d:%s", board, issue, kind)
}

// order puts every ticket after the tickets that block it; ties go to the lower
// number. A blocker that is not one of the tickets, or blockers that form a cycle, is
// an error: neither has an order, and guessing one would be worse than stopping.
func order(tickets []Ticket) ([]Ticket, error) {
	byNumber := make(map[int]Ticket, len(tickets))
	for _, ticket := range tickets {
		byNumber[ticket.Issue.Number] = ticket
	}
	for _, ticket := range tickets {
		for _, blocker := range ticket.Blockers {
			if _, ok := byNumber[blocker]; !ok {
				return nil, fmt.Errorf("ticket #%d is blocked by #%d, which is not a ticket of this run",
					ticket.Issue.Number, blocker)
			}
		}
	}

	// Every ticket starts with the number of blockers it waits for, and each blocker
	// remembers who waits on it.
	waiting := make(map[int]int, len(tickets))
	releases := make(map[int][]int, len(tickets))
	var ready []int
	for _, ticket := range tickets {
		number := ticket.Issue.Number
		waiting[number] = len(ticket.Blockers)
		for _, blocker := range ticket.Blockers {
			releases[blocker] = append(releases[blocker], number)
		}
		if waiting[number] == 0 {
			ready = append(ready, number)
		}
	}
	sort.Ints(ready)

	ordered := make([]Ticket, 0, len(tickets))
	for len(ready) > 0 {
		number := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byNumber[number])
		for _, freed := range releases[number] {
			waiting[freed]--
			if waiting[freed] == 0 {
				ready = append(ready, freed)
			}
		}
		sort.Ints(ready)
	}
	if len(ordered) != len(tickets) {
		var stuck []int
		for number, left := range waiting {
			if left > 0 {
				stuck = append(stuck, number)
			}
		}
		sort.Ints(stuck)
		listed := make([]string, 0, len(stuck))
		for _, number := range stuck {
			listed = append(listed, fmt.Sprintf("#%d", number))
		}
		return nil, fmt.Errorf("the blockers of %s form a cycle", strings.Join(listed, ", "))
	}
	return ordered, nil
}

// openIssues keeps the issues GitHub still has open. A closed sub-issue is not work,
// and a closed blocker does not block.
func openIssues(issues []Issue) []Issue {
	var open []Issue
	for _, issue := range issues {
		if issue.State == "open" {
			open = append(open, issue)
		}
	}
	return open
}

// issueOf reads the issue a worktree belongs to from its directory name: "56" and
// "56-3" both belong to issue 56. Names that are not numbered belong to no run.
func issueOf(name string) (int, bool) {
	head, _, _ := strings.Cut(name, "-")
	number, err := strconv.Atoi(head)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

// isRunWorkspace reports whether a card's workspace belongs to this run. A run's
// workspaces are its own directory and, with several tickets, one per ticket: the
// directory `56` and every `56-T`. Another run's `563` is not one of them.
func (f *Factory) isRunWorkspace(issue int, path string) bool {
	dir := runWorktree(f.Opts.Trees, issue)
	return path == dir || strings.HasPrefix(path, dir+"-")
}
