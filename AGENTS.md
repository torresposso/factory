## What this is

The Software Factory: one Go program, `factory`, that turns a GitHub spec and its tickets into Hermes
kanban cards and the finished branch into one pull request. Read `README.md` for what it does and
`GLOSSARY.md` for its words; `docs/hermes.md` holds the Hermes facts the program depends on.

- `cmd/factory` is a thin main; `internal/factory` holds the logic and its tests.
- The card rules are embedded files in `internal/factory/templates/`. They name only the Makefile
  contract a repository gives the factory (`README.md`), never one repository's files.

## Commands

- `make verify` — `gofmt`, `go vet`, `go test ./...`. Must exit 0 before a commit; CI runs the same.
- `make build` — a local binary in `dist/`.
- A release is a tag `vX.Y.Z` pushed to `main`, after `main` is pushed and its Verify run is green (a tag on a
  red commit publishes nothing and cannot be moved): `.github/workflows/release.yml` builds `linux/amd64` and
  `linux/arm64` and publishes them with `SHA256SUMS`.

## Tests

Everything outside the program goes through four seams (`Git`, `GitHub`, `Board`, `Workspaces`), faked
in `internal/factory/fake_test.go`. No test may call `gh`, `git`, `hermes` or the network.
