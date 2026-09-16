package ipmap

import (
	"math/rand/v2"
	"net/netip"
	"strings"
	"testing"
)

// want6 converts through the standard library, which is the oracle for every
// test in this file: expectations are derived, never hand-written (a test
// expectation can be wrong while the code is right).
func want6(a netip.Addr) addr6 {
	b := a.As16()
	var w addr6
	for i := 0; i < 8; i++ {
		w.hi = w.hi<<8 | uint64(b[i])
		w.lo = w.lo<<8 | uint64(b[8+i])
	}
	return w
}

func want4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// rand16 produces addresses with a bias toward zero bytes so "::" compression
// and the v4-mapped rendering are exercised hard, not occasionally.
func rand16(r *rand.Rand) [16]byte {
	var b [16]byte
	zeroBias := r.IntN(4)
	for i := range b {
		if zeroBias > 0 && r.IntN(zeroBias+1) != 0 {
			continue
		}
		b[i] = byte(r.Uint64())
	}
	return b
}

// Differential parse: for 100k+ canonical strings per family, byte-identical
// agreement with net/netip.
func TestParseAddrMatchesNetip(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 9))
	for i := 0; i < 150000; i++ {
		a := netip.AddrFrom16(rand16(r))
		s := a.String()
		got, ok := parseAddr6([]byte(s))
		if !ok {
			t.Fatalf("parseAddr6(%q) rejected a canonical address", s)
		}
		if w := want6(a); got != w {
			t.Fatalf("parseAddr6(%q)\n got  %016x %016x\n want %016x %016x", s, got.hi, got.lo, w.hi, w.lo)
		}
	}
	for i := 0; i < 150000; i++ {
		v := r.Uint32()
		a := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
		s := a.String()
		got, ok := parseAddr4([]byte(s))
		if !ok {
			t.Fatalf("parseAddr4(%q) rejected a canonical address", s)
		}
		if got != v {
			t.Fatalf("parseAddr4(%q) = %08x, want %08x", s, got, v)
		}
	}
}

// The forms that must be refused: each is checked against the oracle too, so
// the case list cannot drift into rejecting something netip accepts.
func TestParseAddrRejects(t *testing.T) {
	reject4 := []string{
		"", ".", "1.2.3", "1.2.3.4.5", "1.2.3.256", "1.2.3.4.", ".1.2.3.4",
		"1..2.3", "1.2.3.04", "01.2.3.4", "1.2.3.4 ", " 1.2.3.4", "1.2.3.a",
		"1.2.3.1234", "-1.2.3.4",
	}
	for _, s := range reject4 {
		if _, ok := parseAddr4([]byte(s)); ok {
			t.Errorf("parseAddr4(%q) accepted", s)
		}
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			t.Errorf("case list is wrong: netip accepts %q", s)
		}
	}
	reject6 := []string{
		"", ":", ":::", "1:2:3:4:5:6:7", "1:2:3:4:5:6:7:8:9", "1::2::3",
		"12345::", "g::1", "1:2:3:4:5:6:7:", "::1:", "1.2.3.4",
		"fe80::1%eth0", "1:2:3:4:5:6:1.2.3.4.5", "::ffff:1.2.3.04",
		"::ffff:1.2.3.256", "1:2:3:4:5:6:7:1.2.3.4", "::00001",
	}
	for _, s := range reject6 {
		if _, ok := parseAddr6([]byte(s)); ok {
			t.Errorf("parseAddr6(%q) accepted", s)
		}
		if a, err := netip.ParseAddr(s); err == nil && a.Is6() && a.Zone() == "" {
			t.Errorf("case list is wrong: netip accepts %q", s)
		}
	}
}

// Ordering by the internal representation must equal numeric address order —
// the whole store depends on sorting by it.
func TestOrderMatchesNetip(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 11))
	for i := 0; i < 150000; i++ {
		x, y := netip.AddrFrom16(rand16(r)), netip.AddrFrom16(rand16(r))
		if want6(x).less(want6(y)) != x.Less(y) {
			t.Fatalf("addr6 order disagrees with netip for %v vs %v", x, y)
		}
	}
	// The 32-bit model is a bare uint32; its order is checked once against the
	// oracle so the claim "native compare is numeric order" is tested, not said.
	for i := 0; i < 150000; i++ {
		vx, vy := r.Uint32(), r.Uint32()
		x := netip.AddrFrom4([4]byte{byte(vx >> 24), byte(vx >> 16), byte(vx >> 8), byte(vx)})
		y := netip.AddrFrom4([4]byte{byte(vy >> 24), byte(vy >> 16), byte(vy >> 8), byte(vy)})
		if (vx < vy) != x.Less(y) {
			t.Fatalf("uint32 order disagrees with netip for %v vs %v", x, y)
		}
	}
}

