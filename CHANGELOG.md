# Changelog

The artifact format is frozen at version 1. The Go API is v0.x: stable in practice, not yet
promised. Newest first.

## v0.3.1

**Changed**

- Remove the unexported byte-slice address parsers (`parseAddr4`, `parseAddr6`) and their helpers,
  which nothing but tests called. `netip.ParseAddr` is allocation-free on a string and faster on
  v6, and wiring the parsers in would have needed new exported API for about 10% on v4 builds
  only (#38). No exported API or behaviour changes.

## v0.3.0

Fixes from the second adversarial audit (#31–#37). v0.2.1 was never tagged; its entries are folded
in below.

**Security**

- `Open` could panic on a crafted artifact. A middle group offset in either family was never
  bounded by the entry count, so a file whose checksums had been recomputed crashed any process
  that opened it. Every group end is now checked before it is scanned (#31).
- `ipmap build` panicked on a spec value wider than 4096 bytes. It now reports a `file:line`
  error (#34).

**Changed**

- Add `ipmap.MaxValLen` (4096), the widest value a map accepts. This is new exported API, hence
  the minor version.
- ipmap now requires a 64-bit platform, and a 32-bit build fails at compile time with the
  reason. 32-bit builds never compiled before this either (#33).
- `Open` refuses a 128-bit key in `::ffff:0:0/96`. No Builder writes one and no Lookup can
  reach one, so only a crafted file is affected (#32).
- `Map.Close` returns the error from releasing the mapping, where it used to return nil.
- CLI (#35):
  - It exits 1 when a stdout write fails.
  - `build` writes through a unique temp file opened with `O_EXCL`, so concurrent builds to one
    `-out` no longer collide.
  - `<sub> -h` exits 2.
  - `version` reports the module version for `go install` builds.
- `pkg/bits`: the overflow checks in `Field` and `Reserve` can no longer wrap (#36).
- v6 lookups are about 5–10% faster (`addr6Of` via `encoding/binary`).
- New `FuzzOpenRepaired`, which fuzzes past the checksums, plus tests for the big-endian fallback
  and the v6 duplicate rule. Documentation drift is corrected throughout (#37).
- Add `SECURITY.md` (private reporting), `CONTRIBUTING.md` and this changelog.
- Raise the minimum Go version to 1.27 (`go.mod`, README, user guide, roadmap), and the CI
  staticcheck pin to v0.8.2 — v0.8.1 cannot read Go 1.27 export data.
- Documentation catches up with v0.2.0: version labels, and the executive summary's status.

## v0.2.0

- `Options.Epoch`: set the build time stamped into the artifact, so a build can be reproduced
  from its inputs. The zero value still means *now*; existing callers are unaffected.

## v0.1.0

- First public release: library, artifact format (version 1) and CLI (`build`, `lookup`,
  `verify`, `stats`, `version`), under Apache-2.0.
