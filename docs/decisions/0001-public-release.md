# 0001 — Public release

*Decided 2026-09-17. Status: accepted.*

Release `ipmap` publicly under Apache-2.0, as `github.com/netstar-labs/ipmap`, tagged `v0.1.0`,
with the full documentation set including the roadmap and the audit record.

## Context

The library was built to a phased plan (P0–P8) with per-phase exit criteria and an adversarial
question at each gate, then hardened by a full audit and swept for scale behaviour. It carries no
domain knowledge by construction (R2): the value is opaque bytes of a caller-chosen width, and
nothing in the code, tests, or documentation refers to what any consumer stores in them. That
constraint is what makes the library reusable *and* publishable — the two are the same property.

Releasing it publicly is worth doing because the structure it implements is genuinely missing
from the ecosystem — prefix libraries answer a different question, and general-purpose maps pay
4.6× the size for keys this structure infers from position — and because a library this low in a
stack is easier to trust when its construction can be read.

## What was verified before releasing

| Criterion | Evidence |
|---|---|
| **R4 — nothing non-public in the repo or its history** | Every path ever committed enumerated across all revisions: 43 source/doc/config files plus the fuzz corpora, nothing else. Largest blob ever committed: 19.9 KB. No corpora, no fixtures, no binaries. A CI check rejects any blob over 5 MB history-wide |
| **R4 — beyond the repo** | Issue and pull-request bodies (all 21 + 20) and commit messages swept on the same term list — they become public with the repo, which R4's wording does not say and should |
| **R2 — no consumer domain knowledge** | Term sweep over tree, full history, and GitHub metadata: zero hits for any product, dataset, organisation, host, or path. The only domains that appear anywhere are `github.com` module paths |
| **Doc set true against the shipped code** | The P7 audit's doc-drift dimension (10/10 findings fixed), the post-P8 sweep (executive-summary's stale status and its contradicted scale claim), and a cold-reviewer pass — see below. Mechanical claims re-checked at release: `go.mod` = Go 1.25 (matches the README), dependency list is standard library only, the `Err*` table matches `ipmap.go` |
| **Licence** | Apache-2.0, holder `NetSTAR Global, Inc.`, root file only, no per-file headers — copied byte-identical from the sibling repositories so the org's licence text has exactly one spelling. The README carries the licence line in the house format |
| **Cold reviewer, twice** | Two reviewers with no prior exposure, the second fresh to the corrections the first produced. Each built the CLI, wrote a spec, ran every subcommand and all four examples, and wrote external programs against the library from the documentation alone — compiling on the first attempt both times. Both verdicts: **yes, with friction**. The second verified the rewritten size formulas against 19 artifacts across six value widths and both families, finding **0 bytes of error in every case**, and re-derived the architecture document's 2,179 MB measurement from them independently. Every finding from both rounds is listed below, and every one was fixed before release |

## What the release audit found and fixed

**A silent wrong answer, found by the cold reviewer.** A zoned address — `fe80::1%eth0` — was
accepted by `Add`, stored under its zoneless bits, and so silently merged with `fe80::1%eth1`
into a single entry counted as a *conflicting duplicate*; a lookup of the unzoned address then
answered with whichever interface was added last. The library's own fast parser had rejected
zones from P2 with the right rationale stated — *a zone identifies a link, not a host* — while
the shipped path merged them. `Add` now refuses a zoned address and `Lookup` misses on one, which
is symmetric and loud; the behaviour is documented on both and pinned by a test. This is precisely
the class of defect the whole project exists to avoid, and it survived to the release gate.

**The build script's version stamp had no target.** `build/ipmap` passes `-X main.version=$VERSION`
and its header claims "version stamped", but `main.go` declared no `version` variable, and Go's
linker discards an `-X` for a missing symbol *silently* — so every binary it produced was
unversioned and nothing said so. The variable exists now, a `version` subcommand exposes it, and
a test keeps the path wired.

**The sizing guidance was wrong in the paragraph a capacity planner copies.** "4.6 bytes per
32-bit address and 11 bytes per 128-bit address" never stated the value width it assumed (3
bytes), and the sentence after it told the reader to add the 67 MB index that the 4.6 already
amortised — double-counting it. Replaced with the exact size formulas, parameterised on value
width and verified byte-for-byte against built artifacts at three widths in both families.

**Documentation gaps that forced a reader into the source**, all closed: the panic contract and
the 4096-byte `ValLen` ceiling were documented nowhere; `LookupInto`'s buffer-sizing rule lived
only in a code comment; the artifact diagram omitted the v6 per-prefix offset array — the one
section a reader cannot reconstruct the file without — along with the flags and id-width header
fields; the spec-format section never said that the first entry fixes the value width; and the
`Add` snippet discarded its error, teaching the one mistake that silently drops entries. The user
guide now opens with a complete program, extracted from the page and run as part of this review
to confirm it compiles and prints what its comments claim.

