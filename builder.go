package ipmap

import (
	"bytes"
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
	v4    []uint64 // address<<32 | sequence: sorting the word sorts by address
	v6    []entry6
	n     int // entries added; the per-entry sequence number
	built bool

	// direct mode: every added value, ValLen bytes each, in Add order.
	vals []byte

	// interned mode: the distinct values, the id of each, and the id added at
	// each sequence number. The sequence stays separate from the value id —
	// conflating them would break last-wins, which resolves by insertion order.
	tab   []byte
	byVal map[string]uint32
	ids   []uint32
}

// entry6 pairs a 128-bit address with its sequence number — insertion order,
// which is what lets the duplicate rule (last added wins) survive sorting.
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
	b := &Builder{opt: opt}
	if opt.Intern {
		b.byVal = make(map[string]uint32)
	}
	return b
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
//
// A zoned address (fe80::1%eth0) is refused: the artifact stores hosts, and a
// zone identifies a link on one machine — see Map.Lookup, which will not
// answer for one either.
//
// A Builder holds at most 2³²−1 entries — a single budget across both
// families, because the artifact's offsets are 32-bit. Add reports an error at
// the cap rather than wrapping.
//
// Add panics only on misuse of the Builder itself: calling it after Build.
func (b *Builder) Add(addr netip.Addr, val []byte) error {
	if b.built {
		panic("ipmap: Add after Build")
	}
	if !addr.IsValid() {
		return fmt.Errorf("ipmap: invalid address")
	}
	// A zone names a link on one machine, not a host, and the artifact has
	// nowhere to put it: storing fe80::1%eth0 would store it as fe80::1, so
	// two interfaces' addresses would silently become one entry and a lookup
	// of either would answer with whichever was added last. Refuse instead.
	if addr.Zone() != "" {
		return fmt.Errorf("ipmap: address carries a zone (%q): a zone identifies a link, not a host", addr.Zone())
	}
	if len(val) != b.opt.ValLen {
		return fmt.Errorf("ipmap: value is %d bytes, ValLen is %d", len(val), b.opt.ValLen)
	}
	// >= not >: the entry count itself must fit uint32 — the stores' offset
	// arrays end with a sentinel equal to the count. Off by one, the 2^32-th
	// entry wraps that sentinel to zero: a silent universal miss in the 32-bit
	// family, a slice panic in the 128-bit one.
	if b.n >= math.MaxUint32 {
		return fmt.Errorf("ipmap: too many entries")
	}
	vi := uint32(b.n)
	addr = addr.Unmap()
	if addr.Is4() {
		a4 := addr.As4()
		ip := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
		b.v4 = append(b.v4, uint64(ip)<<32|uint64(vi))
	} else {
		b.v6 = append(b.v6, entry6{a: addr6Of(addr), vi: vi})
	}
	if b.opt.Intern {
		id, ok := b.byVal[string(val)] // a hit does not allocate the string
		if !ok {
			id = uint32(len(b.byVal))
			b.byVal[string(val)] = id
			b.tab = append(b.tab, val...)
		}
		b.ids = append(b.ids, id)
	} else {
		b.vals = append(b.vals, val...)
	}
	b.n++
	return nil
}

// Build compiles the accumulated entries into an immutable Map and consumes
// the Builder.
func (b *Builder) Build() (*Map, error) {
	if b.built {
		panic("ipmap: Build called twice")
	}
	b.built = true

	epoch := nowEpoch()
	if !b.opt.Epoch.IsZero() {
		epoch = b.opt.Epoch.Unix()
	}
	m := &Map{valLen: b.opt.ValLen, epoch: epoch}
	if b.opt.Intern {
		m.stats.Distinct = len(b.byVal)
	}
	b.build4(m)
	b.build6(m)

	// Release the builder's intermediates; the Map owns compact copies.
	b.v4, b.v6, b.vals, b.byVal, b.ids = nil, nil, nil, nil, nil
	return m, nil
}

