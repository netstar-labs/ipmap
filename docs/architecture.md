# ipmap — architecture

*The structures, the measurements that chose them, and the trade-offs taken. Figures are from a
real ~10⁸-address dataset unless marked otherwise; each candidate was verified exhaustively —
every key looked up and checked — rather than sampled.*

> **Under construction.** This describes the design the implementation is being built to. Where
> a number came from a prototype rather than this code, it says so.

## The premise

Host observation data has no range structure. Merging runs of consecutive addresses that carry
an identical value, measured over two real feeds:

| Feed | Addresses | Isolated (no same-value neighbour) | Fold |
|---|---|---|---|
| 32-bit | 107,371,346 | 72.6% | **1.196×** |
| 128-bit | 193,274,998 | **99.8%** | **1.001×** |

A prefix encoding of the first needs 97.8 million prefixes for 107.4 million addresses — more
entries than addresses — and measures **1,174 MB against 859 MB flat**. Folding costs 37% more
than not folding. The limit is the address distribution, not the value encoding: discarding every
field and keeping only membership still folds just 1.70×.

**So the design problem is not "how do we compress ranges" but "how do we store a scatter".**

## The idea

The high bits of an address do not need to be stored. Group entries by a prefix, locate the group
by index, and the prefix is implicit in *where the entry sits*.

```
  address  1.2.3.44
           └──┬──┘ └┬┘
        group key  suffix
              │      │
    idx[0x010203] ───┘        one read: where this group starts and ends
        │
        ▼
   suffix[] … 12 · 44 · 91 …   a bounded scan, typically one cache line
        │
        ▼
   value[]  … ── v ── …        one read
```

Three memory touches, bounded, with no search depth to grow. A miss usually costs **one** touch,
because an empty group has `idx[n] == idx[n+1]` and returns immediately.

## Per family

The two families use different structures because the arithmetic differs, not because the code
was written twice by accident.

| | 32-bit | 128-bit |
|---|---|---|
| Group key | first 24 bits | first 64 bits |
| Group lookup | **dense index**, 2²⁴ entries | **sorted prefix table**, binary-searched |
| Why | 2²⁴ × 4 B = 67 MB, affordable | 2⁶⁴ cannot be indexed at any density |
| Suffix | 1 byte | 8 bytes |
| Byte order on disk | little-endian | little-endian, the same — keys load and compare as native `uint64` words |

There is no byte-order asymmetry: the artifact is little-endian throughout, and a 128-bit key is
held as two host-order words compared numerically — never as bytes — so no per-probe conversion
exists to avoid.

Measured for the 128-bit family, 193 million addresses across 4,414,877 distinct 64-bit prefixes
(≈44 addresses each):

| Encoding | Size |
|---|---|
| Flat — full address + value | 3,865 MB |
| Prefix encoding | 4,055 MB |
| **Prefix-split** | **2,179 MB** |

Writing the 64-bit prefix once per group instead of once per address saves 1.55 GB — a 44%
reduction. **The low 64 bits cannot be compressed further**: only 1.9% of them fit in 32 bits and
5.2% in 48, because interface identifiers are effectively random.

## What was measured against what

Every candidate built from the same 107,371,346 addresses, verified exhaustively:

| Layout | Size | Hit | Miss |
|---|---|---|---|
| Flat — sorted keys + parallel values, binary search | 859.0 MB | 414 ns | 270 ns |
| Split on 16 bits | 537.1 MB | 214 ns | 91 ns |
| **Split on 24 bits** | **496.6 MB** | **207 ns** | **35 ns** |
| Split on 24 bits, suffix and value interleaved | 496.6 MB | 206 ns | 38 ns |
| Bitmap over the whole space + rank + values | 892.5 MB | 49 ns | 17 ns |
| General-purpose mmap hash table | 2,281.7 MB | 98 ns | 73 ns |

Read as three honest choices rather than one winner:

