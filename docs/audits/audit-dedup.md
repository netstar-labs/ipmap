# Audit — duplication (B)

Is anything said twice that could drift apart — and is the merge actually
cheaper than the duplication? Ten candidates; four confirmed, six refuted. The
skeptic measured every claim that could be measured: inlining reports,
in-binary A/B benchmarks, end-to-end lookup benchmarks. Full suite passing
under every variant tried.

## Confirmed

**B2 — the packed-id decode existed twice — CONFIRMED.** `values.get` and the
validator's `checkIDs` each decoded the id bytes by hand. This sameness is
load-bearing: the validator bounds what the reader will decode, so a drifted
decode turns the open-time guarantee into a query-time panic. Fixed with one
`(*values).id` method. Measured before shipping: the compiler inlines it at
every call site (`can inline (*values).id`), in-binary A/B 3.82–3.89 ns/op both
ways, end-to-end interned lookup unchanged. Zero cost.

**B4 — the two families' group validation loops — CONFIRMED.** validate held
two near-identical loops (offsets monotone, groups bounded, suffixes strictly
ascending), differing only in suffix type and minimum group size. Open-time
only, so no hot-path stakes — and the drift risk was live, not theoretical: the
corruption corpus had a 32-bit suffix-order case but **no 128-bit one**, so a
weakened v6 check would have passed every test. Fixed with one generic
`checkGroups[S uint8 | uint64]`, and the missing corpus case was added, so the
tested family's coverage now proves both.

**B7 — sameVal hand-rolled bytes.Equal — CONFIRMED.** Seven lines of byte loop
on the cold build path, replaced by `bytes.Equal` (which is also faster, via
memequal).

**B8 — `fmt.Errorf("%w", ErrFormat)` — CONFIRMED.** Identical message,
identical `errors.Is` behaviour, one pointless wrap allocation; the bare return
additionally makes `==` work. Strictly more permissive for callers.

## Refuted

**B1 — named constants for header field offsets — REFUTED.** The claimed
benefit was preventing *silent* drift, and drift here cannot be silent: every
field the writer emits is re-read and re-serialised, the round-trip tests
require byte identity, the reader validates reserved bytes, counts and
geometry, and the CRC-repaired corruption corpus covers the rest. A one-sided
edit fails loudly today. Frozen v1 format; the layout comment is the readable
copy.

**B3 — unify build4/build6's dedupe loops — REFUTED.** The merge needs
per-type address-equality and sequence extractors (packed uint64 vs struct
field), landing at roughly the line count it removes with a closure layer
added. Cold path; the semantics are pinned by tests in both families. (The C3
fix later rewrote both loops — still twice, for the same reason.)

**B5 — generify the u32/u64 byte helpers — REFUTED.** The `unsafe.Slice` half
generifies; the big-endian fallback does not — `PutUint32` vs `PutUint64`
survive inside any "shared" version as a type switch or injected encoders. Same
surface, more indirection, on a path that never executes on shipped hardware.

**B6 — share Lookup's family split — REFUTED.** Measured: the shared helper
does not inline (`cost 215 exceeds budget 80`), adding a permanent call inside
the hot path. Zero-alloc survives and timing is within noise, but the invariant
it protects (mapped and unmapped spellings answer identically) is already
pinned by a dedicated test. A real cost for a marginal benefit.

**B9 — share the CLI's open-and-defer blocks — REFUTED.** The three blocks
share ~6 lines and differ in flag sets; a shared opener needs a
flag-registration callback and still leaves `defer m.Close()` at each caller.
Cold plumbing, no drift risk.

**B10 — width-switch in values.get — REFUTED.** The micro win is real in
isolation (3.86→3.34 ns/op at idW=2) and invisible where it matters: end-to-end
interned lookup measured 44.5–45.7 ns/op against a 44.7–45.7 baseline, with
process-to-process variance several times the delta. A change that cannot be
seen above noise does not clear the ship bar.
