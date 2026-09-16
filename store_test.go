package ipmap

import (
	"bytes"
	"math/rand/v2"
	"net/netip"
	"testing"
)

const testValLen = 5 // deliberately odd: a power of two hides stride bugs

// val derives a value from an address, so any test can know the right answer
// for any address without carrying a table around.
func val(a netip.Addr, salt byte) []byte {
	b := a.As16()
	out := make([]byte, testValLen)
	for i, c := range b {
		out[i%testValLen] ^= c + byte(i) + salt
	}
	out[0] |= 1 // never all-zero, so a zeroed buffer cannot pass as a hit
	return out
}

func v4addr(ip uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(ip >> 24), byte(ip >> 16), byte(ip >> 8), byte(ip)})
}

// oracle replays the same Adds into a Go map — an implementation with nothing
// in common with the store — normalising exactly as Add documents (Unmap),
// with map assignment supplying last-wins.
type oracle map[netip.Addr][]byte

func (o oracle) add(a netip.Addr, v []byte) { o[a.Unmap()] = v }

// buildRandom produces a dataset with the shapes that break stores: dense and
// sparse groups, both families, duplicate addresses with agreeing and
// conflicting values.
func buildRandom(t testing.TB, seed uint64, n int) (*Map, oracle) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed^0xdead))
	b := NewBuilder(Options{ValLen: testValLen})
	o := oracle{}

	add := func(a netip.Addr, v []byte) {
		if err := b.Add(a, v); err != nil {
			t.Fatalf("Add(%v): %v", a, err)
		}
		o.add(a, v)
	}

	// 32-bit: clusters within shared /24s plus lone addresses.
	for i := 0; i < n; i++ {
		if r.IntN(3) == 0 { // a cluster
			base := r.Uint32() &^ 0xff
			for k, kn := 0, 1+r.IntN(40); k < kn; k++ {
				a := v4addr(base | uint32(r.IntN(256)))
				add(a, val(a, 0))
			}
		} else {
			a := v4addr(r.Uint32())
			add(a, val(a, 0))
		}
	}
	// 128-bit: several suffixes under shared high words plus lone addresses.
	for i := 0; i < n; i++ {
		hi := r.Uint64()
		for k, kn := 0, 1+r.IntN(6); k < kn; k++ {
			a := addr6{hi: hi, lo: r.Uint64()}.addr()
			add(a, val(a, 0))
		}
	}
	// Duplicates: re-add a sample with the same value, and a sample with a
	// conflicting one; the map oracle applies last-wins by assignment.
	keys := make([]netip.Addr, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys)/10; i++ {
		k := keys[r.IntN(len(keys))]
		add(k, o[k]) // same value again
	}
	for i := 0; i < len(keys)/10; i++ {
		k := keys[r.IntN(len(keys))]
		add(k, val(k, 7)) // a conflicting value; last-wins makes it the answer
	}

	m, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m, o
}

// The exhaustive oracle: every key the oracle knows must resolve to exactly the
// oracle's value — all of them, not a sample. A sampled check once passed a
// store that was wrong for 45 keys in 10^8.
func TestLookupExhaustive(t *testing.T) {
	m, o := buildRandom(t, 1, 3000)
	if m.Stats().Addrs4+m.Stats().Addrs6 != len(o) {
		t.Fatalf("stored %d+%d addresses, oracle has %d",
			m.Stats().Addrs4, m.Stats().Addrs6, len(o))
	}
	for k, want := range o {
		got, ok := m.Lookup(k)
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("Lookup(%v) = %x,%v; want %x", k, got, ok, want)
		}
		var into [testValLen]byte
		if !m.LookupInto(k, into[:]) || !bytes.Equal(into[:], want) {
			t.Fatalf("LookupInto(%v) = %x; want %x", k, into, want)
		}
	}
}

// Misses must miss: addresses the oracle does not know return false, for both
// families, across many random probes.
func TestLookupMisses(t *testing.T) {
	m, o := buildRandom(t, 2, 2000)
	r := rand.New(rand.NewPCG(9, 9))
	for i := 0; i < 200000; i++ {
		var a netip.Addr
		if i%2 == 0 {
			a = v4addr(r.Uint32())
		} else {
			a = addr6{hi: r.Uint64(), lo: r.Uint64()}.addr()
		}
		if _, known := o[a]; known {
			continue
		}
		if v, ok := m.Lookup(a); ok {
			t.Fatalf("Lookup(%v) hit with %x; the oracle has no such address", a, v)
		}
	}
}