- **Smallest: the 24-bit split.** 1.7× smaller than flat, 4.6× smaller than the hash table.
- **Fastest: bitmap + rank.** But it pays a fixed 512 MB for a bitmap over the whole address
  space to buy O(1) access.
- **Least code: the hash table**, which is an existing, hardened library. It costs 4.6× the size,
  structurally — a hash table must store the **whole key**, since it cannot infer any of it from
  position, plus load-factor slack.

**Flat is the worst of the three on both axes**, and is the intuitive choice, which is why it is
recorded here. A random binary search over 859 MB is ~27 dependent cache misses.

## Interning

Values repeat heavily in this shape of data — measured, 312 addresses per distinct value on the
feed that motivated the design. With interning enabled the store keeps a table of the distinct
values and a fixed-width id per entry, which is where most of the size win lives. It stays
domain-agnostic: it is deduplication of repeated byte strings, and the library never looks inside
them. Both families share one table, because a value's identity has no family.

The id width is the narrowest of 1, 2, 3 or 4 bytes that holds the distinct count, derived at
build time and **asserted on every write** — an id that does not fit means the build's own
bookkeeping is wrong, and it dies there rather than corrupting a neighbour and surfacing as a
wrong answer at query time.

Two properties worth knowing:

- **The table keeps every distinct value ever added**, including values whose only entries were
  later superseded by the last-wins duplicate rule. On real feeds duplicates are a handful in
  10⁸, so compaction would buy almost nothing and is deliberately not done.
- **Duplicate semantics are unchanged by interning.** Insertion order and value identity are
  tracked separately — conflating them is the natural bug, and it breaks last-wins.

Measured on the in-memory store, 10⁸-scale address corpora with values derived onto a 16-bit
space (synthetic cardinality — the honest 312:1 figure above comes from the motivating feed, not
from this test): 107.4M and 193.3M entries interned to 65,536 distinct values, exhaustively
verified, with peak build memory *lower* than the direct layout (2.72 vs 2.83 GB at 107M) and the
128-bit build 20% faster — two-byte ids move less memory than five-byte values.

## Behaviour under scale

The shipped library, swept at 1×, 4× and 10× of a 10M-entries-per-family base (the machinery is
`scale_test.go`, opt-in via `IPMAP_SCALE`). **Caveats first, because the numbers mean nothing
without them**: one machine — Apple M2 Pro, 16 GB, go1.27.0 darwin/arm64; clustered synthetic
input, not uniform — runs of 1–40 suffixes per shared /24 and 1–6 per shared /64, the shape of
scanner and sensor feeds; 4-byte direct (uninterned) values; builds timed once per point,
lookups via `go test -bench`. Misses are reported **twice**, because they are two code paths: a
probe into a group that holds nothing (the index answers, nothing is scanned) and a probe into a
populated group (scanned or binary-searched, then absent). Only the second can degrade with
density, so averaging them would hide the curve this sweep exists to draw.

**Lookups** (ns/op; every path 0 B/op, 0 allocs/op — R7 asserted at every point):

| | 1× | 4× | 10× |
|---|---|---|---|
| 32-bit entries stored | 9.51 M | 38.0 M | 95.1 M |
| hit | 140 | 190 | 207 |
| miss, populated group | 151 | 159 | 177 |
| miss, empty group | **23** | **23** | **23** |
| 128-bit entries stored (prefixes) | 10 M (2.86 M) | 40 M (11.4 M) | 100 M (28.6 M) |
| hit | 304 | 378 | 776 |
| miss, populated group | 237 | 351 | 448 |
| miss, empty group | 165 | 265 | 350 |

- **The 32-bit family has no search depth to grow.** The hit and populated-miss drift
  (140→207 ns) is cache and TLB reach over a structure growing 0.1→0.5 GB, not algorithm; the
  empty-group miss — one index probe, no scan — is flat at 23 ns and scale-invariant.
- **The 128-bit family follows its binary search.** Each 4× in prefixes adds ~2 probe depths,
  and once the prefix table outgrows cache the per-probe constant is a memory latency:
  log₂(28.6 M) ≈ 25 dependent misses accounts for essentially all of the 776 ns.
