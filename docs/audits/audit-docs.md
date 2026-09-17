# Audit — documentation drift (D)

Does any document contradict the code? Ten candidates, 10/10 confirmed by a
skeptic reading both sides of each claim independently. For seven the document
was wrong and the code right; for three (D2, D3, D5) the document described the
tool as intended and the implementation was the thing to fix.

**D1 — the 128-bit byte-order story was wrong — doc fixed.** architecture.md
said the v6 keys are stored in network order "so comparison needs no per-probe
conversion", with a paragraph explaining the deliberate asymmetry. The format
is little-endian throughout (format.go says so; the keys serialise via
`binary.LittleEndian`), and the lookup compares decoded `uint64` words, never
bytes — on disk the key bytes are the *reverse* of network order, and there is
no asymmetry. The table cell and the paragraph now say what the code does. (The
claim's rider that the error also appeared in the roadmap was itself wrong —
checked; it appears only in architecture.md.)

**D2 — "verify and stats both report the count" — implemented.** verify printed
no duplicate counts. The doc's rationale is design intent — a duplicate carrying
a different value means the input disagrees with itself, which a verification
command should surface — so verify now reports both counts unconditionally, and
the golden test pins the new receipt.

**D3 — `-in -` promised stdin and opened a file named "-" — implemented.** The
flag help said "(- for stdin)"; build wasn't even handed stdin. It is now: `-in
-` reads the spec from stdin, errors cite `stdin:LINE`, and a test proves the
artifact matches a file build byte-for-byte (epoch aside).

**D4 — Epoch's doc comment inverted reality — doc fixed.** "Zero for a Map that
has not been through a file": every Map gets `nowEpoch()` at Build; no path
produces zero. The comment now says stamped by Build, carried by the artifact —
which is what the monitoring guidance in the user guide always assumed.

**D5 — the promised examples didn't exist — implemented.** example/README said
the first example "lands with the in-memory store" (P3, three merged phases
back). `example/embed` now exists — the full build → write → open → query life
of an artifact from an embedding program — and the README ties the remaining
topics to the public release (P9) instead of a stale trigger.

**D6 — "also reads stdin" oversold lookup — docs fixed.** stdin is read only
when no address arguments are given; with arguments, piped input was silently
ignored. The package doc and user guide now match the usage string, which had
it right all along ("reads stdin when no args").

**D7 — "a fixed 67 MB regardless of entry count" — doc fixed.** The dense
index is skipped entirely for an artifact with no 32-bit entries — budgeting
guidance that overcharged the documented v6-only case by 67 MB. The sentence
now carries the exception.

**D8 — the README status line was a phase behind — doc fixed.** It placed the
frontier at the artifact format, leaving the CLI reading as unbuilt while the
user guide said the CLI was done. The status note now covers the CLI and this
audit.

**D9 — `-json` was documented as universal — doc fixed.** The user guide's
"Human-readable by default; `-json` for pipelines" blanketed all four commands;
only lookup and stats take the flag (`verify -json` is a flag error). The
synopsis now shows `[-json]` where it exists and the sentence names the two
commands.

**D10 — the package doc sent readers to the roadmap for status — doc fixed.**
"See docs/roadmap.md for what is built and what is not": the roadmap is a plan
with no completion markers of any kind. The pointer now sends status readers to
the README's status note and plan readers to the roadmap.
