# Hermes facts the program depends on

Checked on Hermes 0.22.5 (upstream `bf0203a7`) on 2026-10-24, in `hermes kanban ... --help` and in the JSON
the CLI prints inside the `hermes` container on Basecamp, and — where the JSON says nothing — in Hermes's own
source at tag `v0.22.5`. Basecamp's image pins that tag (`nousresearch/hermes-agent:v0.22.5@sha256:9c556157…`),
and 0.22.5 is still the newest stable release.

## The board

- Always pass `--board <slug>`: the current board is a file shared by every profile. Never
  `boards switch`. `boards list --json` lists every board with its `slug`, and on Basecamp these are
  `default`, `factory-sandbox`, `laiacaliente-app` and `torresposso-site`.
- `list --json` includes each card's `body`, and also `id`, `status`, `assignee`, `skills`, `workspace_path`,
  `workspace_kind`, `branch_name`, `max_runtime_seconds`, `completion_contract`, `max_retries`,
  `model_override`, `result` and `tenant`. It has no `parents` and no `idempotency_key`.
- `show --json` nests the card under `task` and adds `parents` (board ids), `children`, `runs`, `comments`,
  `events` and `latest_summary`; no key either. So a Run's cards are found by `workspace_path` and recognized
  by the titles the program wrote.
- `runs <id> --json` is a list, one entry per attempt, with the worker's `summary` and `metadata`: it is where
  the Evidence reads `verification`, `branch` and `commit` from. A worker also reports `changed_files` and
  `worker_session_id`. Workers write `verification` as text, as a list or as an object of check and result, and
  leave it out now and then; the Evidence lists each check in any of those shapes.
- `create --json` prints the whole card; its id is `id`. The title is positional; `--parent` (repeatable),
  `--skill`, `--workspace dir:<abs>`, `--idempotency-key` and `--max-runtime` are the flags it uses.
- Parents must exist before their children are created. A child starts in `todo` until every parent is
  `done` or `archived`: archiving a parent releases its children, so unfinished cards are archived
  together. A card created under an archived parent is born `todo` and turns `ready` on the next
  dispatcher tick or `list`.
- An idempotency key returns the existing card when it is not archived and makes a new one when it is:
  to repeat a Run, archive first — which is what `plan` does to the leftovers of an earlier Run of the
  same issue.
- `--completion-contract` takes `local-only` (the default), `OWNER/REPO` or an exact pull request URL, and the
  last two fail at completion, not at creation, when the repository has no required status checks. The cards
  use `local-only`, and `publish` checks the pull request itself.
