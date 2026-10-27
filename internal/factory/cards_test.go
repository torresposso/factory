package factory

import (
	"strings"
	"testing"
)

// The cards carry the rules, and the rules are files this program embeds. A rules file
// that interpolates a field nobody sets renders an empty hole, so every one of them is
// rendered here with nothing left over.
func TestEveryRulesFileRenders(t *testing.T) {
	entries, err := templates.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading the embedded rules: %v", err)
	}
	want := map[string]bool{
		"coder-rules-pig.txt":      false,
		"coder-rules-hermes.txt":   false,
		"coder-rules-opencode.txt": false,
		"integration-rules.txt":    false,
		"review-rules.txt":         false,
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok {
			t.Errorf("templates/%s is not a card the factory knows", entry.Name())
			continue
		}
		want[entry.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("templates/%s is missing: the card that asks for it would panic", name)
		}
	}

	data := rulesData{Repo: "torresposso/laiacaliente-app", Issue: 20, Ticket: 21, Branch: "factory/20-21"}
	for name := range want {
		rendered := rules(name, data)
		if strings.Contains(rendered, "{{") {
			t.Errorf("%s kept a placeholder: %s", name, rendered)
		}
		if strings.Contains(rendered, "<no value>") {
			t.Errorf("%s interpolated a field the factory never sets: %s", name, rendered)
		}
		if !strings.HasPrefix(rendered, "## ") {
			t.Errorf("%s does not start with its heading: %s", name, rendered)
		}
		if strings.TrimSpace(rendered) == "" {
			t.Errorf("%s is empty", name)
		}
	}
}

// The coder card is everything the coder cannot find out on its own: the spec as
// context, its ticket verbatim, where its work happens, and the rules.
func TestCoderCardBody(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	// The rules are wrapped for people to read: compare them with the line breaks folded.
	body := strings.Join(strings.Fields(cardByKey(t, plan, "code:22").Body(nil)), " ")

	for _, want := range []string{
		"## Spec #20 (context only): Portfolio",
		"The body of issue Portfolio.",
		"## Ticket #22: the form",
		"The body of issue the form.",
		"## Your run",
		"Branch: factory/20-22",
		"Preview port: 8101",
		"Merge first: factory/20-21",
		"## Coder rules",
		"You do not write the code",
		"tmp/pig-22.md",
		"pig -p --session-dir tmp/pig-sessions",
		"/opt/opencode-skills/engineering/tdd/SKILL.md",
		"background=true",
		"chrome-devtools-axi run < tmp/look.js",
		"console.log(await page.eval(",
		"tmp/evidence/22-what.png",
		"[ -s tmp/pig-22.exit ]",
		"Plan your commands before you run the first one",
		"A review card checks the whole branch after you",
		"call kanban_block with kind \"needs_input\" and its question",
		"Refs #22",
		"make verify",
		"make preview PORT=<your port> DIR=tmp/preview-22",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the coder card does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Refs #20") {
		t.Errorf("the coder card tells the coder to close the spec instead of its ticket:\n%s", body)
	}
}

// The same tickets can be planned for another worker: only the part of the rules that
// says who writes the code changes, and an unknown worker stops the plan before it
// touches anything.
func TestCoderCardFollowsTheWorker(t *testing.T) {
	for worker, want := range map[string]string{
		WorkerPig:      "pig -p --session-dir",
		WorkerHermes:   "You write the code yourself",
		WorkerOpenCode: "opencode run --standalone --title \"ticket 21\"",
	} {
		h := newHarness(t)
		h.spec(20, "Portfolio", []Issue{open(21, "the list")}, map[int][]int{21: nil})
		h.factory.Opts.Worker = worker
		body := cardByKey(t, h.plan(t, 20), "code:21").Body(nil)
		if !strings.Contains(body, want) {
			t.Errorf("worker %s: the coder card does not contain %q:\n%s", worker, want, body)
		}
		for _, other := range []string{"pig -p", "opencode run --standalone --title"} {
			if !strings.Contains(want, other) && strings.Contains(body, other) {
				t.Errorf("worker %s: the coder card also names %q:\n%s", worker, other, body)
			}
		}
	}

	h := newHarness(t)
	h.spec(20, "Portfolio", []Issue{open(21, "the list")}, map[int][]int{21: nil})
	h.factory.Opts.Worker = "gpt"
	if err := h.refused(t, 20); !strings.Contains(err.Error(), "unknown worker") {
		t.Errorf("an unknown worker is refused with %v", err)
	}
}

// A ticket with no blockers has nothing to merge first, and the line must be absent:
// a coder that reads `Merge first:` and finds nothing would wonder what it missed.
func TestCoderCardWithoutBlockersOrReview(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	body := cardByKey(t, plan, "code:21").Body(nil)
	run := yourRun(body)

	if strings.Contains(run, "Merge first") {
		t.Errorf("a ticket with no blockers says to merge something:\n%s", run)
	}
	if strings.Contains(body, "## Spec") == false {
		t.Errorf("a ticket of a spec has no spec section:\n%s", body)
	}
}

