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

// HookOptions reads where the hook's card runs. The board is the hook's, and the rest
// comes from the card's workspace, which is a worktree of the repository: the claimed
// hook runs in the gateway, far from any checkout.
func HookOptions(ctx context.Context, hook Hook) (Options, error) {
	return hookOptions(ctx, shell{}, hook)
}

func hookOptions(ctx context.Context, commands runner, hook Hook) (Options, error) {
	board := &CLIBoard{Board: hook.Board, commands: commands}
	detail, err := board.Show(ctx, hook.Card)
	if err != nil {
		return Options{}, err
	}
	workspace := detail.Card.WorkspacePath
	if workspace == "" {
		return Options{}, fmt.Errorf("%w: %s has no workspace", ErrNotARunCard, hook.Card)
	}
	opts, err := detectOptions(ctx, commands, workspace)
	if err != nil {
		return Options{}, fmt.Errorf("%w: the workspace of %s, %s, is not a checkout: %v", ErrNotARunCard, hook.Card, workspace, err)
	}
	opts.Board = hook.Board
	return opts, nil
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
		return cardOfRun{Card: card, Run: run, Issue: ticket}, nil
	}
	if match := runTitle.FindStringSubmatch(card.Title); match != nil && match[2] == strconv.Itoa(run) {
		return cardOfRun{Card: card, Run: run, Issue: run, Review: match[1] == "Review"}, nil
	}
	return cardOfRun{}, fmt.Errorf("%w: %s (%s) is not a title the plan writes", ErrNotARunCard, id, card.Title)
}

// Note is the comment a hook leaves on the card's issue: that the card started, why it
// stopped, or what it did. The wording is the program's; what the worker reported is
// quoted, never rewritten.
func (f *Factory) Note(ctx context.Context, hook Hook) (*Plan, error) {
	card, err := f.readRunCard(ctx, hook.Card)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("`%s` (%s)", card.Card.ID, card.Card.Title)
	var body string
	switch hook.Event {
	case hookClaimed:
		body = fmt.Sprintf("The factory is working on this: card %s started on board `%s`, run %d.", name, hook.Board, hook.Run)
	case hookBlocked:
		body = fmt.Sprintf("The factory's card %s is blocked and waits for an answer on board `%s`.", name, hook.Board) +
			quoted(hook.Reason, "Its worker gave no reason.")
	case hookCompleted:
		body = fmt.Sprintf("The factory's card %s is done.", name) +
			quoted(hook.Summary, "Its worker reported no summary.")
	}
	return &Plan{Issue: card.Run, Steps: []Step{CommentStep{Issue: card.Issue, Body: body}}}, nil
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
