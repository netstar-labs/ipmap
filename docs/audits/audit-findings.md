# Audit findings — P7, 2026-09-16

The full adversarial audit of everything built through P6: library, artifact
format, CLI, docs. This file is the top line and the verdict table; the four
dimension files hold each claim, its evidence, and — for the refuted — why it
failed.

## Method

Four report-only auditors swept the tree, one per dimension: **simpler** (would
less code do the same job), **duplication** (is anything said twice that could
drift apart), **correctness and optimisation** (can any input make it wrong or
measurably faster), **doc drift** (does any document contradict the code). Their
candidates went to independent skeptics who saw only the claim and the code,
were prompted to refute, and defaulted to REFUTED: a correctness claim needed a
reproducing program against a scratch copy, an equivalence claim a differential
run, a performance claim an end-to-end benchmark. Nothing ran against the repo
tree itself.

Verdicts: **CONFIRMED** (demonstrated), **PLAUSIBLE** (credible, not
demonstrated — none survived at this grade; everything resolved to one side),
**REFUTED** (kept, with the reason). Two correctness candidates duplicated
doc-drift claims and were consolidated there (C4→D3, C6→D4).

## The verdict table

| # | Claim | Verdict | Disposition |
|---|---|---|---|
| A1 | build4's 67 MB cursor array is removable: the placement index equals the loop index for sorted input | CONFIRMED | removed |
| A2 | trailingQuad duplicates parseAddr4 at its only call site | CONFIRMED | deleted; parseAddr4's reject path no longer leaks a partial value |
| B1 | header field offsets should be named constants to prevent silent drift | REFUTED | drift cannot be silent: round-trip and validation tests reject one-sided edits loudly |
| B2 | the packed-id decode exists twice — lookup path and validator — and could drift | CONFIRMED | single decoder `values.id`, inlined at every call site |
| B3 | build4/build6's dedupe loops should be unified | REFUTED | per-type extractors cost what the merge saves; semantics pinned by tests in both families |
| B4 | the two families' group/suffix validation loops should be unified | CONFIRMED | one generic `checkGroups`; the audit also found and closed a corpus gap (no 128-bit suffix-order case) |
| B5 | the u32/u64 byte helpers should be one generic pair | REFUTED | the big-endian fallback still needs both widths; same surface, more indirection |
| B6 | Lookup's family split should be a shared helper | REFUTED | the helper does not inline (cost 215 > budget 80): a permanent call on the hot path for an invariant a test already pins |
| B7 | sameVal's direct branch hand-rolls bytes.Equal | CONFIRMED | bytes.Equal |
| B8 | `fmt.Errorf("%w", ErrFormat)` wraps without adding anything | CONFIRMED | bare return |
| B9 | the CLI's three open-and-defer blocks should share an opener | REFUTED | ~6 shared lines, needs a flag-registration callback; cold plumbing, no drift risk |
| B10 | values.get should switch on id width | REFUTED | the micro win (≤0.7 ns) is invisible end-to-end, under process variance |
| C1 | the entry-count guard is off by one: the 2³²-th entry wraps uint32 offsets | CONFIRMED | guard is now `>=`; white-box cap test added |
| C2 | build's temp+rename needs fsync or it violates its own comment | REFUTED | the comment's threat is a crash mid-write, which rename handles; the power-loss window is fail-closed (Open refuses) and was never promised away |
| C3 | DupConflicts counts adjacent pairs, contradicting its doc ("different than the survivor") | CONFIRMED | run-based counting against the survivor; test and corpus tallies corrected |
| C5 | Open accepts an interned artifact with zero entries — a file no build produces and that cannot re-serialise to itself | CONFIRMED | header-count clause added; crafted corpus case added, mutation-checked |
| C7 | Map.Close is a check-then-act race: concurrent Close can call a nil func | CONFIRMED | sync.Once; concurrent-Close test added under the race pass |
| D1–D10 | ten documentation claims contradict the code | CONFIRMED (10/10) | see [audit-docs.md](audit-docs.md); D2, D3, D5 fixed implementation-side, the rest doc-side |

**Zero unresolved CONFIRMED.**

## Re-validation

After every fix, on the audit branch:

- full gate green: build, vet, staticcheck, deadcode, gofmt, tests plain and `-race`
- both parsers and the artifact reader re-fuzzed, no findings
- CLI goldens byte-for-byte, with one deliberate change: `verify` now reports
  the duplicate counts (D2)
- the corpus oracles re-run over both families (build, store, and format code
  all changed), every line verified through the file-backed map

## The dimension files

- [audit-simplify.md](audit-simplify.md) — A1, A2
- [audit-dedup.md](audit-dedup.md) — B1–B10
- [audit-correctness.md](audit-correctness.md) — C1–C7
- [audit-docs.md](audit-docs.md) — D1–D10
