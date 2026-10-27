// Command factory drives one factory run from the terminal. It is a thin main: the
// work is in internal/factory, which the tests drive with fakes (README.md).
//
//	factory plan    --issue N [--dry-run] [--json]
//	factory clean   --issue N [--dry-run] [--json]
//	factory publish --issue N [--dry-run] [--json]
//	factory publish --hook [--dry-run] [--json]  < payload
//	factory note [--detach] [--dry-run] [--json] < payload
//
// It is started in the repository it works for: the repository, the clone, the
// worktrees and the board are read from that checkout, and each has a flag. A kanban
// hook starts it with the hook's payload on stdin instead (docs/adr/0002): the board is
// the payload's and the rest is read from the card's workspace, because the claimed
// hook runs in the gateway, far from any checkout.
//
// Every phase lists what it will do before it does any of it, and `--dry-run` stops
// after the list. That ordering is the whole safety of `clean`: the cards and the
// worktrees it is about to remove are on the screen before the first one goes.
//
// `--json` prints the plan as one JSON document on stdout — the cards and their bodies,
// the worktrees, and for a publish the pull request it would open — with every line for
// a person on stderr, so the report can be piped somewhere that parses it.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/torresposso/factory/internal/factory"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("factory: ")
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		log.SetOutput(os.Stderr)
		log.Fatal(err)
	}
}

// version is the release, set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

// phases are the commands the factory knows, and what each one is for.
var phases = map[string]string{
	"plan":    "turns a spec and its tickets into worktrees and kanban cards",
	"clean":   "removes a finished run's worktrees and archives the cards that never got done",
	"publish": "pushes the run's branch and opens its one pull request",
	"note":    "comments on a card's issue that it started, stopped or finished",
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("a phase is required\n\n%s", usage)
	}
	phase := args[0]
	if phase == "-h" || phase == "--help" || phase == "help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	if phase == "--version" || phase == "version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if _, ok := phases[phase]; !ok {
		return fmt.Errorf("unknown phase %q\n\n%s", phase, usage)
	}

	flags := flag.NewFlagSet("factory "+phase, flag.ContinueOnError)
	var parse bytes.Buffer
	flags.SetOutput(&parse)
	var (
		issue     int
		dryRun    bool
		asJSON    bool
		fromHook  bool
		detach    bool
		overrides factory.Options
	)
	flags.IntVar(&issue, "issue", 0, "the issue the run belongs to: a spec, or a lone ticket")
	flags.BoolVar(&dryRun, "dry-run", false, "print what would change, and change nothing")
	flags.BoolVar(&asJSON, "json", false, "print the plan as JSON")
	flags.BoolVar(&fromHook, "hook", false, "publish: read the run from a kanban hook's payload on stdin")
	flags.BoolVar(&detach, "detach", false, "note: comment from a process of its own and return at once")
	flags.StringVar(&overrides.Repo, "repo", "", "the GitHub repository, owner/name")
	flags.StringVar(&overrides.Clone, "clone", "", "the base checkout the worktrees come from")
	flags.StringVar(&overrides.Trees, "trees", "", "where the run's worktrees live")
	flags.StringVar(&overrides.Board, "board", "", "the kanban board")
	flags.StringVar(&overrides.Home, "gh-home", "", "HOME for the GitHub commands: the profile whose login can push")
	flags.IntVar(&overrides.GoalTurns, "goal-turns", 0, "plan: run each ticket's coder card in goal mode, at most N turns")
	flags.StringVar(&overrides.CoderModel, "coder-model", "", "plan: the model the ticket cards run on")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("%s\n\n%s", firstLine(parse.String()), usage)
	}
	if phase == "note" {
		fromHook = true
	}
	switch {
	case fromHook && phase != "publish" && phase != "note":
		return fmt.Errorf("--hook is for publish\n\n%s", usage)
	case detach && phase != "note":
		return fmt.Errorf("--detach is for note\n\n%s", usage)
	case fromHook && issue != 0:
		return fmt.Errorf("the hook's payload names the card; --issue is for a run started by hand\n\n%s", usage)
	case !fromHook && issue <= 0:
		return fmt.Errorf("--issue is required\n\n%s", usage)
	}

	if detach {
		return startDetached(args, stdin)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		hook     factory.Hook
		opts     factory.Options
		detected error
	)
	if fromHook {
		var err error
		if hook, err = factory.ReadHook(stdin); err != nil {
			return err
		}
		opts, detected = factory.HookOptions(ctx, &hook)
		if errors.Is(detected, factory.ErrNotARunCard) {
			// Every card of every board fires the claimed hook: one the factory did not
			// plan is none of its business, and says nothing.
			return nil
		}
	} else {
		opts, detected = factory.DetectOptions(ctx, ".")
	}
	override(&opts, overrides)
	if detected != nil && (opts.Repo == "" || opts.Clone == "" || opts.Trees == "" || opts.Board == "") {
		return fmt.Errorf("reading the checkout: %w; run it inside the repository, or pass --repo, --clone, --trees and --board", detected)
	}

	f := factory.New(opts)
	if fromHook && phase == "publish" {
		spec, ok, err := f.ReviewDone(ctx, hook)
		if err != nil || !ok {
			return err
		}
		issue = spec
		f.Board = factory.FromReview(f.Board, hook.Card)
		if !dryRun {
			// The review card is already done: the comment on the spec is where the
			// outcome shows, and stdout is the hook's, so the lines go to stderr.
			if err := f.PublishAndNote(ctx, issue); err != nil {
				return err
			}
			printURL(stderr, f.URL)
			return nil
		}
	}
	var plan *factory.Plan
	var err error
	switch phase {
	case "plan":
		plan, err = f.Plan(ctx, issue)
	case "clean":
		plan, err = f.Clean(ctx, issue)
	case "publish":
		plan, err = f.Publish(ctx, issue)
	case "note":
		plan, err = f.Note(ctx, hook)
		if errors.Is(err, factory.ErrNotARunCard) {
			return nil
		}
		if err == nil {
			issue = plan.Issue
		}
	}
	if err != nil {
		return err
	}

	// With --json, stdout carries the report and nothing else, so it can be piped into
	// something that parses it; the lines a person reads go to stderr beside it. A hook's
	// stdout is Hermes's, so a hook writes them on stderr too.
	human := stdout
	if asJSON || fromHook {
		human = stderr
	}
	fmt.Fprintf(human, "factory %s: issue #%d on board %s\n", phase, issue, opts.Board)
	if asJSON {
		if err := factory.WriteJSON(stdout, factory.NewReport(plan, opts.Board, dryRun)); err != nil {
			return err
		}
	}
	if len(plan.Steps) == 0 {
		fmt.Fprintln(human, "nothing to do.")
		printURL(human, f.URL)
		return nil
	}
	if !asJSON {
		for _, step := range plan.Steps {
			fmt.Fprintf(human, "plan     %s\n", step)
		}
		fmt.Fprintln(human)
	}
	if dryRun {
		fmt.Fprintf(human, "dry run: %d step(s), nothing was changed.\n", len(plan.Steps))
		return nil
	}
	if err := f.Apply(ctx, plan); err != nil {
		return err
	}
	fmt.Fprintf(human, "done: %d step(s).\n", len(plan.Steps))
	printURL(human, f.URL)
	return nil
}