// newValues prepares a store's value layout for n entries: direct when
// interning is off, otherwise the shared table plus a packed-id array whose
// width is derived from the distinct count — and asserted on every write.
func (b *Builder) newValues(n int) values {
	v := values{valLen: b.opt.ValLen}
	if !b.opt.Intern {
		v.direct = make([]byte, n*b.opt.ValLen)
		return v
	}
	v.tab = b.tab
	v.idW = idWidth(len(b.byVal))
	v.ids = make([]byte, n*v.idW)
	return v
}

// setVal writes the value for sequence number seq into slot at.
func (b *Builder) setVal(v *values, at int, seq uint32) {
	if b.opt.Intern {
		v.putID(at, b.ids[seq])
		return
	}
	copy(v.direct[at*b.opt.ValLen:], b.vals[int(seq)*b.opt.ValLen:int(seq+1)*b.opt.ValLen])
}

// build4 compiles the 32-bit store: a dense per-/24 index over suffix bytes.
// The high 24 bits of every address live in the index position, not in the
// entry — which is the whole design.
func (b *Builder) build4(m *Map) {
	slices.Sort(b.v4) // by address, then by value index: last-added sorts last

	// Dedupe keeping the last of each address run, counting what was dropped.
	// Conflicts are measured against the run's survivor, not the next entry in
	// line — DupConflicts answers "how many drops lost information", and only
	// the survivor's value is kept.
	kept := b.v4[:0]
	for i := 0; i < len(b.v4); {
		j := i // j walks to the last entry of this address's run: the survivor
		for j+1 < len(b.v4) && b.v4[j+1]>>32 == b.v4[j]>>32 {
			j++
		}
		for k := i; k < j; k++ {
			m.stats.Dups++
			if !b.sameVal(uint32(b.v4[k]), uint32(b.v4[j])) {
				m.stats.DupConflicts++
			}
		}
		kept = append(kept, b.v4[j])
		i = j + 1
	}
	m.stats.Addrs4 = len(kept)
	if len(kept) == 0 {
		return // a map with no 32-bit entries does not pay 67 MB for their index
	}

	s := &m.s4
	s.idx = make([]uint32, 1<<24+1)
	s.suffix = make([]uint8, len(kept))
	s.vals = b.newValues(len(kept))
	for _, e := range kept {
		s.idx[e>>40+1]++ // count entries per /24 (address's top 24 bits)
	}
	for i := 1; i < len(s.idx); i++ {
		s.idx[i] += s.idx[i-1]
	}
	// kept is sorted, so entry i's slot in its group is exactly i: the prefix
	// sum already places every group's run contiguously in address order. No
	// per-group cursor is needed — that is scatter machinery for unsorted input.
	for i, e := range kept {
		s.suffix[i] = uint8(e >> 32)
		b.setVal(&s.vals, i, uint32(e))
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

	// Same run-based dedupe as build4: conflicts count against the survivor.
	kept := b.v6[:0]
	for i := 0; i < len(b.v6); {
		j := i
		for j+1 < len(b.v6) && b.v6[j+1].a == b.v6[j].a {
			j++
		}
		for k := i; k < j; k++ {
			m.stats.Dups++
			if !b.sameVal(b.v6[k].vi, b.v6[j].vi) {
				m.stats.DupConflicts++
			}
		}
		kept = append(kept, b.v6[j])
		i = j + 1
	}
	m.stats.Addrs6 = len(kept)

	s := &m.s6
	s.suffix = make([]uint64, len(kept))
	s.vals = b.newValues(len(kept))
	for i, e := range kept {
		if i == 0 || kept[i-1].a.hi != e.a.hi {
			s.prefix = append(s.prefix, e.a.hi)
			s.pidx = append(s.pidx, uint32(i))
		}
		s.suffix[i] = e.a.lo
		b.setVal(&s.vals, i, e.vi)
	}
	s.pidx = append(s.pidx, uint32(len(kept)))
}

// sameVal reports whether the values added at two sequence numbers are equal.
// Interned mode answers by id — equal bytes intern to one id, so identity is
// equality — and direct mode compares the bytes.
func (b *Builder) sameVal(vi, vj uint32) bool {
	if b.opt.Intern {
		return b.ids[vi] == b.ids[vj]
	}
	n := b.opt.ValLen
	return bytes.Equal(b.vals[int(vi)*n:int(vi+1)*n], b.vals[int(vj)*n:int(vj+1)*n])
}
