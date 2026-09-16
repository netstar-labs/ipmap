# ipmap — user guide

> **Under construction.** The library — build, lookup, interning, the artifact — is implemented
> and verified; the CLI is not yet. See [roadmap](roadmap.md) for the phase each piece belongs
> to, and treat the API as unstable until the public release.

## Install

```sh
go get github.com/netstar-labs/ipmap        # the library
go build -o ipmap ./app/ipmap               # the CLI
```

Go 1.25 or newer. No dependencies outside the standard library.

## The library

```go
import "github.com/netstar-labs/ipmap"
```

### Building

```go
b := ipmap.NewBuilder(ipmap.Options{ValLen: 4, Intern: true})
b.Add(addr, val)          // any family; routed internally
m, err := b.Build()       // an immutable, queryable Map
_, err = m.WriteTo(f)     // one artifact, both families
```

`ValLen` is fixed for the life of the artifact: every address carries a value of exactly that
width. That is what keeps the stored arrays flat and the lookup path allocation-free.

`Intern` deduplicates repeated values into a table and stores an index in their place. Enable it
whenever many addresses share a value — a set of 10⁸ addresses drawn from 10⁵ distinct values
then pays for each value once rather than 10⁸ times. It changes nothing a caller observes: the
same bytes come back either way.

### Querying

```go
m, err := ipmap.Open(path)     // mmap, verify, refuse anything unrecognised
defer m.Close()

val, ok := m.Lookup(addr)      // val aliases the mapping — no copy, no allocation
ok = m.LookupInto(addr, dst)   // for callers that want their own buffer
```

`Lookup` returns a **subslice of the mapping**. It is valid until `Close` and must not be
modified; copy it if you need to keep it. `LookupInto` exists for callers who would rather own
the bytes than reason about lifetime.

An artifact is immutable once opened, so any number of goroutines may query one `Map`
concurrently without synchronisation. To update, build a new artifact and swap the pointer —
readers still on the old mapping finish against bytes that remain valid.

### Errors from `Open`

Distinguished because the operator response differs:

| Error | Means | Do |
|---|---|---|
| `ErrFormat` | not an ipmap artifact | check the path — this is usually configuration |
| `ErrVersion` | written by a newer format version | update the reader; it refuses rather than guesses |
| `ErrCorrupt` | failed verification — truncated, damaged, or header/body disagree | rebuild; do not serve from it |

**The reader refuses rather than guesses.** An artifact it cannot fully verify is not partially
served: answering from a format or a byte range it does not understand is worse than not
answering.

## The CLI

Designed, **not implemented yet** (roadmap P6):

```
ipmap build  -in <spec> -out <artifact>    compile a text spec into an artifact
ipmap lookup -db <artifact> <addr> [...]   query it; also reads addresses on stdin
ipmap verify -db <artifact>                check an artifact's invariants
ipmap stats  -db <artifact>                entry counts, value width, family breakdown
```

Human-readable by default; `-json` for pipelines. `verify` exits non-zero on any violation, so it
is usable as a build gate.

## The spec format

`build` reads a whitespace-separated text spec — one entry per line, `#` comments and blank lines
ignored:

```
# <address> <value-in-hex>
1.2.3.4        0a1b2c3d
2001:db8::1    0a1b2c3d
```

The value is hex of exactly `ValLen` bytes. Both families may appear in one spec; entries are
routed by family and may be given in any order, since the build sorts regardless.

**Duplicate addresses are resolved last-wins and counted.** `verify` and `stats` both report the
count, because a duplicate that carries a *different* value means the input disagrees with itself
— which is worth knowing about, and is silently destructive in any structure that cannot
represent a key twice.

## The artifact

One self-describing file, little-endian, section-aligned, with a per-section checksum:

```
header    magic · format version · build epoch · value width · entry counts per family
          section offsets · CRC per section
────────────────────────────────────────────────────────────────────────────────────
v4        dense prefix index · suffix bytes · values
v6        sorted prefix table · suffix words · values
values    the interned value table, when interning was used
```

It is designed to be `mmap`ed with no parsing: fixed-width fields, 8-byte-aligned sections. The
per-section CRC means a truncated or damaged file is **refused at open**, not discovered when a
lookup returns something plausible but wrong.

## Operational notes

**Sizing.** Budget roughly 4.6 bytes per 32-bit address and 11 bytes per 128-bit address, plus
the value table when interning. The dense index is a fixed 67 MB regardless of entry count.

**Building.** Peak memory is dominated by the sort, which is proportional to entry count. The
figure is recorded per release in [architecture](architecture.md); check it before building a
set substantially larger than the last one.

**Reloading.** Build to a temporary path, verify, rename, then open the new file and drop the old
mapping once in-flight readers have finished. Never write in place: a reader holding a mapping of
a file being rewritten underneath it has no way to detect the change.

**Monitoring.** `stats` reports entry counts, distinct values and the build epoch. An artifact
whose epoch stops advancing is the failure most worth alerting on, because nothing else about it
looks wrong.
