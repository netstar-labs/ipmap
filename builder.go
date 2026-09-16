package ipmap

import (
	"fmt"
	"math"
	"net/netip"
	"slices"
)

// Builder accumulates (address, value) pairs and compiles them into a Map.
// Entries may arrive in any order and either family; Build sorts. A Builder is
// not safe for concurrent use, and is consumed by Build — reusing it afterwards
// panics rather than quietly building from stale state.
type Builder struct {
	opt   Options
	v4    []uint64 // address<<32 | value index: sorting the word sorts by address
	v6    []entry6
	vals  []byte // every added value, ValLen bytes each, in Add order
	built bool
}

// entry6 pairs a 128-bit address with the index of its value. The value index
// doubles as insertion order, which is what lets the duplicate rule — last
// added wins — survive sorting.
type entry6 struct {
	a  addr6
	vi uint32
}

// maxValLen bounds a value's width. The limit is arbitrary but a limit must
// exist: ValLen sizes every entry in the artifact, and a wild value here is a
// misconfiguration, not a use case.
const maxValLen = 4096

// NewBuilder returns a Builder for values of exactly opt.ValLen bytes. It
// panics if the options are structurally invalid — options are program
// structure, declared once, and a bad declaration must not survive init.
func NewBuilder(opt Options) *Builder {
	if opt.ValLen <= 0 || opt.ValLen > maxValLen {
		panic(fmt.Sprintf("ipmap: ValLen %d is not in 1..%d", opt.ValLen, maxValLen))
	}
	return &Builder{opt: opt}
}

// Add records one address and its value. val must be exactly ValLen bytes; it
// is copied, so the caller may reuse the slice. A 4-in-6 mapped address is
// unmapped first — ::ffff:192.0.2.1 and 192.0.2.1 are the same host, and
// storing them as two entries would make Lookup's answer depend on which
// spelling the caller used.
//
// Adding the same address twice is legal: the entry added last wins, and Build
// counts both the duplicates and how many of them carried a different value,
// because an input that disagrees with itself is worth knowing about.
func (b *Builder) Add(addr netip.Addr, val []byte) error {
	if b.built {
		panic("ipmap: Add after Build")
	}
	if !addr.IsValid() {
		return fmt.Errorf("ipmap: invalid address")
	}
	if len(val) != b.opt.ValLen {
		return fmt.Errorf("ipmap: value is %d bytes, ValLen is %d", len(val), b.opt.ValLen)
	}
	n := len(b.vals) / b.opt.ValLen
	if n > math.MaxUint32 {
		return fmt.Errorf("ipmap: too many entries")
	}
	vi := uint32(n)
	addr = addr.Unmap()
	if addr.Is4() {
		a4 := addr.As4()
		ip := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
		b.v4 = append(b.v4, uint64(ip)<<32|uint64(vi))
	} else {
		b.v6 = append(b.v6, entry6{a: addr6Of(addr), vi: vi})
	}
	b.vals = append(b.vals, val...)
	return nil
}

// Build compiles the accumulated entries into an immutable Map and consumes
// the Builder.
func (b *Builder) Build() (*Map, error) {
	if b.built {
		panic("ipmap: Build called twice")
	}
	b.built = true
	if b.opt.Intern {
		return nil, fmt.Errorf("ipmap: interning is not implemented yet")
	}

	m := &Map{valLen: b.opt.ValLen}
	b.build4(m)
	b.build6(m)

	// Release the builder's intermediates; the Map owns compact copies.
	b.v4, b.v6, b.vals = nil, nil, nil
	return m, nil
}

// build4 compiles the 32-bit store: a dense per-/24 index over suffix bytes.
// The high 24 bits of every address live in the index position, not in the
// entry — which is the whole design.
func (b *Builder) build4(m *Map) {
	slices.Sort(b.v4) // by address, then by value index: last-added sorts last

	// Dedupe keeping the last of each address run, counting what was dropped.
	kept := b.v4[:0]
	for i := 0; i < len(b.v4); i++ {
		if i+1 < len(b.v4) && b.v4[i+1]>>32 == b.v4[i]>>32 {
			m.stats.Dups++
			if !b.sameVal(uint32(b.v4[i]), uint32(b.v4[i+1])) {
				m.stats.DupConflicts++
			}
			continue
		}
		kept = append(kept, b.v4[i])
	}
	m.stats.Addrs4 = len(kept)
	if len(kept) == 0 {
		return // a map with no 32-bit entries does not pay 64 MB for their index
	}

	s := &m.s4
	s.valLen = b.opt.ValLen
	s.idx = make([]uint32, 1<<24+1)
	s.suffix = make([]uint8, len(kept))
	s.val = make([]byte, len(kept)*b.opt.ValLen)
	for _, e := range kept {
		s.idx[e>>40+1]++ // count entries per /24 (address's top 24 bits)
	}
	for i := 1; i < len(s.idx); i++ {
		s.idx[i] += s.idx[i-1]
	}
	cursor := make([]uint32, 1<<24)
	for _, e := range kept {
		g := e >> 40
		at := s.idx[g] + cursor[g]
		cursor[g]++
		s.suffix[at] = uint8(e >> 32)
		vi := uint32(e)
		copy(s.val[int(at)*b.opt.ValLen:], b.vals[int(vi)*b.opt.ValLen:int(vi+1)*b.opt.ValLen])
	}
}

// build6 compiles the 128-bit store: a sorted table of distinct 64-bit
// prefixes, each owning a run of low-word suffixes. The prefix is stored once
// per group rather than once per address.
func (b *Builder) build6(m *Map) {
	slices.SortFunc(b.v6, func(x, y entry6) int {
		switch {
		case x.a.less(y.a):
			return -1
		case y.a.less(x.a):
			return 1
		case x.vi < y.vi:
			return -1
		case x.vi > y.vi:
			return 1
		}
		return 0
	})

	kept := b.v6[:0]
	for i := 0; i < len(b.v6); i++ {
		if i+1 < len(b.v6) && b.v6[i+1].a == b.v6[i].a {
			m.stats.Dups++
			if !b.sameVal(b.v6[i].vi, b.v6[i+1].vi) {
				m.stats.DupConflicts++
			}
			continue
		}
		kept = append(kept, b.v6[i])
	}
	m.stats.Addrs6 = len(kept)

	s := &m.s6
	s.valLen = b.opt.ValLen
	s.suffix = make([]uint64, len(kept))
	s.val = make([]byte, len(kept)*b.opt.ValLen)
	for i, e := range kept {
		if i == 0 || kept[i-1].a.hi != e.a.hi {
			s.prefix = append(s.prefix, e.a.hi)
			s.pidx = append(s.pidx, uint32(i))
		}
		s.suffix[i] = e.a.lo
		copy(s.val[i*b.opt.ValLen:], b.vals[int(e.vi)*b.opt.ValLen:int(e.vi+1)*b.opt.ValLen])
	}
	s.pidx = append(s.pidx, uint32(len(kept)))
}

// sameVal reports whether two stored values are byte-equal.
func (b *Builder) sameVal(vi, vj uint32) bool {
	n := b.opt.ValLen
	x := b.vals[int(vi)*n : int(vi+1)*n]
	y := b.vals[int(vj)*n : int(vj+1)*n]
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
