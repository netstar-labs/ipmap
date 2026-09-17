# ipmap — user guide

> **v0.1.0.** The artifact format is frozen at version 1; the Go API is v0.x — stable in
> practice, not yet promised.

## Install

```sh
go get github.com/netstar-labs/ipmap        # the library
go build -o ipmap ./app/ipmap               # the CLI — `ipmap version` then says "dev"
build/ipmap                                 # or the tracked script: stamps the git version
```

Go 1.25 or newer. No dependencies outside the standard library.

## The library

The whole life of an artifact, compilable as it stands:

```go
package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"

	"github.com/netstar-labs/ipmap"
)

func main() {
	b := ipmap.NewBuilder(ipmap.Options{ValLen: 4})   // 4-byte values, not interned
	for _, e := range []struct {
		addr  string
		value []byte
	}{
		{"192.0.2.1", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"2001:db8::7", []byte{0xC0, 0xFF, 0xEE, 0x01}},
	} {
		if err := b.Add(netip.MustParseAddr(e.addr), e.value); err != nil {
			log.Fatal(err)                             // wrong width, zoned address, entry cap
		}
	}
	m, err := b.Build()                               // immutable and queryable already
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Create("demo.ipmap")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := m.WriteTo(f); err != nil {            // one artifact, both families
		log.Fatal(err)
	}
	f.Close()

	db, err := ipmap.Open("demo.ipmap")                // mmap, verify, refuse the unrecognised
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	v, ok := db.Lookup(netip.MustParseAddr("2001:db8::7"))
	fmt.Println(ok, v)                                 // true [192 255 238 1]

	dst := make([]byte, db.ValLen())                   // your buffer, your lifetime
	ok = db.LookupInto(netip.MustParseAddr("192.0.2.1"), dst)
	fmt.Println(ok, dst)                               // true [222 173 190 239]
}
```

Four more runnable programs — packed values, the zero-downtime reload, interning — are in
[example/](../example/README.md).

### Building

`ValLen` is fixed for the life of the artifact: every address carries a value of exactly that
width, between 1 and 4096 bytes. That is what keeps the stored arrays flat and the lookup path
allocation-free.

`Intern` deduplicates repeated values into a table and stores an index in their place. Enable it
whenever many addresses share a value — a set of 10⁸ addresses drawn from 10⁵ distinct values
then pays for each value once rather than 10⁸ times. It changes nothing a caller observes: the
same bytes come back either way.

