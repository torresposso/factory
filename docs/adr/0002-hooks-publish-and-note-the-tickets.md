# Kanban hooks publish the run and keep the tickets up to date; no publisher card

A Run ended with a publisher card whose whole job was one command, `factory publish --issue N`, run by
a model that could only get it wrong. And the GitHub tickets said nothing while a Run worked on them:
whoever opened one could not tell it was being worked on, why it stopped or what was done.

Hermes's kanban hooks cover both (`docs/hermes.md`): `kanban_task_claimed` fires in the gateway when a
card starts, while `kanban_task_blocked` and `kanban_task_completed` fire in the `hermes` container for
the assignee's profile, with the card's workspace as `cwd`, the payload on stdin, and `factory`, `gh` and
the publisher's `gh` login in reach. So:

- `plan` stops creating the publisher card. The review card is the last card of a Run.
- A new subcommand, `factory note`, reads a hook's payload on stdin, finds the card by `board` and
  `task_id`, and comments on its issue: a ticket card on its ticket, Integrate and review on the spec, a
  lone ticket on the spec. Claimed says the card is being worked on (and which attempt), blocked carries
  the worker's `reason`, completed the worker's `summary`. The wording around them is the program's own.
- `factory publish --hook` reads the same payload and does nothing unless the card is a Run's review card
  that completed. Then it publishes as `publish --issue N` does and comments on the spec with the pull
  request's link, or with why publishing failed: the card is already `done`, so the comment is where a
  failure shows. `publish --issue N` by hand stays the way to retry.
- Only the hooks configure who runs what, in each profile's `config.yaml`:
  - `coder`: `factory note` on `kanban_task_blocked` and `kanban_task_completed`.
  - `qa`: the same, plus `factory publish --hook` after `note` on `kanban_task_completed`, with a 300 s
    timeout (pushing and opening a pull request can take longer than the default 60 s).
  - root profile: `factory note --detach` on `kanban_task_claimed`. It runs in the gateway inside the
    dispatch lock, so it forks the GitHub call and returns at once. The gateway never consents to a hook
    for itself, so this one is written into `/opt/data/shell-hooks-allowlist.json` by hand; the workers
    approve their own profiles' hooks the first time they fire.
- The `orchestrator` profile is retired: no card is assigned to it. `publisher` stays only as the home
  of the `gh` login that can push (`--gh-home`); no card is assigned to it either.

## Considered options

- **Keep the publisher card.** It works, but it is a model run to type one command, and it adds a card,
  a worker and a retry budget to every Run.
- **Have the workers comment on the tickets themselves.** The coder and qa sandboxes cannot reach a `gh`
  login, on purpose, and a model writing on GitHub is exactly what the card rules keep it from doing.
- **A webhook or a poller that watches the board.** A resident process for what the hooks already
  deliver; the webhook was removed on 2026-10-24 for being that kind of ceremony.
- **A Hermes plugin on the same events.** It would be Python inside Hermes, a second program; the
  shell hooks call the one that already exists.

## Consequences

- The comments go out under the publisher's `gh` account, that is, under the owner's name. They hold
  only the program's fixed wording and what the worker reported, never a model writing to GitHub
  directly.
- A hook that fails does not change the card. A missing comment is lost; a failed publish leaves a
  comment saying so, or, if even that fails, a `done` review card and no pull request, which `publish
  --issue N` fixes.
- Each retry of a card comments again on claim; that is the record of the attempts.
- The hooks live in Basecamp's profile configs, outside this repository: they are a step of installing
  a release, written down in the README next to the pin.
- Decided 2026-10-25.