// The boundary set (T3): the addresses that break off-by-one arithmetic, each
// verified present or absent as constructed.
func TestBoundaryAddresses(t *testing.T) {
	b := NewBuilder(Options{ValLen: testValLen})
	o := oracle{}
	add := func(a netip.Addr) {
		if err := b.Add(a, val(a, 0)); err != nil {
			t.Fatal(err)
		}
		o.add(a, val(a, 0))
	}

	add(v4addr(0x00000000))    // the first address: group 0
	add(v4addr(0xffffffff))    // the last: group 2^24-1, its final slot
	add(v4addr(0x00000001))    // beside the first
	for i := 0; i < 256; i++ { // one completely full /24
		add(v4addr(0x0a0b0c00 | uint32(i)))
	}
	add(v4addr(0x0a0b0e01)) // single-entry group with empty groups both sides

	add(addr6{0, 0}.addr())                         // ::
	add(addr6{^uint64(0), ^uint64(0)}.addr())       // the very last address
	add(addr6{0x20010db8 << 32, 0}.addr())          // a group's lowest suffix
	add(addr6{0x20010db8 << 32, ^uint64(0)}.addr()) // and its highest
	add(addr6{0x20010db8 << 32, 5}.addr())

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range o {
		if got, ok := m.Lookup(k); !ok || !bytes.Equal(got, want) {
			t.Fatalf("boundary %v: got %x,%v", k, got, ok)
		}
	}
	// The near-misses around every boundary: each must miss.
	for _, a := range []netip.Addr{
		v4addr(0x00000002), v4addr(0xfffffffe), // beside stored extremes
		v4addr(0x0a0b0bff), v4addr(0x0a0b0d00), // groups adjacent to the full /24
		v4addr(0x0a0b0e00), v4addr(0x0a0b0e02), // inside the single-entry group
		addr6{0, 1}.addr(),                       // beside ::
		addr6{^uint64(0), ^uint64(0) - 1}.addr(), // beside the last
		addr6{0x20010db8<<32 + 1, 5}.addr(),      // hi just above a known group
		addr6{0x20010db8<<32 - 1, 5}.addr(),      // hi just below
		addr6{0x20010db8 << 32, 4}.addr(),        // lo in-gap within the group
		addr6{0x20010db8 << 32, 6}.addr(),
	} {
		if _, known := o[a]; known {
			continue
		}
		if _, ok := m.Lookup(a); ok {
			t.Fatalf("near-miss %v answered", a)
		}
	}
}

// The duplicate rule (T4): last wins, deterministically; both counters count;
// and a 4-in-6 spelling collides with its unmapped twin, because they are the
// same host.
func TestDuplicateRule(t *testing.T) {
	b := NewBuilder(Options{ValLen: 1})
	a := netip.MustParseAddr("192.0.2.7")
	mapped := netip.MustParseAddr("::ffff:192.0.2.7")
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(b.Add(a, []byte{1}))
	check(b.Add(a, []byte{1}))      // dup, same value
	check(b.Add(mapped, []byte{2})) // dup via the other spelling, conflicting
	check(b.Add(a, []byte{3}))      // dup, conflicting; added last — must win

	six := netip.MustParseAddr("2001:db8::9")
	check(b.Add(six, []byte{7}))
	check(b.Add(six, []byte{8})) // conflicting dup in the 128-bit family

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Lookup(a); !bytes.Equal(got, []byte{3}) {
		t.Fatalf("last-wins broken: %x", got)
	}
	if got, _ := m.Lookup(mapped); !bytes.Equal(got, []byte{3}) {
		t.Fatalf("the mapped spelling answers differently: %x", got)
	}
	if got, _ := m.Lookup(six); !bytes.Equal(got, []byte{8}) {
		t.Fatalf("128-bit last-wins broken: %x", got)
	}
	st := m.Stats()
	if st.Addrs4 != 1 || st.Addrs6 != 1 {
		t.Fatalf("addrs = %d/%d, want 1/1", st.Addrs4, st.Addrs6)
	}
	// Dropped: three of the four spellings of a, one of the two adds of six.
	if st.Dups != 4 {
		t.Fatalf("Dups = %d, want 4", st.Dups)
	}
	// Conflicts: 1→1 agrees; 1→2 and 2→3 differ; 7→8 differs.
	if st.DupConflicts != 3 {
		t.Fatalf("DupConflicts = %d, want 3", st.DupConflicts)
	}
}

// LookupInto documents that a short dst reads as a miss rather than a panic,
// and ValLen is how a caller sizes dst correctly; both halves of that contract
// get checked here.
func TestLookupIntoSizing(t *testing.T) {
	m, o := buildRandom(t, 6, 100)
	if m.ValLen() != testValLen {
		t.Fatalf("ValLen = %d, want %d", m.ValLen(), testValLen)
	}
	for k := range o {
		short := make([]byte, m.ValLen()-1)
		if m.LookupInto(k, short) {
			t.Fatal("a short dst reported success")
		}
		exact := make([]byte, m.ValLen())
		if !m.LookupInto(k, exact) {
			t.Fatal("an exact dst missed")
		}
		break
	}
}

