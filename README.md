# ipmap

**A static, memory-mapped map from an IP address to a fixed-width value** — built once, queried
many times, with no allocation per query. Where a CIDR library answers *which prefix covers this
address*, `ipmap` answers *what is stored against this exact address*, across hundreds of
millions of them.

> **Status: under construction.** The library and the CLI are implemented — build, lookup,
> interning, `WriteTo`/`Open` with full verification, and the four subcommands under golden
> tests — exhaustively verified at 10⁸ scale, and hardened by a full adversarial audit
> ([docs/audits/](docs/audits/)). The public release is still ahead; the API is not yet stable.
> The plan: [docs/roadmap.md](docs/roadmap.md).

## Why

Host data does not fold into ranges. A feed of individual addresses — from a scanner, a sensor,
a reputation pipeline — has almost no run structure, so encoding it as prefixes produces *more*
entries than there are addresses, and an index larger than storing every address flat.

`ipmap` takes the other route: **the high bits of an address need not be stored.** Group entries
by a prefix, locate the group by index, and those bits are implicit in position — one byte per
IPv4 address instead of four.

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

**Plan**
- [docs/roadmap.md](docs/roadmap.md) — phases, exit criteria, the test and benchmark inventory

**Examples**
- [example/README.md](example/README.md) — runnable demonstrations, starting with embedding the library

## Layout

| Path | Purpose |
|---|---|
| `ipmap.go` | The library's front door: options, sentinel errors, package documentation |
| `app/ipmap/` | The CLI — `build`, `lookup`, `verify`, `stats` |
| `pkg/` | Sub-packages, as the library outgrows a flat root |
| `docs/` | The documentation set above |
| `example/` | Runnable demonstrations |
| `build/ipmap` | The tracked build script |

## Requirements

Go 1.25 or newer. **No dependencies outside the standard library**, and none are planned — a
dependency this low in the stack becomes a dependency for everything above it.
