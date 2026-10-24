# Hermes facts the program depends on

Checked on Hermes 0.21.6 (2026-10-15) in `hermes kanban ... --help` and in Hermes's own source.

## The board

- Always pass `--board <slug>`: the current board is a file shared by every profile. Never
  `boards switch`. `boards list --json` lists every board with its `slug`, and `list --json` includes
  each card's `body`.
- `create --json` prints the whole card; its id is `id`. `list --json` has no `parents` and no
  `idempotency_key` (only `show --json` has `parents`), so a Run's cards are found by `workspace_path`
  and recognized by the titles the program wrote.
- Parents must exist before their children are created. A child starts in `todo` until every parent is
  `done` or `archived`: archiving a parent releases its children, so unfinished cards are archived
  together.
- An idempotency key returns the existing card when it is not archived and makes a new one when it is:
  to repeat a Run, archive first — which is what `plan` does to the leftovers of an earlier Run of the
  same issue.
- `--completion-contract OWNER/REPO` fails when the repository has no required status checks. The cards
  use `local-only`, and `publish` checks the pull request itself.
- Hermes fires `kanban_task_completed` only to a Python plugin, never to a shell command, so nothing
  waits on a hook; the program reads the board.
- A worker reports `verification`, `branch`, `commit` and `changed_files` in its metadata, and
  `hermes kanban runs <id> --json` is where the Evidence reads the first three from. Workers write
  `verification` as text, as a list or as an object of check and result, and leave it out now and then;
  the Evidence lists each check in any of those shapes.

## Where it runs

- Inside the Hermes container, as the user that owns the repositories' clones. As `root`, `git` stops
  with "dubious ownership".
- GitHub commands run with `HOME` set to the publisher profile's home
  (`/opt/data/profiles/publisher/home` by default, `--gh-home` to change it), because that is the
  profile whose `gh` login can push. Reads included: `gh api` needs a token.
- Reads go through `gh api`, whose JSON the program parses. The pull request is opened with
  `gh-axi pr create`.
- The container needs `git`, `gh`, `gh-axi` and `hermes` on the `PATH`, and `factory` itself for the
  publisher card.

## A trap

An issue whose sub-issues are all closed reads as a Lone ticket, so planning it again plans a Run for
the Spec itself. If its pull request was already merged, `publish` refuses, so the worst case is a
Run's worth of work that never gets published.
