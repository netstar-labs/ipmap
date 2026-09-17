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

Measured at 1×, 4× and 10× on synthesised data:

| | 1× | 4× | 10× |
|---|---|---|---|
| Split-24 size | 0.50 GB | 1.79 GB | 4.36 GB |
| Split-24 hit | 166 ns | 136 ns | **161 ns** |
| Split-24 miss | 101 ns | 188 ns | 249 ns |
| Bitmap+rank size | 0.89 GB | 1.86 GB | **3.79 GB** |
| Bitmap+rank hit | 51 ns | 61 ns | **211 ns** |

Four things follow:

1. **The hit path does not degrade** — an indexed structure has no search depth to grow.
2. **The miss path does**, as groups densify and the bounded scan lengthens. The fix is known — a
   256-bit occupancy bitmap per group, replacing the scan with a popcount — and is not worth its
   cost at present scale.
3. **The ranking inverts near 4×.** Both the dense index and the bitmap are fixed costs, so the
   bitmap is the larger structure at 1× and the smaller one at 10×.
4. **The bitmap's hit path falls off a cliff at 10×**, because what grows is the value array, and
   a random probe into several gigabytes is a TLB miss as well as a cache miss.

**The real cliff is the build, not the query.** At 10× the sort input no longer fits alongside
its output, which is why partitioned sorting is on the roadmap as headroom rather than as a
response to a problem already felt.

*Synthetic figures use a uniform distribution, which is simultaneously the pessimistic case for
the miss path and the optimistic case for cache behaviour — the real 1× miss measured 35 ns
against the synthetic 101 ns. Treat the 10× miss figure as an upper bound.*

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
