# Audit — simpler (A)

Would less code do the same job? Two candidates, both confirmed by a skeptic
running differential experiments against scratch copies.

## A1 — build4's cursor array — CONFIRMED, removed

**Claim.** The 64 MB `cursor := make([]uint32, 1<<24)` scatter array in build4
is dead weight: after the sort, entry `i`'s placement slot `idx[g] + cursor[g]`
always equals `i` itself.

**Why it holds.** `kept` is sorted by address. The prefix sum makes `idx[g]`
the count of entries in groups before `g`; `cursor[g]` is the count already
placed in `g`; for sorted input, entry `i` is the `(i − idx[g])`-th of its
group, so the sum is `i`. The cursor is scatter machinery for unsorted input
that the sort already made sequential.

**Evidence.** 60 random seeds plus edge suites (empty, single-entry,
single-group, group-boundary-dense), byte-identical artifacts with and without
the cursor; zero mismatches.

**Disposition.** Removed. The placement loop is now `for i, e := range kept`.

## A2 — trailingQuad duplicates parseAddr4 — CONFIRMED, deleted

**Claim.** `trailingQuad` (pre-scan for digits-and-three-dots, then
`parseAddr4`) accepts and rejects exactly what `parseAddr4` alone does at its
only call site, parseAddr6's dotted-quad branch.

**What the skeptic falsified.** The literal sub-clause "returns the same
value": `parseAddr4` returned its partial accumulator alongside `false` on
rejection (`return ip, octets == 4`) — 1,095 divergences from `(0, false)`
found by exhaustive sweep. Dead at every call site, since callers gate on the
bool — but a landmine for any future caller.

**Evidence.** Composite `parseAddr6` behaviour identical across 960,800
exhaustive strings, 3M random, 500k differential against net/netip: zero
mismatches.

**Disposition.** `trailingQuad` deleted; parseAddr6 calls `parseAddr4`
directly. And the falsified sub-clause became its own fix: `parseAddr4` now
returns `(0, false)` on every reject, so the equivalence claim is true by
construction from here on.
