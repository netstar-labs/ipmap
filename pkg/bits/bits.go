// Package bits packs fields into a 64-bit word from a declaration of their
// widths. Declare each field's width once and every mask, shift, setter and
// getter is derived from it — no accessor carries its own hand-written mask.
//
// The hazard this exists to remove: a getter whose mask is wider than its field
// silently returns its neighbours' bits along with its own. Hand-maintained
// mask constants drift from the widths they were meant to cover, the read looks
// plausible, and nothing fails. Deriving every mask from the declared width
// makes that class of defect impossible to write, and the package's tests prove
// the property rather than assume it: saturate every field, read each back.
//
// Layouts are declared once, typically as package-level variables:
//
//	var (
//		l     bits.Layout
//		kind  = l.Field(15)
//		score = l.Field(3)
//		_     = l.Reserve(4) // explicitly unassigned
//	)
//
// Fields are allocated from bit 0 upward. A declaration that cannot fit —
// a zero width, or a total past 64 bits — panics at declaration time, which
// for a package-level layout means at init, loudly, in every test run.
//
// Why it ships with ipmap: the store holds opaque fixed-width values and never
// interprets them, so packing structure into those bytes is the caller's job —
// and this package is that job done once, correctly. example/build packs a
// multi-field value this way. ipmap's own artifact format does not use it: the
// format's offsets are frozen wire layout, spelled out longhand in format.go
// where the file format is documented.
package bits

import "fmt"

// Layout allocates fields in a 64-bit word, lowest bits first. The zero value
// is an empty layout ready to use. A Layout is not safe for concurrent
// declaration; declare at init and share the resulting Fields freely.
type Layout struct {
	used uint
}

// Field allocates the next width bits and returns the accessor for them.
// It panics if width is zero or the layout would exceed 64 bits: a layout is
// program structure, not input, and a bad one must not survive init.
func (l *Layout) Field(width uint) Field {
	if width == 0 {
		panic("bits: zero-width field")
	}
	if l.used+width > 64 {
		panic(fmt.Sprintf("bits: layout overflows a 64-bit word: %d used + %d wanted", l.used, width))
	}
	f := Field{shift: l.used, width: width}
	l.used += width
	return f
}

// Reserve allocates width bits that no field will ever read or write, so the
// gap is a declaration rather than an accident. It returns the reserved width
// purely so a declaration can bind it to the blank identifier.
func (l *Layout) Reserve(width uint) uint {
	if l.used+width > 64 {
		panic(fmt.Sprintf("bits: reserve overflows a 64-bit word: %d used + %d wanted", l.used, width))
	}
	l.used += width
	return width
}

// Used reports how many of the word's 64 bits the layout has allocated,
// reserved bits included.
func (l *Layout) Used() uint { return l.used }

// Field reads and writes one declared span of a word. The zero Field is not
// meaningful; obtain Fields from a Layout.
type Field struct {
	shift, width uint
}

// mask is the field's set bits, derived from the declared width — never
// written by hand, which is the point of the package.
func (f Field) mask() uint64 {
	return ^uint64(0) >> (64 - f.width)
}

// Get returns the field's value from w. The result is always within
// [0, f.Max()]: a Get cannot observe a neighbouring field's bits.
func (f Field) Get(w uint64) uint64 {
	return w >> f.shift & f.mask()
}

// Put returns w with the field set to v. The field is cleared first, so a
// second Put replaces rather than accumulates, and v is truncated to the
// field's width, so an oversized value cannot spill into the field above.
// Callers that must detect truncation compare v against Max before writing.
func (f Field) Put(w, v uint64) uint64 {
	return w&^(f.mask()<<f.shift) | v&f.mask()<<f.shift
}

// Max is the largest value the field can hold.
func (f Field) Max() uint64 { return f.mask() }

// Width is the declared width in bits.
func (f Field) Width() uint { return f.width }

// Shift is the field's offset from bit 0. Exposed for documentation and
// debugging; correct code never needs it, because Get and Put already apply it.
func (f Field) Shift() uint { return f.shift }
