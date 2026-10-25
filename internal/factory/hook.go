package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// The kanban events the hooks are configured on (docs/adr/0002). Claimed fires in the
// gateway, from the root profile; blocked and completed fire for the card's assignee,
// in the `hermes` container, with the card's workspace as the working directory.
const (
	hookClaimed   = "kanban_task_claimed"
	hookBlocked   = "kanban_task_blocked"
	hookCompleted = "kanban_task_completed"
)

// The Status a card's issue takes on its projects when the card starts, and when it
// stops for an answer. A project without the option is left alone; the rest of the
// way, In review and Done, is the project's own workflows, moved by the pull request.
const (
	projectInProgress = "In progress"
	projectBlocked    = "Blocked"
)

// The labels an issue carries through a run. factory stays, so a search finds every
// issue the factory worked on; the other three follow the run, one at a time, and
// say on the issue what its project's Status says. ready-for-agent is triage's, and
// goes once an agent has the issue.
const (
	labelFactory      = "factory"
	labelInProgress   = "in-progress"
	labelBlocked      = "blocked"
	labelInReview     = "in-review"
	labelReadyToAgent = "ready-for-agent"
)

// Hook is what Hermes hands a shell hook on stdin when a card changes.
type Hook struct {
	Event string
	// Card is the card's board id, and Board its board's slug.
	Card  string
	Board string
	// Run is the board's id for the worker's run of the card.
	Run int
	// Summary is what the worker said it did (completed), and Reason why it stopped
	// (blocked). Both are the worker's words, never the program's.
	Summary string
	Reason  string
}

// ErrNotARunCard is a hook for a card the factory did not plan. The root profile's
// claimed hook fires for every card on every board, so this is ordinary, not a failure.
var ErrNotARunCard = errors.New("the card is not a card of a factory run")

// ReadHook reads a hook's payload.
func ReadHook(in io.Reader) (Hook, error) {
	var raw struct {
		Event string `json:"hook_event_name"`
		Extra struct {
			TaskID  string `json:"task_id"`
			Board   string `json:"board"`
			RunID   int    `json:"run_id"`
			Summary string `json:"summary"`
			Reason  string `json:"reason"`
		} `json:"extra"`
	}
	if err := json.NewDecoder(in).Decode(&raw); err != nil {
		return Hook{}, fmt.Errorf("reading the hook's payload on stdin: %w", err)
	}
	switch raw.Event {
	case hookClaimed, hookBlocked, hookCompleted:
	default:
		return Hook{}, fmt.Errorf("the hook is for %q; the factory knows %s, %s and %s",
			raw.Event, hookClaimed, hookBlocked, hookCompleted)
	}
	if raw.Extra.TaskID == "" || raw.Extra.Board == "" {
		return Hook{}, fmt.Errorf("the %s payload names no card or no board", raw.Event)
	}
	return Hook{
		Event:   raw.Event,
		Card:    raw.Extra.TaskID,
		Board:   raw.Extra.Board,
		Run:     raw.Extra.RunID,
		Summary: raw.Extra.Summary,
		Reason:  raw.Extra.Reason,
	}, nil
}

// HookOptions reads where the hook's card runs. The board is the one the card is on, and
// the rest comes from the card's workspace, which is a worktree of the repository: the
// claimed hook runs in the gateway, far from any checkout.
//
// The claimed payload names the gateway's current board, not the card's (Hermes 0.22.5:
// the dispatcher claims on every board without switching), so the card is looked for on
// the payload's board first and then on every other; hook.Board is set to where it is.
func HookOptions(ctx context.Context, hook *Hook) (Options, error) {
	return hookOptions(ctx, shell{}, hook)
}

func hookOptions(ctx context.Context, commands runner, hook *Hook) (Options, error) {
	detail, board, err := findCard(ctx, commands, *hook)
	if err != nil {
		return Options{}, err
	}
	hook.Board = board
	workspace := detail.Card.WorkspacePath
	if workspace == "" {
		return Options{}, fmt.Errorf("%w: %s has no workspace", ErrNotARunCard, hook.Card)
	}
	opts, err := detectOptions(ctx, commands, workspace)
	if err != nil {
		return Options{}, fmt.Errorf("%w: the workspace of %s, %s, is not a checkout: %v", ErrNotARunCard, hook.Card, workspace, err)
	}
	opts.Board = board
	return opts, nil
}

