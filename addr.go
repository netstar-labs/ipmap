package ipmap

import (
	"encoding/binary"
	"net/netip"
)

// The address model. Each family gets the representation its arithmetic wants:
// a 32-bit address is a uint32, where comparison and successor are single
// instructions and need no helper at all; a 128-bit address is two host-order
// words ordered by (hi, lo), so sorting and comparing never touch wide
// arithmetic.

// addr6 is a 128-bit address as two host-order words, high first. Ordering by
// (hi, lo) equals numeric address order, and both words fit machine registers.
type addr6 struct{ hi, lo uint64 }

// less reports whether a sorts before b numerically.
func (a addr6) less(b addr6) bool { return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo) }

// addr6Of converts from the standard library's form. The caller guarantees a
// is a valid 16-byte address; a 4-byte address must be mapped first.
func addr6Of(a netip.Addr) addr6 {
	b := a.As16()
	return addr6{hi: binary.BigEndian.Uint64(b[:8]), lo: binary.BigEndian.Uint64(b[8:])}
}
