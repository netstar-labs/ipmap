// Package ipmap is a static, memory-mapped map from an IP address to a
// fixed-width value: built once, queried many times, with no allocation per
// query.
//
// It exists because host data does not fold into ranges. A dataset of individual
// addresses — the kind a scanner, a sensor or a reputation feed produces — has
// almost no run structure, so a prefix structure ends up storing more entries
// than there are addresses. ipmap is the other half of that problem: where a
// CIDR library answers "which prefix covers this address", ipmap answers "what
// is stored against this exact address", over hundreds of millions of them.
//
// The idea it turns on is that the high bits of an address need not be stored.
// Group entries by a prefix and locate the group by index, and those bits are
// implicit in position — one byte per IPv4 address rather than four.
//
// The value is opaque. ipmap stores and returns bytes of a width fixed at build
// time and never interprets them, which is what lets one library serve callers
// whose payloads have nothing in common.
//
// Status: under construction. The README's status note says what is built so
// far and docs/roadmap.md holds the plan; the public API below is not yet
// stable.
package ipmap

import "errors"

// Errors reported when opening an artifact. They are distinguished because the
// operator response differs: a wrong file is a configuration mistake, an
// unsupported version needs a newer reader, and a failed checksum means the
// bytes are damaged.
var (
	// ErrFormat reports a file that is not an ipmap artifact.
	ErrFormat = errors.New("ipmap: not an ipmap artifact")

	// ErrVersion reports an artifact written by a newer format version. A
	// reader refuses rather than guesses: answering from a format it does not
	// understand is worse than not answering.
	ErrVersion = errors.New("ipmap: unsupported format version")

	// ErrCorrupt reports an artifact that failed verification — a truncated
	// file, a damaged section, or a header inconsistent with the body.
	ErrCorrupt = errors.New("ipmap: artifact failed verification")
)

// Options configure a build. ValLen is fixed for the life of an artifact: every
// address carries a value of exactly that width, which is what makes the stored
// arrays flat and the lookup path allocation-free.
type Options struct {
	// ValLen is the width in bytes of the value stored against each address.
	ValLen int

	// Intern deduplicates repeated values into a table and stores an index in
	// their place. Worth enabling whenever many addresses share a value: a
	// feed of 10^8 addresses drawn from 10^5 distinct values pays for its
	// values once rather than 10^8 times.
	Intern bool
}
