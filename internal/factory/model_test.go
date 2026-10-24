package factory

import (
	"strings"
	"testing"
)

func tickets(numbers ...int) []Ticket {
	all := make([]Ticket, 0, len(numbers))
	for _, number := range numbers {
		all = append(all, Ticket{Issue: Issue{Number: number}})
	}
	return all
}

func numbersOf(all []Ticket) []int {
	found := make([]int, 0, len(all))
	for _, ticket := range all {
		found = append(found, ticket.Issue.Number)
	}
	return found
}

// The order is the whole point of reading blocked_by: a ticket whose blockers have not
// been merged cannot be built, and a ticket with no blockers must not wait for the
// slowest one.
func TestOrderPutsBlockersFirst(t *testing.T) {
	all := tickets(5, 9, 3)
	all[1].Blockers = []int{3}

	ordered, err := order(all)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	want := []int{3, 5, 9}
	if got := numbersOf(ordered); !equalInts(got, want) {
		t.Errorf("order = %v; want %v", got, want)
	}
}

// Ties go to the lower number, so two runs of the same spec create the same cards in
// the same order.
func TestOrderBreaksTiesByLowerNumber(t *testing.T) {
	ordered, err := order(tickets(9, 2, 11))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	want := []int{2, 9, 11}
	if got := numbersOf(ordered); !equalInts(got, want) {
		t.Errorf("order = %v; want %v", got, want)
	}
}

func TestOrderKeepsAChain(t *testing.T) {
	all := tickets(1, 2, 3, 4)
	all[1].Blockers = []int{1}
	all[2].Blockers = []int{2}
	all[3].Blockers = []int{3}

	ordered, err := order(all)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	want := []int{1, 2, 3, 4}
	if got := numbersOf(ordered); !equalInts(got, want) {
		t.Errorf("order = %v; want %v", got, want)
	}
}

func TestOrderRefusesACycle(t *testing.T) {
	all := tickets(1, 2)
	all[0].Blockers = []int{2}
	all[1].Blockers = []int{1}

	_, err := order(all)
	if err == nil {
		t.Fatal("order: no error, want a cycle to be refused")
	}
	for _, number := range []string{"#1", "#2"} {
		if !strings.Contains(err.Error(), number) {
			t.Errorf("error %q does not name %s", err, number)
		}
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q does not say what is wrong", err)
	}
}

func TestOrderRefusesABlockerThatIsNotATicket(t *testing.T) {
	all := tickets(7)
	all[0].Blockers = []int{42}

	_, err := order(all)
	if err == nil {
		t.Fatal("order: no error, want a blocker outside the run to be refused")
	}
	if !strings.Contains(err.Error(), "#42") || !strings.Contains(err.Error(), "not a ticket") {
		t.Errorf("error %q does not say that #42 is not a ticket", err)
	}
}

func TestIssueOfReadsTheDirectoryName(t *testing.T) {
	for _, test := range []struct {
		name  string
		issue int
		ok    bool
	}{
		{name: "56", issue: 56, ok: true},
		{name: "56-3", issue: 56, ok: true},
		{name: "56-3-4", issue: 56, ok: true},
		{name: "563", issue: 563, ok: true},
		{name: "notes", ok: false},
		{name: "56a", ok: false},
		{name: "", ok: false},
	} {
		issue, ok := issueOf(test.name)
		if ok != test.ok || issue != test.issue {
			t.Errorf("issueOf(%q) = %d, %v; want %d, %v", test.name, issue, ok, test.issue, test.ok)
		}
	}
}

func TestOpenIssuesKeepsOnlyOpen(t *testing.T) {
	open := openIssues([]Issue{
		{Number: 1, State: "open"},
		{Number: 2, State: "closed"},
		{Number: 3, State: "open"},
	})
	if len(open) != 2 || open[0].Number != 1 || open[1].Number != 3 {
		t.Errorf("openIssues = %v; want #1 and #3", open)
	}
}

func TestNamesOfOneRun(t *testing.T) {
	if got := branch(56); got != "factory/56" {
		t.Errorf("branch(56) = %q", got)
	}
	if got := ticketBranch(56, 57); got != "factory/56-57" {
		t.Errorf("ticketBranch(56, 57) = %q", got)
	}
	if got := runWorktree("/trees", 56); got != "/trees/56" {
		t.Errorf("runWorktree = %q", got)
	}
	if got := ticketWorktree("/trees", 56, 57); got != "/trees/56-57" {
		t.Errorf("ticketWorktree = %q", got)
	}
	if got := idempotencyKey("laiacaliente-app", 56, "code:57"); got != "laiacaliente-app#56:code:57" {
		t.Errorf("idempotencyKey = %q", got)
	}
}

func equalInts(got, want []int) bool {
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

// A port is free when neither it nor its PocketBase port (port+1000) is taken, and the
// Hermes dashboard on 9119 takes 8119 that way.
func TestPreviewPortsStepOverWhatIsTaken(t *testing.T) {
	taken := takenPorts(nil)
	for port := 8100; port <= 8118; port++ {
		taken[port] = true
	}
	run := PlannedRun{Spec: Issue{Number: 20}, Tickets: []Ticket{{Issue: Issue{Number: 21}}}}
	ports, err := previewPorts(run, taken)
	if err != nil {
		t.Fatal(err)
	}
	if ports[21] != 8120 || ports[20] != 8121 {
		t.Errorf("ports = %v; want #21 on 8120 (8119 is the dashboard's PocketBase twin) and #20 on 8121", ports)
	}
}

// When every port is taken the run stops, rather than giving two cards the same one.
func TestPreviewPortsRunOut(t *testing.T) {
	taken := map[int]bool{}
	for port := firstPreviewPort; port <= lastPreviewPort; port++ {
		taken[port] = true
	}
	run := PlannedRun{Spec: Issue{Number: 7}, Lone: true, Tickets: []Ticket{{Issue: Issue{Number: 7}}}}
	if _, err := previewPorts(run, taken); err == nil {
		t.Error("previewPorts handed out a port when none was free")
	}
}

func TestRepoOfEveryRemoteShape(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/torresposso/laiacaliente-app.git",
		"https://github.com/torresposso/laiacaliente-app",
		"https://x-access-token:secret@github.com/torresposso/laiacaliente-app.git",
		"git@github.com:torresposso/laiacaliente-app.git",
		"ssh://git@github.com/torresposso/laiacaliente-app.git",
	} {
		if got, err := repoOf(remote); err != nil || got != "torresposso/laiacaliente-app" {
			t.Errorf("repoOf(%q) = %q, %v", remote, got, err)
		}
	}
	for _, remote := range []string{"", "/srv/git/repo.git", "https://github.com/torresposso"} {
		if got, err := repoOf(remote); err == nil {
			t.Errorf("repoOf(%q) = %q; want an error", remote, got)
		}
	}
}

func TestBoardOfIsASlug(t *testing.T) {
	for repo, want := range map[string]string{
		"torresposso/laiacaliente-app": "laiacaliente-app",
		"torresposso/torresposso.site": "torresposso-site",
		"torresposso/Factory_Sandbox":  "factory-sandbox",
	} {
		if got := boardOf(repo); got != want {
			t.Errorf("boardOf(%q) = %q; want %q", repo, got, want)
		}
	}
}