func TestEmptyAndSingleFamilyMaps(t *testing.T) {
	m, err := NewBuilder(Options{ValLen: 2}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup(netip.MustParseAddr("1.2.3.4")); ok {
		t.Fatal("empty map answered a 32-bit lookup")
	}
	if _, ok := m.Lookup(netip.MustParseAddr("::1")); ok {
		t.Fatal("empty map answered a 128-bit lookup")
	}

	b := NewBuilder(Options{ValLen: 2})
	if err := b.Add(netip.MustParseAddr("2001:db8::1"), []byte{9, 9}); err != nil {
		t.Fatal(err)
	}
	m6, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m6.Lookup(netip.MustParseAddr("1.2.3.4")); ok {
		t.Fatal("a map with no 32-bit entries answered one")
	}
	if m6.s4.idx != nil {
		t.Fatal("a map with no 32-bit entries still allocated the 64 MB index")
	}
}

// Misuse dies loudly at the boundary where it happens.
func TestBuilderMisuse(t *testing.T) {
	b := NewBuilder(Options{ValLen: 2})
	if err := b.Add(netip.Addr{}, []byte{1, 2}); err == nil {
		t.Fatal("invalid address accepted")
	}
	if err := b.Add(netip.MustParseAddr("1.2.3.4"), []byte{1}); err == nil {
		t.Fatal("wrong-width value accepted")
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Add after Build did not panic")
			}
		}()
		_ = b.Add(netip.MustParseAddr("1.2.3.4"), []byte{1, 2})
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("second Build did not panic")
			}
		}()
		_, _ = b.Build()
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("ValLen 0 did not panic")
			}
		}()
		NewBuilder(Options{ValLen: 0})
	}()
	if _, err := NewBuilder(Options{ValLen: 2, Intern: true}).Build(); err == nil {
		t.Fatal("Intern silently ignored; it must error until implemented")
	}
}

// A value returned by Lookup must be unaffected by what the caller does to a
// LookupInto buffer, and capped so an append cannot write into the store.
func TestLookupAliasing(t *testing.T) {
	m, o := buildRandom(t, 3, 200)
	for k, want := range o {
		v, _ := m.Lookup(k)
		if cap(v) != len(v) {
			t.Fatalf("Lookup slice not capacity-capped: len %d cap %d", len(v), cap(v))
		}
		var dst [testValLen]byte
		m.LookupInto(k, dst[:])
		for i := range dst {
			dst[i] = 0xEE
		}
		if got, _ := m.Lookup(k); !bytes.Equal(got, want) {
			t.Fatal("scribbling a LookupInto buffer changed the store")
		}
		break
	}
}

// Lookups run on hot paths millions of times; hold them to zero allocations.
func TestLookupZeroAlloc(t *testing.T) {
	m, o := buildRandom(t, 4, 500)
	var k4, k6 netip.Addr
	for k := range o {
		if k.Is4() {
			k4 = k
		} else {
			k6 = k
		}
		if k4.IsValid() && k6.IsValid() {
			break
		}
	}
	var dst [testValLen]byte
	for name, a := range map[string]netip.Addr{
		"hit4": k4, "hit6": k6,
		"miss4": v4addr(12345), "miss6": addr6{hi: 1, lo: 2}.addr(),
	} {
		if n := testing.AllocsPerRun(1000, func() {
			m.Lookup(a)
			m.LookupInto(a, dst[:])
		}); n != 0 {
			t.Fatalf("%s allocates: %.1f allocs/op", name, n)
		}
	}
}

func benchStore(b *testing.B) (*Map, []netip.Addr, []netip.Addr) {
	m, o := buildRandom(b, 42, 20000)
	hits := make([]netip.Addr, 0, len(o))
	for k := range o {
		hits = append(hits, k)
	}
	r := rand.New(rand.NewPCG(1, 2))
	misses := make([]netip.Addr, len(hits))
	for i := range misses {
		if i%2 == 0 {
			misses[i] = v4addr(r.Uint32())
		} else {
			misses[i] = addr6{hi: r.Uint64(), lo: r.Uint64()}.addr()
		}
	}
	return m, hits, misses
}

func BenchmarkLookupHit(b *testing.B) {
	m, hits, _ := benchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		m.Lookup(hits[i%len(hits)])
	}
}

func BenchmarkLookupMiss(b *testing.B) {
	m, _, misses := benchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		m.Lookup(misses[i%len(misses)])
	}
}
