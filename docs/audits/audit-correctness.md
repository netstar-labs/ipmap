# Audit — correctness and optimisation (C)

Can any input make it wrong? Seven candidates. Every correctness verdict
required a reproducing program against a scratch copy — no claim was confirmed
on reading alone. Two candidates (C4, C6) duplicated doc-drift findings and
were consolidated into D3 and D4; see [audit-docs.md](audit-docs.md).

## C1 — the entry-count guard was off by one — CONFIRMED, fixed

**Claim.** `Add`'s guard `b.n > math.MaxUint32` admits 2³² entries; the stores'
offset arrays are uint32 with a sentinel equal to the count.

**Demonstrated.** At 2³² entries the 32-bit family's final index slot wraps to
zero — every lookup in the last groups becomes a silent miss; the 128-bit
family's pidx sentinel wraps and a lookup panics slicing `[2:0]` (single-prefix
degenerate case); and the writer emits a count the reader is built to reject
(`n4 > math.MaxUint32` — it never gets the chance, since the wrapped value is
what gets written).

**Fix.** The guard is `>=`: the count itself must fit uint32. A white-box test
sets the sequence counter directly — demonstrating the off-by-one with real
Adds needs tens of gigabytes — and pins both sides of the boundary.

## C2 — build's temp+rename lacks fsync — REFUTED

**Claim.** Power loss after the rename can leave the final name holding a
zero-length or truncated file on delayed-allocation filesystems, "violating the
code's own comment" that a name that exists implies a file that serves.

**Why it fails.** The comment's stated threat is *a crash mid-write*, and
against that threat the idiom is complete — even under power loss mid-write,
the final name holds the previous artifact or nothing. The claimed window needs
power loss *after* rename, a durability promise the comment (and every doc in
the repo — checked) never makes. The residual failure mode is fail-closed: Open
CRC-verifies and refuses, and the artifact is a rebuildable derivative in a
build-verify-swap pipeline, not a WAL. The filesystem physics is real
(attenuated in practice by ext4's `auto_da_alloc`); an `of.Sync()` would be
defensible hardening against a threat the code nowhere claims to handle, not a
bug fix — and without a directory fsync it would not deliver the durability
reading the claim asserts anyway.

## C3 — DupConflicts contradicted its own documentation — CONFIRMED, fixed

**Claim.** The doc says "carried a different value than the survivor"; the code
compared each dropped entry to the *next* entry in the run, not the survivor.

**Demonstrated.** Diverges in both directions. Values 1,1,2,3 for one address:
adjacent-pairs says 2, survivor says 3. Values 1,2,1: adjacent says 2, survivor
says 1. The existing unit test pinned the wrong semantic (`want 3` where the
documented rule gives 4).

**Fix.** Code to match doc — the documented semantic is the useful one
("how many drops lost information"). Both dedupe loops now walk each address
run to its survivor and count drops against it. The unit test now pins the
distinguishing case, and the corpus test's independent tally was reworked to
survivor semantics (it had been counting salt bumps — the adjacent count).

## C5 — accepted-but-not-canonical: the interned empty artifact — CONFIRMED, fixed

**Claim.** The header-count validation accepts `interned` with zero entries in
both families — a shape no build can produce (the flag follows Distinct, which
requires an Add).

**Demonstrated.** A crafted 288-byte file (header + a lone value table) that
Open accepted; its re-serialisation both differed from the file and was itself
refused — the one hole found in accepted-implies-canonical.

**Fix.** A header-count clause: `interned && n4+n6 == 0` is corrupt. The
crafted file is now a corruption-corpus case, and the case was mutation-checked:
removing the clause in a scratch copy makes the test fail with "accepted".

## C7 — Map.Close raced with itself — CONFIRMED, fixed

**Claim.** `Close`'s nil-check-then-clear is an unsynchronised check-then-act;
concurrent Close on one Map is a data race.

**Demonstrated.** Eight racing Close calls under `-race`: a deterministic race
report every run, and an actual nil-func panic — goroutine A passes the nil
check, B clears the field, A calls nil. (The claim's headline mechanism, an
OS-level double munmap, was wrong: syscall's mmapper serialises unmaps under a
lock and the second call returns EINVAL. The crash is the simpler one.) Without
instrumentation ~32,000 racing rounds never crashed — a two-instruction window,
latent in production, guaranteed noise in any client's race-enabled test suite.

**Contract judgment.** Concurrent Close was outside the documented contract
(concurrent *queries* only), but an idiom that visibly signals idempotence
should deliver it: the check-then-act is UB under the Go memory model
regardless.

**Fix.** `sync.Once`. Close is now documented and tested as concurrently
idempotent — and the doc says plainly that Close still does not synchronise
with in-flight lookups, which remain the caller's lifetime problem.
