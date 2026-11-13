package ipmap

import (
	"math/rand/v2"
	"net/netip"
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

// addr converts to the standard library's form. Tests only: the production
// paths never leave the internal representation.
func (a addr6) addr() netip.Addr {
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(a.hi >> (56 - 8*i))
		b[8+i] = byte(a.lo >> (56 - 8*i))
	}
	return netip.AddrFrom16(b)
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