// A lone ticket has no spec above it, so the card must not pretend otherwise; a review
// card still follows it, so the coder does not review its own work.
func TestLoneTicketCardBody(t *testing.T) {
	h := newHarness(t)
	h.lone(7, "The mobile menu")
	plan := h.plan(t, 7)

	body := cardByKey(t, plan, "code:7").Body(nil)
	run := yourRun(body)

	for _, want := range []string{
		"## Ticket #7: The mobile menu",
		"Branch: factory/7",
		"Preview port: 8100",
		"## Coder rules",
		"You do not write the code",
		"A review card checks the whole branch after you",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the lone ticket's card does not contain %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"Merge first"} {
		if strings.Contains(run, unwanted) {
			t.Errorf("the lone ticket's card says %q, and there is no such thing:\n%s", unwanted, run)
		}
	}
	if strings.Contains(body, "## Spec") {
		t.Errorf("the lone ticket's card has a spec section, and there is no spec:\n%s", body)
	}
}

// yourRun is the "Your run" block of a card body: the lines about this card, and not
// the rules below them, which name the same words when they explain them.
func yourRun(body string) string {
	_, rest, ok := strings.Cut(body, "## Your run")
	if !ok {
		return ""
	}
	next := strings.Index(rest, "\n## ")
	if next < 0 {
		return rest
	}
	return rest[:next]
}

// The integration card names the ticket cards by the board's ids, which is how the
// integrator reads a ticket's summary with kanban_show when a merge conflicts.
func TestIntegrationCardBody(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	body := cardByKey(t, plan, "integrate").Body(map[string]string{"code:21": "t_21", "code:22": "t_22"})

	for _, want := range []string{
		"## Spec #20: Portfolio",
		"The body of issue Portfolio.",
		"#21 the list | branch factory/20-21 | worktree /trees/20-21 | card t_21",
		"#22 the form | branch factory/20-22 | worktree /trees/20-22 | card t_22",
		"Branch: factory/20",
		"Preview port: 8102",
		"## Integration rules",
		"make verify",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the integration card does not contain %q:\n%s", want, body)
		}
	}
}

// The reviewer has no GitHub access: the review card is the only place it can read the
// tickets, so every ticket is in it, verbatim.
func TestReviewCardBody(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	body := cardByKey(t, plan, "review").Body(nil)

	for _, want := range []string{
		"## Spec #20: Portfolio",
		"## Ticket #21: the list",
		"## Ticket #22: the form",
		"Branch: factory/20",
		"Preview port: 8102",
		"## Review rules",
		"code-review skill",
		"read tmp/evidence/review-what.png",
		"console.log(await page.eval(",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the review card does not contain %q:\n%s", want, body)
		}
	}
}

// The bodies are concatenated text, with the section headings Markdown reads and one
// blank line between the parts: a card body with runs of blank lines is a card that is
// painful to read in the terminal.
func TestCardBodiesHaveNoRunsOfBlankLines(t *testing.T) {
	h := newHarness(t)
	h.spec(20, "Portfolio",
		[]Issue{open(21, "the list"), open(22, "the form")},
		map[int][]int{21: nil, 22: {21}},
	)
	plan := h.plan(t, 20)

	for _, card := range cardsOf(plan) {
		body := card.Body(map[string]string{"code:21": "t_21", "code:22": "t_22"})
		if strings.Contains(body, "\n\n\n") {
			t.Errorf("the card %s has a run of blank lines:\n%s", card.Key, body)
		}
		if !strings.HasSuffix(body, "\n") {
			t.Errorf("the card %s does not end with a newline", card.Key)
		}
	}
}

// The rules are the factory's, and the factory serves every repository on the stack: they
// name the Makefile contract (verify, preview, preview-stop) and nothing one repository has.
func TestRulesNameOnlyTheMakefileContract(t *testing.T) {
	data := rulesData{Repo: "torresposso/laiacaliente-app", Issue: 20, Ticket: 21, Branch: "factory/20-21"}
	for _, name := range []string{"coder-rules-pig.txt", "coder-rules-hermes.txt", "coder-rules-opencode.txt", "integration-rules.txt", "review-rules.txt"} {
		rendered := rules(name, data)
		for _, unwanted := range []string{"make css", "app.css", "pocketbase-bin", "internal/web", "PocketBase"} {
			if strings.Contains(rendered, unwanted) {
				t.Errorf("%s names %q, which is one repository's", name, unwanted)
			}
		}
	}
}

// A card runs with nobody to answer an approval prompt: every rules file keeps the worker off the
// writes Hermes asks a human about, so it never spends a turn on one.
func TestRulesKeepTheWorkerOffApprovals(t *testing.T) {
	data := rulesData{Repo: "torresposso/laiacaliente-app", Issue: 20, Ticket: 21, Branch: "factory/20-21"}
	for _, name := range []string{"coder-rules-pig.txt", "coder-rules-hermes.txt", "coder-rules-opencode.txt", "integration-rules.txt", "review-rules.txt"} {
		rendered := rules(name, data)
		for _, wanted := range []string{"agent-instruction file", "`bash -c`", "heredoc"} {
			if !strings.Contains(rendered, wanted) {
				t.Errorf("%s does not say %q", name, wanted)
			}
		}
	}
}
