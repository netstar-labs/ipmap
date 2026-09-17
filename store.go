package ipmap

import (
	"net/netip"
	"sort"
)

// Map answers lookups against a built set of addresses. It is immutable: any
// number of goroutines may query one Map concurrently with no synchronisation.
// To change the data, build a new Map and swap the pointer.
type Map struct {
	s4     store4
	s6     store6
	valLen int
	epoch  int64 // unix seconds; set by Build, carried by the artifact
	stats  Stats
	close  func() error // releases the mapping; nil for a built Map
}

// Stats describes what a build produced. Duplicates are reported rather than
// silently resolved, because an input that names one address twice with two
// different values is disagreeing with itself — and the caller, not this
// library, knows whether that is expected.
type Stats struct {
	Addrs4       int // distinct 32-bit addresses stored
	Addrs6       int // distinct 128-bit addresses stored
	Dups         int // entries dropped by the last-wins rule
	DupConflicts int // ...of which carried a different value than the survivor
	// Distinct is the interned value table's row count — every distinct value
	// ever added, including values whose only entries were later superseded by
	// the last-wins rule (they stay in the table; compaction would buy little
	// on real feeds, where duplicates are rare). Zero when interning is off.
	Distinct int
}

// Stats reports the build's counts.
func (m *Map) Stats() Stats { return m.stats }

// ValLen is the width in bytes of every stored value.
func (m *Map) ValLen() int { return m.valLen }

// Lookup returns the value stored against addr. The returned slice aliases the
// Map's storage: it is valid for the Map's lifetime and must not be modified —
// use LookupInto for an owned copy. A 4-in-6 mapped address is unmapped first,
// matching Add, so both spellings of a host answer identically.
func (m *Map) Lookup(addr netip.Addr) ([]byte, bool) {
	if !addr.IsValid() {
		return nil, false
	}
	addr = addr.Unmap()
	if addr.Is4() {
		b := addr.As4()
		return m.s4.lookup(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
	}
	return m.s6.lookup(addr6Of(addr))
}

// LookupInto copies addr's value into dst and reports whether it was found.
// dst must be at least ValLen bytes; a shorter dst reports false rather than
// panicking, so a sizing mistake reads as a miss under test instead of a crash
// in production.
func (m *Map) LookupInto(addr netip.Addr, dst []byte) bool {
	if len(dst) < m.valLen {
		return false
	}
	v, ok := m.Lookup(addr)
	if ok {
		copy(dst, v)
	}
	return ok
}

// store4 holds the 32-bit family: a dense index of every /24, a suffix byte
// per entry, and the values. The top 24 bits of each address are implicit in
// the index position — the design's whole idea.
type store4 struct {
	idx    []uint32 // 2^24+1 offsets; group g's entries are [idx[g], idx[g+1])
	suffix []uint8  // low octet, ascending within a group
	vals   values   // entry position -> value bytes, direct or interned
}

func (s *store4) lookup(ip uint32) ([]byte, bool) {
	if s.idx == nil {
		return nil, false
	}
	lo, hi := s.idx[ip>>8], s.idx[ip>>8+1]
	want := uint8(ip)
	// The run is sorted and at most 256 long — typically a cache line — so a
	// linear scan with an early exit beats a binary search's branching here.
	for j := lo; j < hi; j++ {
		if s.suffix[j] == want {
			return s.vals.get(int(j)), true
		}
		if s.suffix[j] > want {
			break
		}
	}
	return nil, false
}

// store6 holds the 128-bit family: a sorted table of the distinct high words,
// each owning a run of low-word suffixes. The high word is stored once per
// group rather than once per address; 2^64 admits no dense index, so the group
// is found by binary search instead of position.
type store6 struct {
	prefix []uint64 // distinct high words, ascending
	pidx   []uint32 // len(prefix)+1 offsets into suffix
	suffix []uint64 // low words, ascending within a group
	vals   values
}

func (s *store6) lookup(a addr6) ([]byte, bool) {
	i := sort.Search(len(s.prefix), func(i int) bool { return s.prefix[i] >= a.hi })
	if i == len(s.prefix) || s.prefix[i] != a.hi {
		return nil, false
	}
	lo, hi := s.pidx[i], s.pidx[i+1]
	seg := s.suffix[lo:hi]
	j := sort.Search(len(seg), func(j int) bool { return seg[j] >= a.lo })
	if j == len(seg) || seg[j] != a.lo {
		return nil, false
	}
	return s.vals.get(int(lo) + j), true
}