// findCard shows the hook's card on the board the payload names, or else on the first
// other board that has it.
func findCard(ctx context.Context, commands runner, hook Hook) (BoardCardDetail, string, error) {
	detail, first := (&CLIBoard{Board: hook.Board, commands: commands}).Show(ctx, hook.Card)
	if first == nil {
		return detail, hook.Board, nil
	}
	out, err := commands.run(ctx, nil, "", "hermes", "kanban", "boards", "list", "--json")
	if err != nil {
		return BoardCardDetail{}, "", errors.Join(first, err)
	}
	var boards []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(out), &boards); err != nil {
		return BoardCardDetail{}, "", fmt.Errorf("reading the boards: %w", err)
	}
	for _, board := range boards {
		if board.Slug == hook.Board {
			continue
		}
		if detail, err := (&CLIBoard{Board: board.Slug, commands: commands}).Show(ctx, hook.Card); err == nil {
			return detail, board.Slug, nil
		}
	}
	return BoardCardDetail{}, "", fmt.Errorf("card %s is on no board: %w", hook.Card, first)
}

// The titles the plan writes, read back: a ticket's card, and the two cards of the run.
var (
	ticketTitle = regexp.MustCompile(`^#(\d+): `)
	runTitle    = regexp.MustCompile(`^(Integrate|Review) #(\d+): `)
)

// cardOfRun is one of a run's cards, read back from the board.
type cardOfRun struct {
	Card BoardCard
	// Run is the run's issue, and Issue the one the card works for: its ticket, or the
	// spec for the integration and the review.
	Run, Issue int
	Review     bool
	Detail     BoardCardDetail
}

// readRunCard reads a card back as one of a run's. Its workspace says which run, and
// its title which issue; a card that fits neither is not one the plan wrote.
func (f *Factory) readRunCard(ctx context.Context, id string) (cardOfRun, error) {
	detail, err := f.Board.Show(ctx, id)
	if err != nil {
		return cardOfRun{}, err
	}
	card := detail.Card
	_, run, ok := f.runOfWorktree(Worktree{Path: card.WorkspacePath})
	if !ok {
		return cardOfRun{}, fmt.Errorf("%w: %s works in %s, outside %s", ErrNotARunCard, id, card.WorkspacePath, f.Opts.Trees)
	}
	if match := ticketTitle.FindStringSubmatch(card.Title); match != nil {
		ticket, _ := strconv.Atoi(match[1])
		return cardOfRun{Card: card, Run: run, Issue: ticket, Detail: detail}, nil
	}
	if match := runTitle.FindStringSubmatch(card.Title); match != nil && match[2] == strconv.Itoa(run) {
		return cardOfRun{Card: card, Run: run, Issue: run, Review: match[1] == "Review", Detail: detail}, nil
	}
	return cardOfRun{}, fmt.Errorf("%w: %s (%s) is not a title the plan writes", ErrNotARunCard, id, card.Title)
}

// Note is the comment a hook leaves on the card's issue: that the card started, why it
// stopped, or what it did. The wording is the program's; what the worker reported is
// quoted, never rewritten.
//
// A card that starts also marks its issue, and the spec with it: labelled factory and
// in-progress, assigned to the factory's login, In progress on its projects. One that
// stops is labelled blocked instead, assigned to the repository's owner, whose answer
// it waits for, and moved to Blocked. A card that stops or ends also carries what its
// worker wrote on the card during that run: Hermes calls no hook on a comment
// (0.22.5), so this is the first moment the issue can have it. The comment goes first, so an issue that refuses the rest still
// gets the word out.
func (f *Factory) Note(ctx context.Context, hook Hook) (*Plan, error) {
	card, err := f.readRunCard(ctx, hook.Card)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("`%s` (%s)", card.Card.ID, card.Card.Title)
	issues := []int{card.Issue}
	if card.Run != card.Issue {
		issues = append(issues, card.Run)
	}
	var body string
	var marks []Step
	switch hook.Event {
	case hookClaimed:
		body = fmt.Sprintf("The factory is working on this: card %s started on board `%s`, run %d.", name, hook.Board, hook.Run)
		login, err := f.GitHub.Login(ctx)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			marks = append(marks,
				LabelStep{Issue: issue, Add: []string{labelFactory, labelInProgress}, Remove: []string{labelReadyToAgent, labelBlocked}},
				AssignStep{Issue: issue, Logins: []string{login}},
				StatusStep{Issue: issue, Status: projectInProgress},
			)
		}
	case hookBlocked:
		body = fmt.Sprintf("The factory's card %s is blocked and waits for an answer on board `%s`.", name, hook.Board) +
			quoted(hook.Reason, "Its worker gave no reason.") + cardComments(card.Detail, hook.Run)
		marks = append(marks, LabelStep{Issue: card.Issue, Add: []string{labelBlocked}, Remove: []string{labelInProgress}})
		if owner := f.owner(); owner != "" {
			marks = append(marks, AssignStep{Issue: card.Issue, Logins: []string{owner}})
		}
		marks = append(marks, StatusStep{Issue: card.Issue, Status: projectBlocked})
	case hookCompleted:
		body = fmt.Sprintf("The factory's card %s is done.", name) +
			quoted(hook.Summary, "Its worker reported no summary.") + cardComments(card.Detail, hook.Run)
	}
	return &Plan{Issue: card.Run, Steps: append([]Step{CommentStep{Issue: card.Issue, Body: body}}, marks...)}, nil
}

