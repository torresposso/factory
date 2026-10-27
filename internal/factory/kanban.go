package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// The card statuses the factory reacts to. A board card in one of these is being
// worked on right now: no plan and no clean may touch the run it belongs to.
const (
	statusRunning = "running"
	statusReady   = "ready"
	statusDone    = "done"
	// A blocked card is a worker asking a person for something, and a card in review is
	// waiting on a reviewer: both hold work that archiving would throw away.
	statusBlocked = "blocked"
	statusReview  = "review"
)

// Board is the Hermes kanban board. The factory always names the board it means: the
// board the CLI considers current is a file shared by every profile on the machine,
// and it is not the run's.
type Board interface {
	// List reads every card of the board that is not archived.
	List(ctx context.Context) ([]BoardCard, error)
	// Everywhere reads every card that is not archived on every board of the machine.
	// The preview ports are the machine's, not one board's.
	Everywhere(ctx context.Context) ([]BoardCard, error)
	// Show reads one card with what only `show` reports: its parents and its last
	// summary.
	Show(ctx context.Context, id string) (BoardCardDetail, error)
	// Runs reads one card's runs: what its workers reported, including the metadata
	// with the verification, the branch and the commit.
	Runs(ctx context.Context, id string) ([]CardRun, error)
	// Create creates one card and returns it as the board made it. parents are board
	// ids, and they must exist: a child starts in `todo` until every parent is done.
	Create(ctx context.Context, card NewCard, parents []string) (BoardCard, error)
	// Archive archives cards, as a whole: archiving a parent releases its children.
	Archive(ctx context.Context, ids []string) error
}

// NewCard is a card the factory asks the board to create.
type NewCard struct {
	Title          string
	Body           string
	Assignee       string
	Skills         []string
	Workspace      string
	MaxRuntime     int
	IdempotencyKey string
	// GoalTurns above zero runs the card in goal mode, for at most that many turns.
	GoalTurns int
	// Model overrides the assignee profile's model for this card.
	Model string
}

// BoardCard is one card of the board, as far as the factory cares.
type BoardCard struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Body              string   `json:"body"`
	Assignee          string   `json:"assignee"`
	Status            string   `json:"status"`
	Skills            []string `json:"skills"`
	WorkspacePath     string   `json:"workspace_path"`
	MaxRuntimeSeconds int      `json:"max_runtime_seconds"`
}

// BoardCardDetail is a card with what `list` does not report.
type BoardCardDetail struct {
	Card BoardCard `json:"task"`
	// Parents are board ids.
	Parents []string `json:"parents"`
	// Summary is what the card's last worker said it did.
	Summary string `json:"latest_summary"`
	// Comments are what was written on the card, oldest first, and Runs its attempts.
	Comments []CardComment `json:"comments"`
	Runs     []CardRun     `json:"runs"`
}

// CardComment is one comment on a card: a worker's note on its progress or findings.
type CardComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// CardRun is one run of a card.
type CardRun struct {
	ID        int             `json:"id"`
	StartedAt int64           `json:"started_at"`
	Profile   string          `json:"profile"`
	Status    string          `json:"status"`
	Outcome   string          `json:"outcome"`
	Summary   string          `json:"summary"`
	Metadata  json.RawMessage `json:"metadata"`
}

// Keys are the metadata keys a card's worker is asked to report.
const (
	// MetadataVerification is the commands the worker ran, and their result.
	MetadataVerification = "verification"
	// MetadataBranch is the branch the work is on.
	MetadataBranch = "branch"
	// MetadataCommit is the commit that carries it.
	MetadataCommit = "commit"
)

