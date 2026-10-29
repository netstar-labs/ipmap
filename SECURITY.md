# Security policy

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub:
[Report a vulnerability](https://github.com/netstar-labs/ipmap/security/advisories/new).

Please include the artifact or input that triggers it (or a program that builds one), the
`ipmap version` output, and what you expected versus what happened. A reproducer is worth more
than a description.

## What counts

`ipmap`'s trust boundary is **the artifact file**: `Open` is meant to refuse any file it cannot
trust, and a lookup on an opened map is meant never to read outside it. In scope:

- a file `Open` accepts and then answers incorrectly from, or crashes on
- an input that makes the spec reader, the address parser or `Builder` panic, hang, or silently
  drop or alter an entry
- a lookup that reads out of bounds, or allocates

Out of scope, documented behaviour: a lookup after `Close` on a mapped artifact ends the process
with `SIGSEGV` — see the `Close` documentation. A build that exceeds memory on an input past the
stated limit ([architecture](docs/architecture.md#behaviour-under-scale)) is a capacity limit,
not a vulnerability.

## Supported versions

Only the latest `v0.x` release receives fixes. The artifact format is frozen at version 1; a fix
that would change it is a version bump with a refusal path, never a silent change.

## What to expect

Acknowledgement within a few working days, and a fix or a stated decision after triage. Fixes
ship as a patch release with the finding described in the changelog once a fix is available.
