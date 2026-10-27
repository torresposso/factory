# Hermes facts the program depends on

Checked on Hermes 0.22.5 (upstream `bf0203a7`) between 2026-10-24 and 2026-10-26, in `hermes kanban ... --help` and in the JSON
the CLI prints inside the `hermes` container on Basecamp, and — where the JSON says nothing — in Hermes's own
source at tag `v0.22.5`. Basecamp's image pins that tag (`nousresearch/hermes-agent:v0.22.5@sha256:9c556157…`),
and 0.22.5 is still the newest stable release.

## The board

- Always pass `--board <slug>`: the current board is a file shared by every profile. Never
  `boards switch`. `boards list --json` lists every board with its `slug`, and on Basecamp these are
  `default`, `factory-sandbox`, `laiacaliente-app`, `luciotorres-online`, `torresposso-site` and
  `voxpopuli-digital`. A board's name is its slug, with hyphens and no dots.
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
  worker registered no shell hooks, which is why the program first read the board instead; since
  2026-10-25 the hooks are configured (docs/adr/0002), and the board is still where a run's cards and
  their `summary` and `metadata` are read.
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
  `config.yaml` and in all three profiles (`coder`, `qa`, `publisher`), so it does not matter which one the gateway reads.

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
- A card has nobody to answer an approval prompt. Hermes refuses a dangerous command at once in that
  case, but a write to an agent-instruction file (`AGENTS.md`, `CLAUDE.md`, …;
  `security.protected_instruction_files`) waits `approvals.timeout` (300 s by default) before it refuses:
  seen on 2026-10-26, when a coder card lost 10 minutes on two such writes. Since that day `coder` and
  `qa` set `approvals.timeout: 0`, and the card rules forbid those writes and the commands Hermes blocks.
