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
- **Zero allocation on every query path**, asserted by benchmark (R7).

## Workflow

Every change is an issue, then a branch, then a pull request whose body contains `Closes #N` —
CI rejects one that does not. Keep a change to one conceptual step.

## The gate

A change is not done until this is green, and a failing required check is not waivable:

```sh
export GOWORK=off
go build ./... && go vet ./... && staticcheck ./... && \
  deadcode -test ./... && test -z "$(gofmt -l .)" && go test -race ./...
```

For a bug, land the failing test first, then the fix. Security issues go through
[SECURITY.md](SECURITY.md), not a public issue.