// Fields reads a run's metadata as a map.
func (r CardRun) Fields() (map[string]any, error) {
	if len(r.Metadata) == 0 {
		return nil, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(r.Metadata, &fields); err != nil {
		return nil, fmt.Errorf("reading the metadata of the %s run: %w", r.Profile, err)
	}
	return fields, nil
}

// CLIBoard runs the board's CLI, `hermes kanban`.
type CLIBoard struct {
	// Board is the board's slug.
	Board    string
	commands runner
}

func (b *CLIBoard) List(ctx context.Context) ([]BoardCard, error) {
	out, err := b.run(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	var cards []BoardCard
	if err := json.Unmarshal([]byte(out), &cards); err != nil {
		return nil, fmt.Errorf("reading the board: %w", err)
	}
	return cards, nil
}

func (b *CLIBoard) Everywhere(ctx context.Context) ([]BoardCard, error) {
	out, err := b.commands.run(ctx, nil, "", "hermes", "kanban", "boards", "list", "--json")
	if err != nil {
		return nil, err
	}
	var boards []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(out), &boards); err != nil {
		return nil, fmt.Errorf("reading the boards: %w", err)
	}
	var cards []BoardCard
	for _, board := range boards {
		other := &CLIBoard{Board: board.Slug, commands: b.commands}
		listed, err := other.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("board %s: %w", board.Slug, err)
		}
		cards = append(cards, listed...)
	}
	return cards, nil
}

func (b *CLIBoard) Show(ctx context.Context, id string) (BoardCardDetail, error) {
	out, err := b.run(ctx, "show", id, "--json")
	if err != nil {
		return BoardCardDetail{}, err
	}
	var detail BoardCardDetail
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		return BoardCardDetail{}, fmt.Errorf("reading card %s: %w", id, err)
	}
	return detail, nil
}

func (b *CLIBoard) Runs(ctx context.Context, id string) ([]CardRun, error) {
	out, err := b.run(ctx, "runs", id, "--json")
	if err != nil {
		return nil, err
	}
	var runs []CardRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, fmt.Errorf("reading the runs of card %s: %w", id, err)
	}
	return runs, nil
}

func (b *CLIBoard) Create(ctx context.Context, card NewCard, parents []string) (BoardCard, error) {
	// The title is a positional argument of `hermes kanban create`, not a flag: --title
	// belongs to `edit`, and passing it here is an argparse error.
	args := []string{
		"create",
		card.Title,
		"--body-file", "-",
		"--assignee", card.Assignee,
		"--workspace", "dir:" + card.Workspace,
		"--max-runtime", strconv.Itoa(card.MaxRuntime),
		// main has no required status checks, so a GitHub completion contract would
		// fail at creation. The publish step checks the pull request itself.
		"--completion-contract", "local-only",
		"--idempotency-key", card.IdempotencyKey,
	}
	for _, parent := range parents {
		args = append(args, "--parent", parent)
	}
	for _, skill := range card.Skills {
		args = append(args, "--skill", skill)
	}
	if card.GoalTurns > 0 {
		args = append(args, "--goal", "--goal-max-turns", strconv.Itoa(card.GoalTurns))
	}
	if card.Model != "" {
		args = append(args, "--model", card.Model)
	}
	args = append(args, "--json")

	out, err := b.runWithStdin(ctx, card.Body, args...)
	if err != nil {
		return BoardCard{}, err
	}
	var created BoardCard
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		return BoardCard{}, fmt.Errorf("reading the card the board created: %w", err)
	}
	if created.ID == "" {
		return BoardCard{}, fmt.Errorf("the board accepted the card %q and reported no id", card.Title)
	}
	return created, nil
}

func (b *CLIBoard) Archive(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := b.run(ctx, append([]string{"archive"}, ids...)...)
	return err
}

func (b *CLIBoard) run(ctx context.Context, args ...string) (string, error) {
	return b.runWithStdin(ctx, "", args...)
}

func (b *CLIBoard) runWithStdin(ctx context.Context, stdin string, args ...string) (string, error) {
	return b.commands.run(ctx, nil, stdin, "hermes", append([]string{"kanban", "--board", b.Board}, args...)...)
}

// isMoving reports whether a card is being worked on, or is about to be.
func isMoving(card BoardCard) bool {
	return card.Status == statusRunning || card.Status == statusReady
}

// isWaiting reports whether a card holds work somebody still has to answer for.
func isWaiting(card BoardCard) bool {
	return card.Status == statusBlocked || card.Status == statusReview
}