**Claims that were true only on unix.** "An opened artifact may exceed RAM" holds where `Open`
mmaps; off unix the file is read into memory and must fit. Both the guide and the `Open` godoc
now say so — the fallback existed since P5 and no document had ever mentioned it.

**Smaller corrections:** the entry cap was stated one off (the builder holds 2³²−1 and errors on
the next, not on the 2³²−1-th); the dense index is 2²⁴+1 offsets, and the sentinel is why the
67 MB figure is what it is; `stats` reports distinct values only for interned artifacts; a
sentence in the executive summary — in the paragraph carrying the project's central
justification — did not parse; the prototype bake-off tables never stated their value width; and
the README now discloses the 67 MB floor where an evaluator meets it, since a six-address v4
artifact is 67 MB and testing small is what evaluators do.

**The executive summary had drifted** — still describing the work as in progress, and carrying
the prototype's "hit path flat across a tenfold range", which the P8 sweep contradicts for the
128-bit family. Corrected to what was measured.

The second review round then found two more, both in prose that had stood since P0:

**The sentence justifying the library's existence was false.** "Encoding that as prefixes
produces *more entries than there are addresses*" appeared in the README, the executive summary,
the architecture document and the package godoc — and the architecture document performed the
refuting subtraction two words earlier: 97.8 million prefixes for 107.4 million addresses is
*fewer* entries, by nine per cent. The true claim is sharper and survives scrutiny: the fold
leaves ~89.8 million same-value runs, CIDR-aligning them splits those back into *more prefixes
than there were runs*, and each entry grows from 8 bytes to 12 — which is why prefix encoding
measures 37% larger than flat. Corrected in all four places. For a library whose proposition is
*trust these measurements*, this was the worst sentence in the repository to have wrong.

**The failure model told readers the query API could not crash them.** "Never anything a `Map`
does" and "`defer m.Close()` is always safe" are both false for the case that matters: a lookup
after `Close` on a mapped artifact is a use-after-free that ends the process with `SIGSEGV`, not
a recoverable panic — demonstrated. The `Close` godoc had always said so; the two sentences a
reader actually consults to learn the failure model said the opposite. Both rewritten, with the
fatal case stated explicitly and the safe pattern pointed at `example/lookup`.

Also corrected: a subtraction that disagreed with the table above it (1.55 GB where 3,865 − 2,179
gives 1.69 GB); `ErrVersion`'s documented scope, which omitted unknown flag bits and older
versions; the artifact diagram's section-table row, which understated it as 20 bytes when the
reader requires a 24-byte row with zeroed reserved words; `Stats`' field names, never spelled
out; the index size written as 64 MB in code comments and 67 MB everywhere else; and the fact
that `go build` leaves `ipmap version` reporting `dev` unless the tracked build script stamps it.

Repository topics were the two-tag default; set to the discoverability set the public sibling
repositories use.

## Decisions taken here

**Apache-2.0, matching the organisation's other public libraries.** Permissive, patent-granting,
and already the house licence — a second licence in one org is a question nobody should have to
answer.

**`v0.1.0`, not `v1.0.0`.** The format version is frozen at 1 and the reader refuses anything
else, so artifacts are stable; the *Go API* has had no external consumer yet, and v0.x says that
honestly. The sibling `cidr` library follows the same convention. A `v1.0.0` is earned by a
consumer depending on the API and finding it sufficient, not by the author believing it is.

**Ship the roadmap and the audit record, unlike the leaner sibling repositories.** Both were
written to be public — the roadmap says so in its own preamble and speaks only of an abstract
"consumer" — and for a library whose entire proposition is *trust this with your lookup path*,
the phase criteria, the adversarial questions, the confirmed defects and the refuted candidates
are the evidence. The audit record also carries the levers deliberately not taken, with the
measurements that made each a no: that is what stops the next developer re-deriving it.

**Branch protection on `main` after the release, not before.** It had no work to do while the
repository was private and single-author; it has work to do the moment anyone can open a pull
request. Required status checks, no force pushes, no deletions — with administrators exempt, so
the maintainer keeps an escape hatch and remains the gate they have been throughout.

## Consequences

- The API is public and the format is frozen at version 1. A format change is a version bump plus
  a refusal path (R6); a breaking API change is `v0.2.0` until a `v1.0.0` is earned.
- The git history is public. It was audited for that, and the audit is repeatable: the term sweep
  is a `git grep` over `git rev-list --all`.
- Issues and pull requests become readable. They were swept on the same terms.
- The build boundary is documented rather than discovered: peak build memory is a stated function
  of input size, and the external-sort lever is recorded with its trigger.

## Not decided here

Whether `pkg/bits` stays inside this module or becomes its own. It ships here because it is the
value-packing companion to an opaque-value store and has a demonstrated use in `example/build`;
if it acquires consumers unrelated to `ipmap`, that is the moment to reconsider.
