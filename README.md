# factory

The Software Factory: it turns a GitHub spec and its tickets into Hermes kanban cards, and the
finished branch into one pull request a person merges. The mechanical part — reading the tickets,
ordering them, making the worktrees, creating the cards and publishing the branch — is this program. The models only write code and review it.

```
factory plan    --issue N [--dry-run] [--json]
factory clean   --issue N [--dry-run] [--json]
factory publish --issue N [--dry-run] [--json]
factory publish --hook [--dry-run] [--json]  < payload
factory note [--detach] [--dry-run] [--json] < payload
factory --version
```

## Install

Each release has a static binary per architecture and `SHA256SUMS`. Pin both in the image that runs
Hermes:

```sh
curl -fsSLO https://github.com/torresposso/factory/releases/download/v0.1.0/factory-linux-arm64
echo "<sha256 from SHA256SUMS>  factory-linux-arm64" | sha256sum -c -
install -m 0755 factory-linux-arm64 /usr/local/bin/factory
```

Then the kanban hooks, in each profile's `config.yaml` (docs/adr/0002). They run in the `hermes`
container, not in a card's sandbox, so `factory` and the `gh` login are in reach:

```yaml
# profiles/coder/config.yaml
hooks:
  kanban_task_blocked:   [{command: "factory note"}]
  kanban_task_completed: [{command: "factory note"}]

# profiles/qa/config.yaml
hooks:
  kanban_task_blocked:   [{command: "factory note"}]
  kanban_task_completed:
    - {command: "factory note"}
    - {command: "factory publish --hook", timeout: 300}

# config.yaml (the root profile, which the gateway's dispatcher reads)
hooks:
  kanban_task_claimed: [{command: "factory note --detach"}]
```

A worker approves its own profile's hooks the first time they fire. The gateway never approves one
for itself: the claimed hook goes into `/opt/data/shell-hooks-allowlist.json` by hand, or it is skipped
with "not allowlisted" in the gateway's log.

## How a run starts

1. The spec is a GitHub issue, its tickets are sub-issues, and blocking is GitHub's native `blocked_by`.
2. From the repository's checkout, where Hermes runs: `factory plan --issue <spec>`. `--dry-run` prints
   every step and touches nothing; `--dry-run --json` prints the same plan as one JSON document on
   stdout, with the lines for a person on stderr.
3. Watch it: `hermes kanban --board <slug> list`.

It is started in the repository it works for and reads the rest from that checkout: the repository is
`origin`'s, the clone is the main checkout (git's common dir, so it is the same from a run's worktree),
the worktrees are `<clone>-worktrees`, and the board is the repository's name as a slug
(`torresposso.site` is `torresposso-site`). `--repo`, `--clone`, `--trees` and `--board` override them;
`--gh-home` names the `HOME` whose `gh` login can push.

## The phases

- `plan` reads the issue's open sub-issues and their open `blocked_by`, orders the tickets (blockers
  first, ties by lower number, an error on a cycle or on a blocker that is not a ticket), makes the
  worktrees, and creates the cards. An issue with no open sub-issue is a lone ticket. It refuses when
  `factory/<N>` already has an open pull request, when a card of the run is `running` or `ready`, or when
  one is `blocked` or in `review` (a worker is waiting on an answer); and it clears what earlier runs
  left behind — the cards of this issue, its worktrees, and the worktrees of other runs whose pull
  request is closed.
- `clean` removes the run's worktrees and archives the cards of it that are not `done`; the `done` cards
  stay on the board as the record of the run, until planning the issue again archives them. It refuses
  unless the pull request exists and is closed and no card of the run is `running` or `ready`.
- `publish` pushes `factory/<N>` and opens its one pull request. It refuses unless the review card is
  `done` and the branch has commits of its own. An open pull request is reported instead of a second
  one; a closed one refuses. `publish --hook` is the qa profile's completed hook: it acts only when the
  card is a run's review card, and then comments on the spec with the pull request's link, or with why
  publishing failed. `publish --issue N` by hand is the retry.
- `note` is the hooks' comment on GitHub: when a card is claimed, blocked or completed, it says so on
  the card's issue — a ticket's card on its ticket, Integrate and review on the spec — quoting what the
  worker reported, if anything. `--detach` returns at once and comments from a process of its own, for
  the claimed hook, which runs inside the dispatcher's lock. Both read Hermes's payload on stdin and
  say nothing for a card the factory did not plan.

Every phase prints the whole list of steps before it takes the first, and `--dry-run` stops after the
list. Nothing asks a model for anything: the pull request's body is assembled from what the board and
git reported and from the program's own fixed wording.

## What a repository gives the factory

Three Makefile targets, and the card rules name nothing else of the repository:

- `make verify` — every check, exit 0 before a commit; no network, no database.
- `make preview PORT= DIR=` — serves the site on `PORT` with what it needs on `PORT+1000`, its data in
  `DIR`, fetching any binary it needs itself; it runs until stopped.
- `make preview-stop DIR=` — stops the preview started with that `DIR`, and no other.

What the rules leave to the repository — which files are generated and how they are regenerated — is
in its `AGENTS.md`, which every card reads first.

## The cards

One coder card per ticket, in the ticket order, each in its own worktree (`<trees>/<spec>-<ticket>`,
branch `factory/<spec>-<ticket>`), with the cards of its blockers as parents. Its body carries the spec
as context, the ticket verbatim, `Branch`, `Preview port`, `Merge first` and the coder rules.

With two or more tickets they are followed by an Integrate card (the run's own worktree and branch
`factory/<spec>`) and a review card, its child. A lone ticket
has no Integrate: its coder card works on `factory/<spec>` and the review card is its child. Every run
has a review card, because a coder reviewing its own work is no review.

The preview ports are the machine's, not one board's: `plan` reads the `Preview port:` lines of every
card that is not done on every board and gives each ticket, then the run, the first port from 8100
whose `PORT+1000` is free too. 8191, 9191 (a preview served by hand), 8642 (the Hermes API) and 9119
(its dashboard) are never handed out. Integrate and review share the run's port.

No card publishes: the review card's completed hook does, with the same code as `publish --issue N`,
so publishing is never a model's job (docs/adr/0002). The comments on GitHub go out under the `gh` login
`--gh-home` names, and say only the program's fixed wording and what a worker reported.

## The merge danger

The pull request names the kinds of path that play by other rules, by the stack's patterns:
`**/Dockerfile*` (the production image), `.github/**` (the CI), `**/*migrations/**` (the database),
`go.mod`, `go.sum`, `package.json`, `package-lock.json` (the dependencies), `assets/**` (the asset
pipeline) and `**/static/**` (the committed static files). A migration gets its own sentence, because a
migration that has run does not go back with a revert.

## The evidence

GitHub takes no image upload through its API. Coder and review cards save up to 3 PNG in
`tmp/evidence/` of their worktree (never committed), and `publish` pushes them to the branch
`evidence/<spec>` from a throwaway repository so the pull request can link them. That branch is never
merged; delete it after merging if you want the list tidy, and the links go with it.

## Development

See `AGENTS.md`. `make verify` is the whole check, and the tests never call `git`, `gh`, `hermes` or the
network.
