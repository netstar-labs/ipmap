# ipmap — executive summary

## What it is

A Go library and CLI that turns a large set of `(IP address, value)` pairs into a single
memory-mapped file, and answers lookups against that file with no allocation per query and no
service in between. Both address families, one artifact, standard library only.

## The problem it solves

Address data comes in two shapes, and the tooling only covers one of them.

**Allocation data is range-shaped.** Who owns a block, where a block is, which network announces
it — a few hundred thousand prefixes describing four billion addresses. Prefix-matching
structures serve this well and there are good ones.

**Observation data is not.** What a scan found, what a sensor saw, what a pipeline classified —
attached to individual addresses, with neighbours that are unrelated or absent. Measured on a
real feed of 10⁸ addresses, merging every run of consecutive addresses that share a value shrinks
the set by only **1.00–1.20×** — another way of saying there are almost no runs to merge.
Encoding that as prefixes barely reduces the entry count — 97.8 million prefixes for 107.4
million addresses, because CIDR-aligning the few runs that exist splits them back up — while
every entry grows by half. The result is an index **37% larger** than simply storing every
address.

There is no widely available structure for the second shape. `ipmap` is that structure.

## The approach

Group entries by a prefix and locate the group by index, and the prefix becomes implicit in
position rather than stored per entry. An IPv4 address then costs one byte instead of four; a
128-bit address costs eight instead of sixteen, with its prefix written once per group rather
than once per address.

Measured against the alternatives on 10⁸ real addresses at a 3-byte value width, every candidate
verified exhaustively against every key:

| Structure | Size | Lookup |
|---|---|---|
| Flat sorted array + binary search | 859 MB | 414 ns |
| **Prefix-split index** | **497 MB** | **207 ns hit · 35 ns miss** |
| Bitmap + rank | 893 MB | 49 ns |
| General-purpose mmap hash table | 2,282 MB | 98 ns |
| Prefix/CIDR encoding | 1,174 MB | — |

The prefix-split index is the smallest by a wide margin, and its **hit cost has no search depth
to grow**: swept across a tenfold range on the shipped library, the 32-bit hit moved only with
cache reach (140→207 ns) and the miss into empty space stayed flat at 23 ns; the 128-bit hit
follows its binary search over distinct prefixes, logarithmically (304→776 ns at 28.6 M
prefixes). The full curves, and their caveats, are in
[architecture](architecture.md#behaviour-under-scale).

## Why it is worth building rather than buying

Nothing off the shelf targets this shape. Prefix libraries answer a different question. A
general-purpose hash table works correctly but costs 4.6× the size, because it must store every
key in full where a positional index infers most of it. A flat sorted array is simple but is the
slowest option measured, since a random binary search over hundreds of megabytes is dominated by
cache misses.

## What it deliberately does not do

- **No prefix or longest-match lookup.** Different data, different structure; use a CIDR library
  alongside it.
- **No mutation after build.** The artifact is immutable once opened. Rebuild and swap.
- **No interpretation of the value.** Opaque bytes, by design — the constraint that keeps one
  library usable by unrelated callers.

## Status

Built and verified: library, artifact format and CLI, exhaustively tested at 10⁸ scale, hardened
by a full adversarial audit ([docs/audits/](audits/audit-findings.md)) and swept for scale
behaviour. The public release is the remaining phase; the API is not stable until it ships. The
plan it was built to: [roadmap](roadmap.md).