- `kanban_task_completed` reaches a shell hook (`hooks:` in a profile's `config.yaml`). Tried on Basecamp
  on 2026-10-24: the hook in the assignee's profile ran in the worker without an allowlist entry, with
  the card's workspace as `cwd`, and got `extra.task_id`, `board`, `assignee`, `run_id` and `summary` on
  stdin, but not the run's metadata. It only fires for the profile whose config has it. On 0.21.6 the
  worker registered no shell hooks, which is why the program reads the board instead — and no profile on
  Basecamp has one configured today, so the board is still the only source.
- Tried on Basecamp on 2026-10-25 with all three kanban hooks in every profile: `kanban_task_blocked`
  behaves like `kanban_task_completed` (the assignee's worker, the workspace as `cwd`, `extra.reason`
  instead of `summary`), and a worker writes its own profile's hooks into that profile's
  `shell-hooks-allowlist.json` the first time they fire. `kanban_task_claimed` fires in the gateway's
  dispatcher, once, from the **root** profile only (`profile_name` `default`, `cwd` `/opt/data`), with
  `task_id`, `board`, `assignee` and `run_id`. The gateway never consents for itself: until the hook is in
  `/opt/data/shell-hooks-allowlist.json` (or `hooks_auto_accept` is on) it logs "not allowlisted" and skips it.
  It runs inside the dispatch lock, so it has to return at once. Its `board` is **not** the card's: it is
  the gateway's current board (`hermes kanban boards switch`), whichever board the claimed card is on —
  seen on 2026-10-25 with a `factory-sandbox` card whose payload said `torresposso-site`. `factory note`
  looks the card up on every board for that reason. Blocked and completed carry the right board.
- Tried again on 2026-10-25 with `coder` and `qa` cards (two completed, one blocked): their hooks fire too,
  and they run in the `hermes` container itself, not in the card's throwaway one — uid 1001, `HOME` the
  profile's home (`/opt/data/profiles/<profile>/home`), the workspace as `cwd`, with `factory`, `gh`, the
  repository's `origin` and the publisher's `gh` login all in reach. The model's tools stay in the sandbox;
  only the hook command, which is the profile's config and not the model's choice, gets out.
  A worker's hook may read the board but not write it: every process a worker spawns carries
  `HERMES_DELEGATED_CHILD_CONTEXT`, and the board refuses its writes ("delegate_task child contexts cannot
  mutate Kanban tasks or boards"). `hermes kanban list` is one of them — it promotes ready cards before it
  lists — while `show` and `runs` only read. `factory publish --hook` reads the run from the review card up
  through its parents with `show` for that reason (seen on 2026-10-25, when the first publish failed on `list`).
- A comment on a card (`kanban_comment`, `hermes kanban comment`) calls no hook at all: `add_comment` only
  appends a `commented` event (read in `hermes_cli/kanban_db.py` of 0.22.5), and `on_kanban_task_updated`
  is a plugin hook for other writes. `show --json` lists them under `comments` (`author`, `body`,
  `created_at`) and the attempts under `runs` (`id`, `started_at`), so a blocked or completed Note quotes the
  comments of that run on the issue — the soonest the issue can see them.
- The dispatcher is not a service of its own: it lives inside the gateway that owns the board. On Basecamp
  `kanban.dispatch_interval_seconds` is 10, `review_dispatch` is on, and `max_in_progress` is 3 in the root
  `config.yaml` and in all four profiles, so it does not matter which one the gateway reads.

## Where it runs

- Inside the `hermes` container, as `hermes`, uid 1001 — the user that owns the repositories' clones under
  `/home/ubuntu/repos`. `s6` (running as root) starts the gateway and the dashboard as that user.
- `docker exec hermes …` lands as **root**, and as root `git` stops with "dubious ownership" in those clones.
  Every command typed by hand goes `docker exec -u hermes hermes …`, and `hermes` is not on the host's `PATH`
  at all.
- `coder` and `qa` do not run there: each card gets a throwaway container of the same image
  (`terminal.backend: docker`), as uid 1001, with the card's directory as `/workspace` and only
  `/home/ubuntu/repos` and the Go cache mounted. `HOME` is `/tmp`, so `/opt/data` — and with it every `gh`
  login — is out of reach: a coder card builds, tests and commits, and only `publish` pushes.
- The hooks, `plan` and `clean` run in the container itself, as uid 1001. GitHub commands run with
  `HOME` set to the publisher profile's home (`/opt/data/profiles/publisher/home` by default, `--gh-home` to
  change it), because that is the profile whose `gh` login can push; no card is assigned to `publisher`
  any more, it is only that login's home (docs/adr/0002). Reads included: `gh api` needs a token.
- Everything goes through `gh api`: reads print the JSON the program parses, and the pull request
  (`POST repos/<repo>/pulls`), the Notes (`POST repos/<repo>/issues/<n>/comments`) and the Project moves
  (`graphql`) send JSON on stdin with `--input -`. Since 2026-10-25 that login is the machine account
  `torrespossobot`, with write on the repositories and on the Project, and the `project` scope; the
  owner's login stays beside it, inactive (`gh auth switch` brings it back).
- The pull request comes from that machine account, so GitHub tells the owner nothing on its own: once it
  is open, `publish` asks the repository's owner for a review (`POST repos/<repo>/pulls/<n>/requested_reviewers`),
  which notifies them and puts it in their review queue. The author is never asked — GitHub refuses that.
  Its title is the spec's without the trailing `(spec)`.
- A Note also moves the card's issue on every Project it is in whose `Status` has the option: to
  `In progress` when the card is claimed (a ticket's spec too), to `Blocked` when it blocks. In review
  and Done are the Project's own workflows, moved by the pull request's `Closes #`.
- The labels follow the run, one state at a time, the way the Project's `Status` does: on claim the
  issue and its spec get `factory` (which stays, so a search finds every issue the factory touched) and
  `in-progress`, and lose `ready-for-agent` and `blocked`; on block the issue swaps `in-progress` for
  `blocked`; when the pull request opens, the spec and every ticket it closes swap `in-progress` for
  `in-review`. The merge closes them with their labels. Each change reads the issue's labels and puts the
  whole set back (`PUT repos/<repo>/issues/<n>/labels`), because a `DELETE` of a label the issue lacks is
  a 404. On claim it also assigns them to the login it acts as (`gh api user`); on block it assigns the
  issue to the repository's owner, whose answer the card waits for. Assignees are only added. A label the
  repository lacks is made by GitHub on the spot, grey (`#ededed`; tried on 2026-10-25), so the four are
  created with colour and description in each repository beforehand.
- The container needs `git`, `gh` and `hermes` on the `PATH`, plus `factory` itself for the hooks:
  all of them are in the image.

## A trap

An issue whose sub-issues are all closed reads as a Lone ticket, so planning it again plans a Run for
the Spec itself. If its pull request was already merged, `publish` refuses, so the worst case is a
Run's worth of work that never gets published.
