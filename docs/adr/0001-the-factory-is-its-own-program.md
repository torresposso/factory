# The Software Factory is its own Go program, shipped as a release binary

The factory began inside one product's repository (`laiacaliente-app`, `cmd/factory` and
`internal/factory`). Four products are meant to run on it, so it became its own repository: one Go
program, released as a static binary per architecture with `SHA256SUMS`, which the Hermes image pins by
version and checksum. A product gives it three Makefile targets (`verify`, `preview`, `preview-stop`)
and an `AGENTS.md`; there is no per-repository config file.

It extracts the program from `laiacaliente-app` at commit `8cb6efae79ace1d18049db45a25ee815f8e8aa24`. The history stays there; this
repository starts fresh.

## Considered options

- **Keep it in the first product and copy it.** Every product would carry its own diverging copy of
  the card rules.
- **A Python Hermes plugin.** Hermes's plugin API was a release candidate, and the program was already
  thousands of tested lines of Go behind four fakes. A small plugin on `kanban_task_completed` may still
  replace the publisher card later.
- **A config file per repository** (risky paths, preview ports). The stack's patterns and a
  machine-wide port allocation made it unnecessary; the Makefile contract is the whole interface.

## Consequences

- The program is a tool built outside Hermes, which the house rules otherwise forbid. It is the one
  named exception, and it ships the way build tools do: from its official release, checked against a
  pinned checksum, never copied from another machine.
- A change to the card rules is a release and a new pin, not an edit in a product.
- Decided 2026-10-23.
