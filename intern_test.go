package ipmap

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"testing"
)

// The phase's core criterion: the two build modes are observationally
// identical. Same adds, both modes, exhaustive agreement on every key — and on
// misses, since a broken id could resolve a miss into somebody else's value.
func TestInternedMatchesUninterned(t *testing.T) {
	distinct := map[string]bool{}
	direct, o := buildRandomOpt(t, 11, 3000, Options{ValLen: testValLen},
		func(_ netip.Addr, v []byte) { distinct[string(v)] = true })
	interned, o2 := buildRandomOpt(t, 11, 3000, Options{ValLen: testValLen, Intern: true})
	if len(o) != len(o2) {
		t.Fatalf("the two runs generated different data: %d vs %d keys", len(o), len(o2))
	}
	for k, want := range o {
		dv, dok := direct.Lookup(k)
		iv, iok := interned.Lookup(k)
		if !dok || !iok || !bytes.Equal(dv, iv) || !bytes.Equal(dv, want) {
			t.Fatalf("modes disagree at %v: direct %x,%v interned %x,%v want %x", k, dv, dok, iv, iok, want)
		}
	}
	r := rand.New(rand.NewPCG(21, 22))
	for i := 0; i < 100000; i++ {
		var a netip.Addr
		if i%2 == 0 {
			a = v4addr(r.Uint32())
		} else {
			a = addr6{hi: r.Uint64(), lo: r.Uint64()}.addr()
		}
		if _, known := o[a]; known {
			continue
		}
		_, dok := direct.Lookup(a)
		_, iok := interned.Lookup(a)
		if dok || iok {
			t.Fatalf("a miss answered at %v: direct=%v interned=%v", a, dok, iok)
		}
	}
	// Distinct counts every distinct value ever added — including values whose
	// only entries were later superseded by last-wins, which stay in the table.
	// An independent count over the identical add stream must agree exactly.
	if d := interned.Stats().Distinct; d != len(distinct) {
		t.Fatalf("Distinct = %d, independent count = %d", d, len(distinct))
	}
	if d := direct.Stats().Distinct; d != 0 {
		t.Fatalf("direct mode reports Distinct = %d, want 0", d)
	}
}

// The width boundaries: 256 distinct ids fit one byte because ids run 0..n-1.
func TestIDWidth(t *testing.T) {
	for _, tc := range []struct{ n, w int }{
		{1, 1}, {255, 1}, {256, 1},
		{257, 2}, {65535, 2}, {65536, 2},
		{65537, 3}, {1 << 24, 3},
		{1<<24 + 1, 4},
	} {
		if got := idWidth(tc.n); got != tc.w {
			t.Errorf("idWidth(%d) = %d, want %d", tc.n, got, tc.w)
		}
	}
}

// Every id width gets an end-to-end build whose distinct count forces it, and
// an exhaustive check on the result. Width 4 would need >16.7M distinct values
// — out of unit-test range, so its packing is covered by the putID/get pair
// below and the width never asserts falsely by construction.
func TestInternAcrossIDWidths(t *testing.T) {
	for _, distinct := range []int{200, 60000, 70000} {
		t.Run(fmt.Sprintf("distinct=%d", distinct), func(t *testing.T) {
			if testing.Short() && distinct > 1000 {
				t.Skip("short mode")
			}
			b := NewBuilder(Options{ValLen: 3, Intern: true})
			type kv struct {
				a netip.Addr
				v []byte
			}
			var want []kv
			r := rand.New(rand.NewPCG(uint64(distinct), 5))
			for i := 0; i < distinct*3; i++ {
				id := i % distinct // three addresses share each value
				v := []byte{byte(id), byte(id >> 8), byte(id >> 16)}
				a := v4addr(r.Uint32())
				if i%5 == 0 {
					a = addr6{hi: r.Uint64(), lo: r.Uint64()}.addr()
				}
				if err := b.Add(a, v); err != nil {
					t.Fatal(err)
				}
				want = append(want, kv{a.Unmap(), v})
			}
			m, err := b.Build()
			if err != nil {
				t.Fatal(err)
			}
			if m.Stats().Distinct != distinct {
				t.Fatalf("Distinct = %d, want %d", m.Stats().Distinct, distinct)
			}
			if w := idWidth(distinct); (w == 1) != (distinct <= 256) || (w == 3) != (distinct > 65536) {
				t.Fatalf("test setup no longer forces the width it says it does")
			}
			// last-wins on random collisions: replay into a map for the truth
			truth := map[netip.Addr][]byte{}
			for _, e := range want {
				truth[e.a] = e.v
			}
			for a, v := range truth {
				got, ok := m.Lookup(a)
				if !ok || !bytes.Equal(got, v) {
					t.Fatalf("Lookup(%v) = %x,%v; want %x", a, got, ok, v)
				}
			}
		})
	}
}

// An id wider than the derived width is a builder bug and must die at build,
// not corrupt a neighbouring entry and surface as a wrong answer at query.
func TestPutIDOverflowPanics(t *testing.T) {
	v := values{valLen: 1, idW: 1, ids: make([]byte, 8), tab: make([]byte, 300)}
	defer func() {
		if recover() == nil {
			t.Fatal("an id past the derived width did not panic")
		}
	}()
	v.putID(0, 256)
}

// The packed-id round trip at every width, including 4 — the width the
// end-to-end tests cannot reach.
func TestPutIDGetRoundTrip(t *testing.T) {
	for w := 1; w <= 4; w++ {
		n := 40
		v := values{valLen: 2, idW: w, ids: make([]byte, n*w)}
		maxID := uint32(1)<<(8*w) - 1
		if w == 4 {
			maxID = 1<<32 - 1
		}
		// build a table big enough for the ids we place at the extremes
		ids := []uint32{0, 1, maxID / 2, maxID - 1, maxID}
		v.tab = make([]byte, (int(maxID)+1)*2)
		if w == 4 {
			// a 8.5 GB table is not a unit test; place small ids plus one probe
			ids = []uint32{0, 1, 2, 3, 700000}
			v.tab = make([]byte, (700000+1)*2)
		}
		for i, id := range ids {
			v.tab[int(id)*2] = byte(id)
			v.tab[int(id)*2+1] = byte(id >> 16)
			v.putID(i, id)
		}
		for i, id := range ids {
			got := v.get(i)
			if got[0] != byte(id) || got[1] != byte(id>>16) {
				t.Fatalf("width %d id %d: read back the wrong table row", w, id)
			}
		}
	}
}

// Interned lookups stay allocation-free.
func TestInternZeroAlloc(t *testing.T) {
	m, o := buildRandomOpt(t, 12, 500, Options{ValLen: testValLen, Intern: true})
	var k netip.Addr
	for k = range o {
		break
	}
	var dst [testValLen]byte
	if n := testing.AllocsPerRun(1000, func() {
		m.Lookup(k)
		m.LookupInto(k, dst[:])
	}); n != 0 {
		t.Fatalf("interned lookup allocates: %.1f allocs/op", n)
	}
}
