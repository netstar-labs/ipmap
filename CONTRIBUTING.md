# Contributing

Read [docs/roadmap.md](docs/roadmap.md) first: it holds the rules that never relax. The ones
that decide whether a change is accepted:

- **Standard library only.** No dependencies, now or later (R1).
- **No domain knowledge.** The value is opaque bytes; nothing in code, tests or docs may refer to
  what a consumer stores in it (R2).
- **Test data is synthetic.** No real-world corpora in the repo or its history (R4).
- **Every parser is fuzzed against an oracle** and verification is exhaustive, not sampled
  (R3, R5).
- **A format change is a version bump plus a refusal path** (R6).
- **Zero allocation on every query path**, asserted by `AllocsPerRun` tests (R7).

## Workflow

Every change is an issue, then a branch, then a pull request whose body contains `Closes #N` —
CI rejects one that does not. Keep a change to one conceptual step.

## The gate

A change is not done until this is green, and a failing required check is not waivable:

```sh
export GOWORK=off
go build ./... && go vet ./...
GOOS=windows go build ./... && GOOS=windows go vet ./...
GOOS=linux GOARCH=s390x go build ./...
go test ./... && go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
test -z "$(go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./...)"
test -z "$(gofmt -l .)"
```

This is `.github/workflows/ci.yml` line for line. `deadcode` exits 0 even when it finds dead
code, so the gate is that its report is empty, not its exit status; and the tools are pinned
because a gate that can start failing with no change in the repo is not a gate.

For a bug, land the failing test first, then the fix. Security issues go through
[SECURITY.md](SECURITY.md), not a public issue.