// printURL names the pull request a publish opened, or the open one it found and left
// alone. That address is what a person merges.
func printURL(out io.Writer, url string) {
	if url != "" {
		fmt.Fprintf(out, "\npull request: %s\n", url)
	}
}

// override puts the flags a person passed over what was read from the checkout or the
// card.
func override(opts *factory.Options, flags factory.Options) {
	for _, field := range []struct {
		to   *string
		from string
	}{
		{&opts.Repo, flags.Repo},
		{&opts.Clone, flags.Clone},
		{&opts.Trees, flags.Trees},
		{&opts.Board, flags.Board},
		{&opts.Home, flags.Home},
	} {
		if field.from != "" {
			*field.to = field.from
		}
	}
	opts.GoalTurns = flags.GoalTurns
	if flags.CoderModel != "" {
		opts.CoderModel = flags.CoderModel
	}
}

// startDetached runs the same command again, without --detach, in a session of its own,
// with the payload in a file for its stdin, and returns without waiting. The claimed
// hook runs inside the dispatcher's lock, so it may not wait on GitHub; what the comment
// costs is the detached process's.
func startDetached(args []string, stdin io.Reader) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	payload, err := os.CreateTemp("", "factory-hook-*.json")
	if err != nil {
		return err
	}
	// The file goes once the child has it open: it reads the payload from its fd.
	defer os.Remove(payload.Name())
	defer payload.Close()
	if _, err := io.Copy(payload, stdin); err != nil {
		return err
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var again []string
	for _, arg := range args {
		if name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "="); strings.HasPrefix(arg, "-") && name == "detach" {
			continue
		}
		again = append(again, arg)
	}
	child := exec.Command(self, again...)
	child.Stdin = payload
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

// firstLine is the flag package's own complaint about a flag, without the usage it
// prints under it: usage is this program's to write.
func firstLine(text string) string {
	for index, char := range text {
		if char == '\n' {
			return text[:index]
		}
	}
	return text
}

const usage = `factory plan    --issue N [--dry-run] [--json] [--goal-turns N] [--coder-model M]
factory clean   --issue N [--dry-run] [--json]
factory publish --issue N [--dry-run] [--json]
factory publish --hook [--dry-run] [--json]  < payload
factory note [--detach] [--dry-run] [--json] < payload

  plan     turns a spec and its tickets into worktrees and kanban cards
  clean    removes a finished run's worktrees and archives the cards that never got done
  publish  pushes the run's branch and opens its one pull request
  note     comments on a card's issue that it started, stopped or finished

note and publish --hook are kanban hooks: they read Hermes's payload on stdin, and do
nothing for a card the factory did not plan. publish --hook publishes only when a run's
review card completes, and says on the spec how it went. note --detach returns at once
and comments from a process of its own, for the claimed hook.

--dry-run prints what the phase would change and changes nothing. Run it inside the
repository: the repository is origin's, the clone is the main checkout, the worktrees
are <clone>-worktrees and the board is the repository's name. --repo, --clone, --trees,
--board and --gh-home override them. factory --version prints the release.`