`Add` reports an error — **check it** — when the value is not exactly `ValLen` bytes, when the
address is invalid or carries a zone, or when the builder reaches its cap of 2³²−1 entries (one
budget across both families; the artifact's offsets are 32-bit). A **zoned** address such as
`fe80::1%eth0` is refused rather than stored: an artifact holds hosts, and the zone identifies a
link on one machine — storing it would silently merge two interfaces into one entry.
`::ffff:192.0.2.1` is *not* refused: it is unmapped to `192.0.2.1`, so both spellings of a host
are one entry, and either spelling finds it.

### Querying

`Lookup` returns a **subslice of the mapping**. It is valid until `Close` and must not be
modified; copy it if you need to keep it. `LookupInto` exists for callers who would rather own
the bytes than reason about lifetime: `dst` must be at least `ValLen()` bytes — a shorter one
reports a miss rather than panicking, so a sizing mistake shows up in a test instead of in
production. An invalid or zoned address misses, matching `Add`.

Alongside them, `Stats()` returns `Addrs4`, `Addrs6` (addresses stored per family), `Dups`
(entries dropped by the last-wins rule), `DupConflicts` (how many of those carried a different
value than the entry that survived) and `Distinct` (interned table rows, zero when interning is
off). `ValLen()` gives the value width, and `Epoch()` the build time in unix seconds — the field
to alert on, since an artifact that stops being rebuilt looks entirely healthy otherwise.

`Close` **unmaps**. On a `Map` from `Build` there is nothing to release and it is a no-op, but on
one from `Open` it revokes the memory every value and every future lookup points into — so a
`Lookup` after `Close`, or one still running in another goroutine when `Close` lands, is a
use-after-free that ends the process with `SIGSEGV`. It is not a panic and `recover` will not
save you. `Close` is idempotent and safe to call concurrently *with itself*; it is not
synchronised with lookups, and no locking inside the library could make it so. `defer m.Close()`
is right when the `Map` does not outlive the function; when it does — a handler goroutine, a
cached pointer — close only once every reader is provably finished, as
[example/lookup](../example/lookup/main.go) shows.

An artifact is immutable once opened, so any number of goroutines may query one `Map`
concurrently without synchronisation. To update, build a new artifact and swap the pointer —
readers still on the old mapping finish against bytes that remain valid.

### What panics, and why

Only programmer error against the `Builder`'s own contract — never input:

| Call | Panics when | Because |
|---|---|---|
| `NewBuilder` | `ValLen` is not in 1..4096 | Options are program structure, declared once; a bad declaration must not survive init |
| `Add` | called after `Build` | The builder is consumed by `Build`; the alternative is building from stale state |
| `Build` | called twice | Same |

Everything else is an error return. Bad addresses, wrong-width values, damaged artifacts and
unknown format versions are all *inputs*, and inputs are refused, not fatal. Queries never panic
on their own account: a miss is a miss, a short `LookupInto` buffer is a miss, an invalid or
zoned address is a miss.

The one fatal case is lifetime, not input: **using an `Open`ed `Map`, or a value it returned,
after `Close`** — that is a use-after-free against an unmapped region, and it ends the process
with `SIGSEGV` rather than a recoverable panic. See `Close`, above.

**Platforms.** `Open` memory-maps the artifact on unix, so an artifact may exceed RAM and the
kernel pages it in as lookups touch it. Elsewhere — Windows — there is no mapping: `Open` reads
the file into memory, so the artifact must fit. Everything else behaves identically, the
verification included.

### Errors from `Open`

Distinguished because the operator response differs:

| Error | Means | Do |
|---|---|---|
| `ErrFormat` | not an ipmap artifact | check the path — this is usually configuration |
| `ErrVersion` | a format version this reader does not implement, or a flag bit it does not know | update the reader; it refuses rather than guesses |
| `ErrCorrupt` | failed verification — truncated, damaged, or header/body disagree | rebuild; do not serve from it |

**The reader refuses rather than guesses.** An artifact it cannot fully verify is not partially
served: answering from a format or a byte range it does not understand is worse than not
answering.

## The CLI

```
ipmap build  -in <spec> -out <artifact> [-intern]   compile a text spec (-in - reads stdin)
ipmap lookup -db <artifact> [-json] <addr> [...]    query it; reads stdin when no args are given
ipmap verify -db <artifact>                         check an artifact's invariants
ipmap stats  -db <artifact> [-json]                 entry counts, value width, family breakdown
ipmap version                                       what build this is
```

Human-readable by default; `lookup` and `stats` take `-json` for pipelines. `verify` exits
non-zero on any violation, so it is usable as a build gate. `build` writes `<out>.tmp` beside its
output and renames it into place, so a crash cannot leave a half-artifact under the final name —
worth knowing if `-out` points into a watched directory.

## The spec format

`build` reads a whitespace-separated text spec — one entry per line, `#` comments and blank lines
ignored:

```
# <address> <value-in-hex>
1.2.3.4        0a1b2c3d
2001:db8::1    0a1b2c3d
```

**The first entry sets the value width** — `build` has no width flag — and every later entry must
match it, to the byte. Both families may appear in one spec; entries are routed by family and may
be given in any order, since the build sorts regardless. `::ffff:1.2.3.4` is stored as the
32-bit address `1.2.3.4`, so it counts under `addrs4` in `stats`; a zoned address such as
`fe80::1%eth0` is refused with its line number.

**Duplicate addresses are resolved last-wins and counted.** `build`, `verify` and `stats` all
report the count, because a duplicate that carries a *different* value means the input disagrees
with itself — which is worth knowing about, and is silently destructive in any structure that
cannot represent a key twice. A conflict is counted against the entry that survived, not against
its neighbour in the run.

## The artifact

One self-describing file, little-endian, section-aligned, with a per-section checksum:

```
header    magic · format version · flags (bit 0 = interned) · value width · id width
          build epoch · entry counts per family · prefix, distinct and duplicate counts
          8 × section { offset u64 · length u64 · CRC u32 · 4 reserved bytes }
          header CRC u32 · 4 reserved bytes    (the reserved bytes must be zero:
          the reader refuses a file whose padding carries anything)
──────────────────────────────────────────────────────────────────────────────────────
v4        dense /24 index (2²⁴+1 offsets) · suffix byte per entry · values
v6        sorted /64 prefix table · per-prefix offsets (prefixes+1) · suffix word per
          entry · values
shared    the interned value table, when interning was used
```

Eight sections, each 8-byte aligned, in that order, ending exactly at end-of-file; a family's
sections are zero-length when it has no entries. Each family carries its **own** value section —
holding the values themselves, or packed ids into the shared table when interning is on.

It is designed to be `mmap`ed with no parsing: fixed-width fields, 8-byte-aligned sections. The
per-section CRC means a truncated or damaged file is **refused at open**, not discovered when a
lookup returns something plausible but wrong — and the reader accounts for every byte, so
anything it accepts re-serialises byte-identically.

## Operational notes

**Sizing.** The artifact size is exact, not a rule of thumb — for value width `V`:

```
v4    67,108,872  +  n × (1 + V)                       the index, then a byte and a value each
v6    prefixes × 12 + 4  +  n × (8 + V)                a table entry and an offset per /64, then
                                                       each address's low word and its value
both  280-byte header + the above + the interned table (distinct × V) when interning,
      each section rounded up to an 8-byte boundary
```

So 10⁸ 32-bit addresses at `ValLen: 4` is 567 MB, and the same count of 128-bit addresses at
~44 per /64 is 1.23 GB. **The 67 MB dense index is a fixed cost paid in full by a six-address
artifact** — it is what buys the 32-bit family its scan-free lookup — and is omitted entirely
when the artifact has no 32-bit entries. Interning replaces the per-entry `V` with an id of 1–4
bytes, which is where the win lives when values repeat.

**Building.** Peak memory is dominated by the sort, which is proportional to entry count:
roughly 1.4 × (35 + 2·ValLen) bytes per 128-bit entry, 1.4 × (9 + 2·ValLen) per 32-bit entry
plus the fixed index — measured, with the fit rule, in
[architecture](architecture.md#behaviour-under-scale). Keep a build under about two-thirds of
RAM: past that it slows first and OOMs second, and never writes a wrong artifact.

**Reloading is zero-downtime.** Build to a temporary path, verify, rename over the old name
(atomic on one filesystem), `Open` the new file, and swap an `atomic.Pointer[ipmap.Map]` — the
whole cutover is one atomic store, and no reader ever locks or blocks. Readers mid-lookup keep
answering from the old mapping: an mmap holds the file's content, not its name, so the rename
disturbs nobody. The one decision left to you is when to `Close` the old map — Close unmaps, and
values it returned die with it — so close only after its readers are provably done: a grace
period, a refcount, or simply keeping old mappings until exit. Runnable:
[example/lookup](../example/lookup/main.go). Never write an artifact in place: a reader mapped
over a file being rewritten cannot detect the change, and on most platforms a truncation under a
live mapping is a `SIGBUS` crash, not an error.

**Monitoring.** `stats` reports entry counts, the value width, duplicate counts, the build epoch,
and — when the artifact was built with `-intern` — the distinct value count. An artifact whose
epoch stops advancing is the failure most worth alerting on, because nothing else about it looks
wrong.