- A card's own Hermes model never sees an image: the `opencode-go` profile sets
  `supports_vision_tool_messages=False` and the tool-result envelope Hermes builds gets a 422, so
  `vision_analyze` falls back to a second call that describes the whole image in text — 1,200–3,300
  characters whatever the question, 12–70 s by the 2026-10-26 measurements (9.9–16.4 s across eight
  card looks that day). The relay is not the limit: probed that day, it answers 200 to an image inside
  a tool result; it is the envelope. OpenCode is unaffected because it lifts the image out of the tool
  result and sends it as a **user** message (captured body: `role:"tool"` with "Image read
  successfully", then `role:"user"` with the base64 image). A blind probe on a control PNG
  (`TUCAN 84 MAGENTA / codigo 7f3a92`, absent from every text file) read both lines in 3 s for
  $0.00019, and `-c` cost ~55% less than a fresh session because the prompt rides as cache read. Cards
  therefore hand screenshots to OpenCode now — `-c` on a coder, a fresh call on the reviewer — and
  `vision_analyze` stays for the rest of Hermes. Integrate still does not look at the tickets' images.
  `region` crops the original before the downscale, so it is for reading a detail, not for speed.
- OpenCode 2.1.4 is in the image only to read a screenshot (a card's own model never sees an image), and for the
  skills; a card writes its own code. The image carries OpenCode (npm package `@opencode/cli-linux-arm64`,
  pinned by version and sha256) with Matt Pocock's 38 skills baked in at `/opt/opencode-skills` (pinned by
  commit and tarball sha256; `github.com/mattpocock/skills`). The cards' `opencode.json`, which lives in the
  mounted cache (`~/.cache/factory-go/opencode/config/opencode/`), adds that path under `skills` and sets the
  model. 23 of those skills set `disable-model-invocation`, so OpenCode does not list them — `implement`,
  `implement-spec`, `to-spec`, `to-tickets` and `retro` among them — and a card loads them by name; `tdd` and
  `code-review` are listed. A card calls `opencode run --standalone`: a private server per call, on no
  fixed port, and `-c` still continues the session the previous call left in that workspace (probed in a
  card on 2026-10-26, second call from its own exec, which answered with the first one's words). Without
  the flag the first call starts a server on 49374 and the later ones are its clients — which dies with
  `Managed service port 49374 already in use` when anything else holds that port. The ticket goes in on
  stdin: argv is wrapped in quotes and re-escaped, so `"$(cat ticket.md)"` arrived 25 characters longer
  and with its quotes altered that day, while stdin arrived byte for byte. Both card profiles, `coder`
  and `qa`, set `terminal.docker_env.XDG_CONFIG_HOME` to the mounted cache's `opencode/config` and
  forward `OPENCODE_GO_API_KEY` by name; a profile without the pair gets OpenCode's default model
  (`big-pickle`), which cannot read an image (probed 2026-10-26: `CANNOT SEE`).
- Who writes the code in a coder card was measured on 2026-10-27 with the same tickets: spec #79 (the reading
  time of a Nota: a function, the list, the page) copied six times in factory-sandbox, one run at a time, the
  copies' ticket texts hashing the same, each branched from the same `main` (`d7433f0`). The order was PiG,
  Hermes alone, OpenCode, OpenCode, Hermes alone, PiG. The three ticket cards together took 398 s and 520 s
  with Hermes alone, 605 s and 660 s with PiG (0.5.3) and 798 s and 539 s with OpenCode; the whole run 747 s and
  926 s, 753 s and 951 s, 972 s and 879 s. The Review card took 261–468 s with the same rules in every run, so
  the totals do not tell the three apart; the ticket cards do. The diffs were alike (4–5 files, 88–126 lines).
  Since v0.5.0 the card's own model writes the code, tests first, and looks at its screenshot through
  `opencode run --standalone`; PiG and the `--worker` flag that chose between them are gone (v0.4.0–v0.4.2).
  Compare any other change the same way: the same tickets, one run at a time (two cores) and more than once.
- A foreground terminal command has five minutes. `terminal.lifetime_seconds` is 300 and
  `_cleanup_inactive_envs` recycles any sandbox that has no background process of its own, so a command
  still running at 300 s gets its container stopped and returns `exit 137`
  (`Cleaned up inactive environment for task`, 2026-10-26, after 323 s, which killed a card's implement
  run). Long work runs with `background=true` and its output in a file: the reaper refreshes the
  sandbox's activity while that process lives, and the file is where the card reads its transcript.
- The agent's own browser tools are off in a card, and turning them on would not help: the image runs
  Hermes in its default *browser-use* mode, where `browser_*` are disabled on purpose, and they run in
  the main container, where a card's preview — a port inside the card's own sandbox — does not exist.
  Cards look with `chrome-devtools-axi` inside their sandbox: `run` with a script (`page.open`,
  `page.fill`, `page.click`, `page.eval`) prints what it finds, and `screenshot` saves the image;
  selectors survive a page change, a snapshot's `@refs` do not. `run` returns only what the script writes to stdout (probed 2026-10-26 in a sandbox-like container: Chrome starts in 3.6 s cold and 1.2 s warm, a screenshot takes 0.2 s): an `eval` without `console.log` prints nothing and a top-level `return` fails with `Illegal return statement`. The rules said the opposite until v0.4.1, which is why cards wrote three or four `look.js` per ticket.
- The dashboard shows a running card as `Session: local-…` and nothing else. The transcript is in the
  profile's state database: `/opt/data/profiles/<profile>/state.db`, table `messages`, one row per turn
  with its `tool_name` and `tool_calls`. Read it with `sqlite3` in read-only mode; the card's own log
  file under `/opt/data/kanban/boards/<board>/logs/` holds only that session line.
- Where a coder card's time goes (three tickets of factory-sandbox spec #64, 2026-10-26, read from the
  profile's `state.db` and the cards' `tmp/opencode-N.log`): 66–71 % of a card of 470–730 s is waiting for
  the backgrounded OpenCode, the Hermes model 120–130 s, `make verify`, the preview and the screenshot
  under 70 s. In the OpenCode log about a quarter of the lines are it searching the disk for its skills
  (they are not listed, so it does not know the path) and about 60 % are the `code-review` step. Since
  v0.3.9 the coder rules name the skills' paths, ask for one `look.js`, and tell OpenCode to skip
  `code-review`: the review card does that review. Measured on spec #69 the next run: OpenCode fell from about 430 s to 150–210 s a ticket, but the
  cards still took 470–615 s because they polled it with `sleep` calls of 45–120 s and overslept by
  90–150 s, so since v0.3.10 the rules wait on the `.out` file in one bounded loop and ask the card to
  plan its commands and send the ones that follow each other together.
- Two cards on factory-sandbox did the same ticket on 2026-10-26: the first, told to call OpenCode with
  `--standalone` and no skills, took 271 s and 13 changed lines with a test that only looked for the text;
  the flow's card loaded `implement`, ran `tdd`, and its `code-review` subagents pushed it to assert the
  path's position, 314 s in all and 20 lines.
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
