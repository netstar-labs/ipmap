# Changelog

The artifact format is frozen at version 1. The Go API is v0.x: stable in practice, not yet
promised. Newest first.

## v0.2.1

- Add `SECURITY.md` (private reporting), `CONTRIBUTING.md` and this changelog.
- Raise the minimum Go version to 1.27 (`go.mod`, README, user guide, roadmap).
- Documentation catches up with v0.2.0: version labels, and the executive summary's status.

## v0.2.0

- `Options.Epoch`: set the build time stamped into the artifact, so a build can be reproduced
  from its inputs. The zero value still means *now*; existing callers are unaffected.

## v0.1.0

- First public release: library, artifact format (version 1) and CLI (`build`, `lookup`,
  `verify`, `stats`, `version`), under Apache-2.0.
