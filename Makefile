.PHONY: verify test build

# Every check, the same in CI and before a commit. No network, no board, no GitHub.
verify:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: the files above are not formatted"; exit 1; }
	go vet ./...
	go test ./...

test:
	go test ./...

# A local binary in dist/, never in the repo root.
build:
	CGO_ENABLED=0 go build -trimpath -o dist/factory ./cmd/factory