- **No silent cliff**: both families' curves are the flat-or-logarithmic shapes the structures
  predict, at every point.

**Builds** (seconds; Add streaming half / Build sort-dedupe-fill half):

| | 1× | 4× | 10× |
|---|---|---|---|
| 32-bit add / build | 0.08 / 0.59 | 0.39 / 2.4 | 0.82 / 6.2 |
| 128-bit add / build | 0.23 / 1.4 | 0.91 / 6.2 | 8.9 / **26.7** |

**Peak build memory, measured as a function of n** (`TestBuildMemoryCurve`: each point a fresh
subprocess, true MaxRSS). The measured points fit a simple model — **peak ≈ 1.4 × live set**,
the 1.4 being GC headroom — where, for value width V, live is:

- 32-bit: (9 + 2V) bytes per entry + the fixed 67 MB index (+ the caller's own input)
- 128-bit: (35 + 2V) bytes per entry (+ the caller's own input)

Validated where the machine had room: at V=4, the model predicts 2.9 GB for the 32-bit 10× build
(measured 3.0 GB) and 3.3 GB for the 128-bit 4× (measured 3.4 GB). **The 128-bit 10× point is
the fit boundary on this machine**: the model wants ~8.3 GB alongside the OS on a 16 GB box, and
what was observed instead is MaxRSS plateauing at 4.2 GB while the build went superlinear
(6.2 s → 26.7 s for 2.5× the data) — inferred memory compression, the box paying in time what it
no longer had in space. So the sizing rule, stated rather than discovered: **keep
1.4·(35+2V)·n plus your input under about two-thirds of RAM** — on 16 GB that is roughly 175 M
128-bit entries built bare; the sweep's 100 M point, held alongside its own 1.6 GB of input,
had already crossed the line. 32-bit builds are ~2.5× cheaper per entry.

Two boundaries this is not: **the query side has none** — an opened artifact is served from the
mapping and may exceed RAM, with cold probes costing a page fault; and the entry cap (2³²−1 per
build, both families combined) sits near 260 GB of build memory, the same decade as the sort
boundary. When the sort boundary is actually reached, the lever is a **partitioned external merge
sort** — see *Headroom, deliberately untaken* in [the roadmap](roadmap.md), which records every
lever this measurement session weighed and left on the table, with its trigger. Past the line the build slows first and fails loudly (OOM) second; it does
not produce a wrong artifact — everything written is checksummed and canonical regardless.

**Where it stops working, and how it fails** (the phase's adversarial question):

- The builder refuses its 2³²−1-th entry with an error — the count must fit the artifact's
  32-bit offsets; test-pinned at the boundary.
- An oversized build degrades visibly (time) and then loudly (OOM). No path degrades silently:
  the lookup curves above are the structure's predicted shapes, and an artifact that builds is
  byte-canonical and CRC-verified whatever the memory weather was.
- The known future lever for the populated-group miss — a 256-bit occupancy bitmap per /24
  replacing the scan with a popcount — remains not worth its cost: the scan path moved 151→177 ns
  over a 10× range.

## Trade-offs taken

| Decision | Cost | Why |
|---|---|---|
| Fixed-width opaque values | callers pack their own | The constraint that lets unrelated callers share one library |
| Immutable after build | rebuild and swap to update | Lock-free concurrent reads, and no reader ever sees a partial state |
| Two structures, one per family | two implementations to maintain | 2⁶⁴ cannot be densely indexed; pretending otherwise would cost the 32-bit family its index |
| Standard library only | some wheels reinvented | A dependency this low becomes a dependency for everything above it |
| Refuse rather than guess | an unreadable artifact is an outage | A reader that answers from a format it does not understand is worse than one that does not answer |

## Concurrency

An artifact is immutable once opened, so readers need no synchronisation: many goroutines and
many processes may query one mapping concurrently. Updating means building a new artifact and
swapping the pointer — readers on the old mapping finish against bytes that are still valid.
