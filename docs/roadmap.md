# ipmap — roadmap

*Sequenced work with entry and exit criteria. Written 2026-09-16.*

`ipmap` is a static, memory-mapped map from an IP address to a fixed-width value: built once,
queried many times, zero allocation per query. It is to host tables what a CIDR library is to
prefix tables — and it exists because host data does not fold into ranges. A dataset of ~10⁸
individual addresses folds at 1.00–1.20×, which makes a prefix structure *larger* than storing
every address, not smaller.

The idea it turns on: **the high bits of an address do not need to be stored.** Group entries by
a prefix and find the group by index, and those bits are implicit in position — one byte per
IPv4 address instead of four.

> **This repo is written to be public.** It has no knowledge of any particular dataset, product,
> or organisation, and it never will — see [R2](#rules-that-never-relax). Anything about *why* a
> given consumer wants it belongs in that consumer's own documentation.

---

## The standing gate — every phase, every commit

No phase is done until this is green, and **a failing required check is not waivable**:

```sh
export GOWORK=off
go build ./... && go vet ./... && staticcheck ./... && \
  deadcode -test ./... && test -z "$(gofmt -l .)" && go test -race ./...
```

---

## Rules that never relax

Invariants, not phase criteria. A change that needs one relaxed is the wrong change.

| # | Rule | Why |
|---|---|---|
| **R1** | **Standard library only** | A dependency this low in the stack is a dependency for every consumer |
| **R2** | **No domain knowledge, ever** | The value is opaque bytes. No field name, identifier, comment, test or fixture may refer to what a consumer stores in it. This is what keeps the library reusable *and* publishable |
| **R3** | **Verification is exhaustive, not sampled** | See T5 |
| **R4** | **No non-public content in the repo or its history** | Test data is synthetic or generated. Large real-world corpora are local-only and gitignored. Audited against the full history, not `HEAD` — a public repo's history is public too |
| **R5** | **Every parser is fuzzed against an oracle** | Hand-rolled address parsing is where silent wrong answers live |
| **R6** | **A format change is a version bump plus a refusal path** | A reader that guesses at an unknown version is worse than one that refuses |
| **R7** | **Zero allocation on every query path** | Asserted by benchmark, not by inspection |

---

## Hazard classes the tests exist to catch

Each has cost someone real time. They are **entry requirements** — the test lands before the code
it guards.

| | Hazard | The test that catches it |
|---|---|---|
| **T1** | A getter masking wider than its field silently returns its neighbours' bits | Saturate every field to full width, read each back. Catches the class, not an instance |
| **T2** | A closure capturing an error variable escapes to the heap — allocations per row, billions per build | `ReportAllocs` on every hot path, asserted at zero |
| **T3** | `size<<1` overflows at the top of the address space, so a whole-space range miscounts | Explicit boundary cases: first address, last address, `/0`, single-entry and full groups |
| **T4** | Duplicate keys carrying **conflicting** values occur in real inputs. A rank-based index cannot represent a key twice and silently misaligns every entry after the first | The builder defines, tests and *reports* a conflict rule. Duplicates are counted, never silently dropped |
| **T5** | A 20,000-key sample passed a structure that was wrong for 45 keys out of 10⁸ — a 0.04% chance of catching it | Oracle tests iterate **every** key |
| **T6** | A test expectation can be wrong while the code is right | Differential tests against `net/netip` wherever an oracle exists, rather than hand-written expectations |
| **T7** | A reader that trusts its input is the whole attack surface | A corruption corpus every member of which must be refused at open |

---

## Test inventory

Every item is a gate on the phase that introduces it and a permanent regression check after.

### Correctness

| Test | Covers | Phase |
|---|---|---|
| `TestFieldsDoNotBleed` | T1 — saturate all fields, read each back | P1 |
| `TestFieldsAreIndependent` | clearing one field disturbs no other | P1 |
| `TestSettersOverwrite` | setters clear before writing; a second write replaces | P1 |
| `TestOversizedValueDoesNotSpill` | a too-wide value truncates, never spills upward | P1 |
| `TestLookupExhaustive` | **every** key resolves to its own value — run per family (T5) | P3 |
| `TestLookupAgainstLinearScan` | the structure and a naive reference agree on every key, per family | P3 |
| `TestBoundaryAddresses` | T3 — first, last, below-first, above-last, in-gap, empty group, single-entry group, full group | P3 |
| `TestDuplicateKeyRule` | T4 — conflicting duplicates resolve deterministically and are counted | P3 |
| `TestInternedMatchesUninterned` | identical answers for every key, both build modes, both families | P4 |
| `TestIDWidthOverflowFails` | exceeding the id space fails loudly at build, not at query | P4 |
| `TestRoundTripByteIdentical` | build → write → open → re-write is byte-equal | P5 |
| `TestRefusesUnknownVersion` | R6 | P5 |
| `TestRefusesCorruptArtifact` | T7 — the full corruption corpus | P5 |
| `TestZeroCopyLookup` | the returned value aliases the mapping | P5 |
| `TestBothFamiliesOneArtifact` | one file holds both; opening routes by family | P5 |
| `TestCLIGolden` | human and `-json` output, per subcommand, both families | P6 |

### Differential — against `net/netip` as oracle

| Test | Covers | Phase |
|---|---|---|
| `TestParseAddrMatchesNetip` | ≥100k canonical addresses per family, byte-identical (R5, T6) | P2 |
| `TestParseAddrRejects` | malformed inputs the oracle rejects | P2 |
| `TestOrderMatchesNetip` | internal ordering equals `Addr.Less` over random pairs | P2 |
| `TestNextAcrossWordBoundary` | successor arithmetic carries correctly | P2 |

### Fuzz — each with a stated budget, corpus committed

| Target | Oracle / invariant | Phase |
|---|---|---|
| `FuzzParseAddr` | agreement with `net/netip`, both families | P2 |
| `FuzzBitsLayout` | random layouts round-trip | P1 |
| `FuzzOpen` | never panics; accepts nothing it then answers wrongly from (T7) | P5 |
| `FuzzSpecParse` | the text spec reader never panics | P6 |

### Corruption corpus — every member must be refused

Truncation at each section boundary · a flipped CRC per section · bumped format version ·
overflowed length fields · zero-length sections · misaligned section offsets · a header claiming
more entries than the file holds · overlapping sections · a family section present in the header
but absent from the body.

### Concurrency

| Test | Covers | Phase |
|---|---|---|
| `TestConcurrentReaders` | `-race`, many goroutines against one open map | P5 |
| `TestConcurrentOpenClose` | `-race`, open/close interleaved with reads | P5 |

---

## Benchmark inventory

All report `ns/op`, `B/op`, `allocs/op`. **Allocations are asserted at zero on every query path
(R7)**; a benchmark that reports non-zero is a failing test, not a note.

| Benchmark | Reports | Phase |
|---|---|---|
| `BenchmarkBitsGetSet` | the codec's own cost | P1 |
| `BenchmarkParseAddr` | ns/op and MB/s, per family | P2 |
| `BenchmarkLookupHit` / `BenchmarkLookupMiss` | hit and miss measured **separately**, per family — they diverge by an order of magnitude and an average hides both | P3 |
| `BenchmarkBuild` | wall clock **and peak RSS**, phased: parse · sort · emit, per family | P3 |
| `BenchmarkOpen` | mmap plus verification cost at full size | P5 |
| Scale sweep | every lookup and build benchmark at **1×, 4×, 10×**, both families | P8 |

**Benchmarks must be run against a realistic distribution, not a uniform one.** Uniformly random
keys are simultaneously the pessimistic case for the miss path and the optimistic case for cache
behaviour — measured, the gap between uniform and realistic input on the miss path is about 3×.
Reporting only uniform numbers overstates one and understates the other, so every result states
which distribution produced it.

## Phases

Each states its **goal**, **exit criteria**, and the **adversarial question** — what a skeptic
should try to prove at that gate.

> **Both address families are built together, not sequenced.** They do not share a structure —
> 32-bit keys use a dense prefix index, 128-bit keys a sorted prefix table — but they share the
> address model, the public API and the artifact format. Building one first and adding the other
> later means the format gets shaped around the first family and the second is retrofitted into
> it. The phases below are ordered so that **the format is designed with both structures already
> in hand.**

### P0 — Scaffold and gate
**Goal.** An empty repo that already enforces everything, on the house skeleton rather than an
improvised one.

**Exit.**
- House scaffold: module `github.com/netstar-labs/ipmap`, **Go 1.25** (matching the sibling
  library, so depending on `ipmap` never forces a consumer's toolchain bump) · root package *is*
  the library · `app/ipmap/` for the CLI · `pkg/bits/` for sub-packages.
- **No `LICENSE`, no `NOTICE`, no per-file headers.** House standard: a private repo is
  unlicensed — all rights reserved — and the licence is added at go-public, not before. Per-file
  copyright lines are never added at all.
- `.gitignore` covering `sandbox/`, `testdata/` and `docs/plan/` — **verified before the first
  commit**, because the corpora are already in the working tree.
- A **large-blob guard in CI**: reject any blob over a size threshold. `.gitignore` does not stop
  `git add -f`, and R4 is about the history, which is expensive to fix after a repo is public.
- The required doc set written as **real content, not stubs** — `README.md`, `docs/introduction.md`,
  `docs/executive-summary.md`, `docs/architecture.md`, `docs/userguide.md`, `example/README.md`.
  They will be wrong in places until the code exists; P9 verifies them rather than writes them.
- `ci.yml` runs the standing gate on every push and PR · `pr-issue-link.yml` enforces that every
  PR closes an issue.
- `CODEOWNERS` routes review to `the-laboratory`. **Branch protection is deferred**: the R&D head
  is the gate today, and protection only starts doing work when there is a second contributor —
  which is at go-public. CI still runs, because feedback and enforcement are different jobs.
- **`ipmap` stays out of the parent `go.work`.** It has no first-party dependency, and a
  workspace is exactly how a private-module import creeps in unnoticed.
- The scaffold commits on `main`; thereafter every change is issue → branch → PR closing it.
- P1's issue is filed, since no branch may exist without one.

**Adversarial.** *Can a file that cannot be published reach the history?*

### P1 — `bits`: the declarative codec
**Goal.** Declare a field's width once; derive every mask, shift, setter and getter from it.
**Exit.** No literal mask anywhere in the package · T1 test present **and demonstrated to fail**
against a deliberately too-wide mask · independence, overwrite, no-spill, fuzz, zero-alloc.
**Adversarial.** *Construct a layout this package accepts that loses or corrupts data.*

### P2 — The address model, both families
**Goal.** Parse, normalise, order and advance an address of either width. No storage yet.
**Exit.** One representation per family, each ordered so that comparison equals numeric address
order without wide arithmetic · **differential against `net/netip`** for parse, ordering and
successor, ≥100k canonical addresses each plus a fuzz budget (R5, T6) · successor arithmetic
carries correctly across an internal word boundary · rejects what the oracle rejects · zero
allocation.
**Adversarial.** *Find a string where this parser and `net/netip` disagree, or a pair where the
orderings differ.*

### P3 — Both stores, in memory
**Goal.** Build and lookup for **both** families. Two different structures behind one interface;
no file format yet.
**Exit.** Each family independently passes: an **exhaustive** oracle over generated *and* large
real-world input, every key resolving to its own value (T5) · agreement with a linear-scan
reference on every key · the boundary set — first, last, below-first, above-last, in-gap, empty
group, single-entry group, full group (T3) · the duplicate-key rule, defined, tested and counted
(T4) · zero-allocation lookup (R7, T2).
**The phase does not exit until both families pass.** A green 32-bit store is not a milestone on
its own; it is half of one.
**Adversarial.** *Find an address where the structure and a linear scan of the same input
disagree* — asked separately of each family.

### P4 — Interning
**Goal.** Deduplicate repeated values into a table; store ids. Family-agnostic by construction.
**Exit.** Interned and non-interned builds agree on **every** key, both families · dedup ratio
recorded in `docs/architecture.md` · id width derived from the distinct count and asserted, with
overflow failing loudly at build rather than silently at query.
**Adversarial.** *Make the two build modes disagree on one key.*

### P5 — The artifact format
**Goal.** One self-describing file holding both families, mmap'd, that refuses anything it
cannot trust. **Designed now, with both structures known** — this ordering is the point.
**Exit.** Versioned header, aligned sections, per-section CRC · byte-identical round trip ·
**entire corruption corpus refused at open** (T7) · reader fuzzed · zero-copy lookup · opening
routes by family · concurrency tests under `-race`.
**Adversarial.** *Craft a file this reader accepts and then answers incorrectly from.* The
highest-value skeptic pass in the plan — a reader that trusts its input is the whole attack
surface.

### P6 — CLI
**Goal.** `ipmap build | lookup | verify | stats` over a generic text spec.
**Exit.** Golden output tests, human and `-json`, both families · `verify` exits non-zero on any
violation · build wall-clock and peak RSS recorded per family.
**Adversarial.** *Make the CLI report success on an artifact `verify` should reject.*

### P7 — Hardening: the full audit
**Goal.** Four report-only auditors — simpler pathways · duplication · correctness and
optimization · doc-vs-code drift — produce *candidates*. Every candidate then faces an
independent skeptic given only *claim + code*, prompted to refute and defaulting to refuted when
uncertain. Correctness findings must be **reproduced by a throwaway program against a copy**:
"looks wrong" is a candidate, "here is the program that makes it misbehave" is a finding.
**Exit.** Verdicts recorded as CONFIRMED / PLAUSIBLE / REFUTED, **refuted findings kept with a
one-line reason** · `docs/audits/` committed · **zero unresolved CONFIRMED** · re-validation:
full gate green, every parser and trust boundary re-fuzzed, golden suites byte-for-byte. *A fix
that cannot survive re-validation is reverted, not shipped.*
**Adversarial.** The phase is the adversarial question.

### P8 — Scale and performance
**Goal.** Know where it breaks before a consumer finds out.
**Exit.** Scale sweep at 1×/4×/10×, both families, hit and miss separately · numbers in
`docs/architecture.md` with their caveats stated · **peak build memory documented as a function
of input size**, and the point at which an in-memory sort stops fitting stated rather than
discovered.
**Adversarial.** *At what input size does this stop working, and does it fail loudly or degrade
silently?* Silent degradation is a finding.

### P9 — Public release
**Goal.** The visibility change is the only change.
**Exit.** The doc set **verified true against the shipped code**, not merely present — it was
written at P0 and the code has moved since · **`LICENSE` added**: Apache-2.0, holder
`NetStar Global, Inc.`, root file only, no per-file headers, plus the README licence line ·
**R4 audited across the full history**, not `HEAD`: no corpora, no fixture, nothing
untracked-but-committed · **R2 audited**: no identifier, comment, test or doc refers to any
consumer's domain · branch protection enabled, now that it has work to do · a reviewer who has
never seen the repo builds and uses it from the docs alone, demonstrated · sign-off with a
decision record.
**Adversarial.** *Find one thing in this repo — including its git history — that could not be
published.*

---

## Headroom, deliberately untaken

Levers weighed during P7–P8 and left on the table, each with the trigger that would change the
answer. Recorded so the next developer inherits the analysis, not just the absence.

| Lever | Buys | Take it when |
|---|---|---|
| **Partitioned external merge sort** in Build | builds past the in-memory boundary — sequential merge runs, *not* mmap'd scratch, which thrashes under random-access sorting | input approaches ~11M 128-bit entries per GB of build-host RAM at ValLen 4 (the measured model: peak ≈ 1.4·(35+2V)·n); today's largest feed is ~5× under it on a 32 GB host |
| **Parallel sort** in Build | ~4–5× off the sort half (26.7 s at 100M 128-bit entries, single-threaded today) | build latency matters operationally — it is once-per-artifact now |
| **Per-/24 occupancy bitmap** (popcount instead of scan) | the 32-bit populated-group miss | that path degrades meaningfully — measured 151→177 ns across a 10× sweep, so not yet |
| **Huge pages / TLB relief** for multi-GB maps | the hit path's cache drift (140→207 ns over 10×) | a consumer runs latency-sensitive at ≥10⁸ entries; platform-specific |
| **128-bit prefix search layout** (Eytzinger or top-bit radix) | the log-depth hit (776 ns at 28.6M prefixes ≈ 25 dependent misses) | 128-bit hit latency at that scale is on a serving path |

## Out of scope, permanently

- **Prefix / longest-match lookup.** That is a different structure for different data; use a CIDR
  library alongside this one.
- **Mutation after build.** The map is immutable once opened. Rebuild and swap.
- **Interpreting the value.** R2. The library stores bytes and returns bytes.