// Successor must agree with the oracle everywhere, and carry across the
// internal 64-bit boundary in particular.
func TestNextMatchesNetip(t *testing.T) {
	cases := []string{
		"::", "::1", "::ffff:ffff:ffff:ffff", // carries into the high word
		"0:0:0:1::", "2001:db8::ffff", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe",
	}
	for _, s := range cases {
		a := netip.MustParseAddr(s)
		if got, want := want6(a).next(), want6(a.Next()); got != want {
			t.Errorf("%s: next = %016x %016x, want %016x %016x", s, got.hi, got.lo, want.hi, want.lo)
		}
	}
	r := rand.New(rand.NewPCG(5, 13))
	for i := 0; i < 100000; i++ {
		a := netip.AddrFrom16(rand16(r))
		if !a.Next().IsValid() {
			continue // the top of the space; wrap is the caller's problem by contract
		}
		if got, want := want6(a).next(), want6(a.Next()); got != want {
			t.Fatalf("next disagrees with netip at %v", a)
		}
	}
}

// The netip conversions must round-trip, since the public API speaks netip and
// the internals do not.
func TestAddr6RoundTripsNetip(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 19))
	for i := 0; i < 100000; i++ {
		a := netip.AddrFrom16(rand16(r))
		if got := addr6Of(a).addr(); got != a {
			t.Fatalf("round trip changed the address: %v -> %v", a, got)
		}
	}
}

// The parsers exist to avoid an allocation per address at feed scale; hold
// them to it.
func TestParseAddrZeroAlloc(t *testing.T) {
	in4 := []byte("203.0.113.87")
	if n := testing.AllocsPerRun(1000, func() {
		if _, ok := parseAddr4(in4); !ok {
			t.Fatal("reject")
		}
	}); n != 0 {
		t.Fatalf("parseAddr4 allocates: %.1f allocs/op", n)
	}
	in6 := []byte("2001:db8:85a3::8a2e:370:7334")
	if n := testing.AllocsPerRun(1000, func() {
		if _, ok := parseAddr6(in6); !ok {
			t.Fatal("reject")
		}
	}); n != 0 {
		t.Fatalf("parseAddr6 allocates: %.1f allocs/op", n)
	}
}

// Bidirectional differential fuzz: each parser accepts exactly when the oracle
// accepts (zone-free inputs), and the value agrees when both do. This is the
// test that finds the cases nobody thought to list.
func FuzzParseAddr(f *testing.F) {
	for _, s := range []string{
		"1.2.3.4", "255.255.255.255", "0.0.0.0", "::", "::1", "1000::",
		"::ffff:1.2.3.4", "64:ff9b::192.0.2.33", "fe80::1", "1:2:3:4:5:6:7:8",
		"2001:0db8:0000:0000:0000:0000:0000:0001", "1.2.3.04", "1::2::3", "%",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 64 {
			return
		}
		oracle, err := netip.ParseAddr(s)

		got4, ok4 := parseAddr4([]byte(s))
		if want := err == nil && oracle.Is4(); ok4 != want {
			t.Fatalf("parseAddr4(%q) accept=%v, netip accept-as-v4=%v", s, ok4, want)
		}
		if ok4 && got4 != want4(oracle) {
			t.Fatalf("parseAddr4(%q) = %08x, want %08x", s, got4, want4(oracle))
		}

		got6, ok6 := parseAddr6([]byte(s))
		// netip accepts zones and, in v6 position, nothing else this parser
		// deliberately narrows; agreement is required for zone-free inputs.
		want := err == nil && !oracle.Is4() && oracle.Zone() == "" && !strings.Contains(s, "%")
		if ok6 != want {
			t.Fatalf("parseAddr6(%q) accept=%v, netip accept-as-v6=%v", s, ok6, want)
		}
		if ok6 && got6 != want6(oracle) {
			t.Fatalf("parseAddr6(%q) disagrees with netip", s)
		}
	})
}

func BenchmarkParseAddr4(b *testing.B) {
	in := []byte("203.0.113.87")
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := parseAddr4(in); !ok {
			b.Fatal("reject")
		}
	}
}

func BenchmarkParseAddr6(b *testing.B) {
	in := []byte("2001:db8:85a3::8a2e:370:7334")
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := parseAddr6(in); !ok {
			b.Fatal("reject")
		}
	}
}
