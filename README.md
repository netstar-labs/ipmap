# ipmap

**A static, memory-mapped map from an IP address to a fixed-width value** — built once, queried
many times, with no allocation per query. Where a CIDR library answers *which prefix covers this
address*, `ipmap` answers *what is stored against this exact address*, across hundreds of
millions of them.

> **Status: v0.1.0.** Library, artifact format and CLI, verified exhaustively at 10⁸ scale,
> hardened by a full adversarial audit ([docs/audits/](docs/audits/)) and swept for scale
> behaviour at 10⁸ entries per family ([architecture](docs/architecture.md#behaviour-under-scale)).
> The **artifact format is frozen at version 1** — a reader refuses anything it does not
> recognise rather than guessing. The **Go API is v0.x**: stable in practice, not yet promised,
> because it has had no outside consumer to prove it sufficient. Why that choice, and what was
> audited before publishing: [docs/decisions/](docs/decisions/0001-public-release.md).

## Why

Host data does not fold into ranges. A feed of individual addresses — from a scanner, a sensor,
a reputation pipeline — has almost no run structure: measured on 10⁸ real addresses, merging
every run of neighbours that share a value shrinks the set by just 1.2×, and CIDR-aligning those
runs splits them back into more prefixes than there were runs. You get 9% fewer entries, each
half again as large — an index **37% bigger** than storing every address flat.

`ipmap` takes the other route: **the high bits of an address need not be stored.** Group entries
by a prefix, locate the group by index, and those bits are implicit in position — one byte per
IPv4 address instead of four.

That positional index is a **fixed 67 MB** for the 32-bit family, paid in full whether the
artifact holds six addresses or a hundred million — it is what buys a lookup with no search in
it. The design pays for itself above roughly 10⁷ addresses; below that, a plain map is the right
tool. The 128-bit family has no such floor.

```
  BUILD (once)                                QUERY (many, concurrent)
  ────────────                                ────────────────────────
  (addr, value) ──┐
                  │   Builder          ┌──► Lookup(addr) → value, ok
  (addr, value) ──┼──────────────────► │      one index read, a bounded
                  │    ├ intern        │      scan, one value read
  (addr, value) ──┘    └ sort          └──► no allocation, no search depth
                          │
                          ▼
                   one artifact ──► mmap, CRC-verified, refuses what it cannot trust
                   ├ v4: dense prefix index + suffix + value
                   └ v6: sorted prefix table + suffix + value
```

The value is **opaque**. `ipmap` stores and returns bytes of a width fixed at build time and
never interprets them, which is what lets one library serve callers whose payloads have nothing
in common.

## Documentation

**Start here**
- [docs/introduction.md](docs/introduction.md) — what it is and what the name means
- [docs/executive-summary.md](docs/executive-summary.md) — the one-page version

**Deep dive**
- [docs/architecture.md](docs/architecture.md) — the structures, the measurements, the trade-offs
- [docs/audits/](docs/audits/) — the adversarial audit: every claim, verdict, and fix

**Operations**
- [docs/userguide.md](docs/userguide.md) — the CLI, the spec format, the artifact format

**Plan and decisions**
- [docs/roadmap.md](docs/roadmap.md) — phases, exit criteria, the test and benchmark inventory,
  and the levers deliberately not taken
- [docs/decisions/](docs/decisions/) — the decision records, starting with the public release

**Examples**
- [example/README.md](example/README.md) — four runnable demonstrations: embedding, building with
  packed values, the zero-downtime reload, interning

## Layout

| Path | Purpose |
|---|---|
| `ipmap.go` | The library's front door: options, sentinel errors, package documentation |
| `app/ipmap/` | The CLI — `build`, `lookup`, `verify`, `stats`, `version` |
| `pkg/` | Sub-packages, as the library outgrows a flat root |
| `docs/` | The documentation set above |
| `example/` | Runnable demonstrations |
| `build/ipmap` | The tracked build script |
| `testdata/` | Committed fuzz corpora — replayed by every `go test` run |

## Requirements

Go 1.25 or newer. **No dependencies outside the standard library**, and none are planned — a
dependency this low in the stack becomes a dependency for everything above it.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).