// ReviewDone is the run a hook says is ready to publish: the run whose review card it
// reports done. Every other card and event is no run, and ok is false.
func (f *Factory) ReviewDone(ctx context.Context, hook Hook) (issue int, ok bool, err error) {
	if hook.Event != hookCompleted {
		return 0, false, nil
	}
	card, err := f.readRunCard(ctx, hook.Card)
	if errors.Is(err, ErrNotARunCard) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return card.Run, card.Review, nil
}

// FromReview is the board as a review card's hook may read it. Hermes keeps a worker's
// descendants off the board (Hermes 0.22.5), and `hermes kanban list` counts as a write,
// because it promotes ready cards before it lists, so the run's cards are read from the
// review card up through its parents with `show` instead.
func FromReview(board Board, review string) Board {
	return runGraph{Board: board, review: review}
}

type runGraph struct {
	Board
	review string
}

func (g runGraph) List(ctx context.Context) ([]BoardCard, error) {
	var cards []BoardCard
	seen := map[string]bool{}
	for queue := []string{g.review}; len(queue) > 0; queue = queue[1:] {
		if seen[queue[0]] {
			continue
		}
		seen[queue[0]] = true
		detail, err := g.Board.Show(ctx, queue[0])
		if err != nil {
			return nil, err
		}
		cards = append(cards, detail.Card)
		queue = append(queue, detail.Parents...)
	}
	return cards, nil
}

// PublishAndNote publishes the run and says on the spec how it went. The review card is
// already done when its hook runs, so this comment is the only place a failed publish
// shows; `factory publish --issue N` by hand is the retry.
func (f *Factory) PublishAndNote(ctx context.Context, issue int) error {
	plan, err := f.Publish(ctx, issue)
	if err == nil {
		err = f.Apply(ctx, plan)
	}
	body := "The factory published this run: " + f.URL
	if err != nil {
		body = fmt.Sprintf("The factory could not publish this run: %v\n\n"+
			"Run `factory publish --issue %d` in the run's worktree to try again.", err, issue)
	}
	if noted := f.Apply(ctx, &Plan{Issue: issue, Steps: []Step{CommentStep{Issue: issue, Body: body}}}); noted != nil {
		return errors.Join(err, noted)
	}
	return err
}

// quoted is a worker's words as a Markdown quote under the program's line, or what to
// say when it left none.
// maxCommentsLength keeps a note under GitHub's 65536 characters for a comment's body.
const maxCommentsLength = 60000

// cardComments are the comments written on the card since the run started, each quoted
// under its author. A run the card does not list counts from its first comment, and a
// card nobody commented on adds nothing.
func cardComments(detail BoardCardDetail, run int) string {
	var since int64
	for _, attempt := range detail.Runs {
		if attempt.ID == run {
			since = attempt.StartedAt
		}
	}
	var text string
	for _, c := range detail.Comments {
		if c.CreatedAt < since || strings.TrimSpace(c.Body) == "" {
			continue
		}
		text += fmt.Sprintf("\n\n**%s** wrote on the card:", c.Author) + quoted(c.Body, "")
	}
	if len(text) > maxCommentsLength {
		text = strings.ToValidUTF8(text[:maxCommentsLength], "") + "\n\n_The rest is on the card._"
	}
	return text
}

func quoted(text, none string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return " " + none
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight("> "+line, " ")
	}
	return "\n\n" + strings.Join(lines, "\n")
}
