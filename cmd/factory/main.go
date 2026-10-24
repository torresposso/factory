// Command factory drives one factory run from the terminal. It is a thin main: the
// work is in internal/factory, which the tests drive with fakes (README.md).
//
//	factory plan    --issue N [--dry-run] [--json]
//	factory clean   --issue N [--dry-run] [--json]
//	factory publish --issue N [--dry-run] [--json]
//
// It is started in the repository it works for: the repository, the clone, the
// worktrees and the board are read from that checkout, and each has a flag.
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
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/torresposso/factory/internal/factory"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("factory: ")
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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
}

func run(args []string, stdout, stderr io.Writer) error {
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

	opts, detected := factory.DetectOptions(context.Background(), ".")
	flags := flag.NewFlagSet("factory "+phase, flag.ContinueOnError)
	var parse bytes.Buffer
	flags.SetOutput(&parse)
	var (
		issue  int
		dryRun bool
		asJSON bool
	)
	flags.IntVar(&issue, "issue", 0, "the issue the run belongs to: a spec, or a lone ticket")
	flags.BoolVar(&dryRun, "dry-run", false, "print what would change, and change nothing")
	flags.BoolVar(&asJSON, "json", false, "print the plan as JSON")
	flags.StringVar(&opts.Repo, "repo", opts.Repo, "the GitHub repository, owner/name")
	flags.StringVar(&opts.Clone, "clone", opts.Clone, "the base checkout the worktrees come from")
	flags.StringVar(&opts.Trees, "trees", opts.Trees, "where the run's worktrees live")
	flags.StringVar(&opts.Board, "board", opts.Board, "the kanban board")
	flags.StringVar(&opts.Home, "gh-home", opts.Home, "HOME for the GitHub commands: the profile whose login can push")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("%s\n\n%s", firstLine(parse.String()), usage)
	}
	if issue <= 0 {
		return fmt.Errorf("--issue is required\n\n%s", usage)
	}
	if detected != nil && (opts.Repo == "" || opts.Clone == "" || opts.Trees == "" || opts.Board == "") {
		return fmt.Errorf("reading the checkout: %w; run it inside the repository, or pass --repo, --clone, --trees and --board", detected)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f := factory.New(opts)
	var plan *factory.Plan
	var err error
	switch phase {
	case "plan":
		plan, err = f.Plan(ctx, issue)
	case "clean":
		plan, err = f.Clean(ctx, issue)
	case "publish":
		plan, err = f.Publish(ctx, issue)
	}
	if err != nil {
		return err
	}

	// With --json, stdout carries the report and nothing else, so it can be piped into
	// something that parses it; the lines a person reads go to stderr beside it.
	human := stdout
	if asJSON {
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
// alone. That address is what the publisher card reports and what a person merges.
func printURL(out io.Writer, url string) {
	if url != "" {
		fmt.Fprintf(out, "\npull request: %s\n", url)
	}
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

const usage = `factory plan    --issue N [--dry-run] [--json]
factory clean   --issue N [--dry-run] [--json]
factory publish --issue N [--dry-run] [--json]

  plan     turns a spec and its tickets into worktrees and kanban cards
  clean    removes a finished run's worktrees and archives the cards that never got done
  publish  pushes the run's branch and opens its one pull request

--dry-run prints what the phase would change and changes nothing. Run it inside the
repository: the repository is origin's, the clone is the main checkout, the worktrees
are <clone>-worktrees and the board is the repository's name. --repo, --clone, --trees,
--board and --gh-home override them. factory --version prints the release.`
