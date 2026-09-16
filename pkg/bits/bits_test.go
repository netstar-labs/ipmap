package bits

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// layout under test: mixed widths, a reserved gap, and a field that ends
// exactly at bit 63, so both word boundaries are exercised.
func testLayout() (fields []Field, used uint) {
	var l Layout
	fields = []Field{
		l.Field(15), // 0-14
		l.Field(15), // 15-29
		l.Field(3),  // 30-32
		l.Field(1),  // 33
	}
	l.Reserve(4) // 34-37, deliberately unassigned
	fields = append(fields,
		l.Field(9),  // 38-46
		l.Field(17), // 47-63: ends at the top bit
	)
	return fields, l.Used()
}

// checkNoBleed saturates every field in one word, then reads each back through
// its getter. A getter whose mask is wider than its field cannot pass: the
// bits it would wrongly include are all ones, so the read comes back too large.
// It returns an error rather than failing a *testing.T so that the harness
// itself can be tested against a deliberately broken getter.
func checkNoBleed(fields []Field, get []func(uint64) uint64) error {
	var w uint64
	for _, f := range fields {
		w = f.Put(w, f.Max())
	}
	for i, f := range fields {
		if got, want := get[i](w), f.Max(); got != want {
			return fmt.Errorf("field %d (width %d, shift %d): got %d, want %d — the read includes bits outside the field",
				i, f.Width(), f.Shift(), got, want)
		}
	}
	return nil
}

func getters(fields []Field) []func(uint64) uint64 {
	g := make([]func(uint64) uint64, len(fields))
	for i, f := range fields {
		g[i] = f.Get
	}
	return g
}

func TestFieldsDoNotBleed(t *testing.T) {
	fields, _ := testLayout()
	if err := checkNoBleed(fields, getters(fields)); err != nil {
		t.Fatal(err)
	}
}

// The harness must catch the defect class it exists for, or every green run of
// TestFieldsDoNotBleed is decoration. Break one getter the classic way — a
// mask two bits wider than the field — and require the harness to notice.
func TestHarnessCatchesBleed(t *testing.T) {
	fields, _ := testLayout()
	get := getters(fields)
	f := fields[2] // width 3, with a saturated neighbour above
	get[2] = func(w uint64) uint64 {
		wrong := ^uint64(0) >> (64 - (f.Width() + 2)) // the hand-written-mask bug
		return w >> f.Shift() & wrong
	}
	if err := checkNoBleed(fields, get); err == nil {
		t.Fatal("a getter with a too-wide mask passed the harness; the harness proves nothing")
	}
}

// Clearing one field must not disturb any other, for every field in turn.
func TestFieldsAreIndependent(t *testing.T) {
	fields, _ := testLayout()
	for i, target := range fields {
		var w uint64
		for _, f := range fields {
			w = f.Put(w, f.Max())
		}
		w = target.Put(w, 0)
		for j, f := range fields {
			want := f.Max()
			if j == i {
				want = 0
			}
			if got := f.Get(w); got != want {
				t.Errorf("clearing field %d disturbed field %d: got %d, want %d", i, j, got, want)
			}
		}
	}
}

// A second Put replaces the first; nothing accumulates.
func TestSettersOverwrite(t *testing.T) {
	fields, _ := testLayout()
	f := fields[0]
	w := f.Put(0, 20006)
	w = f.Put(w, 401)
	if got := f.Get(w); got != 401 {
		t.Fatalf("second Put did not replace the first: got %d, want 401 (an OR-without-clear bug)", got)
	}
}

// An oversized value truncates to its own field and never reaches the one above.
func TestOversizedValueDoesNotSpill(t *testing.T) {
	fields, _ := testLayout()
	lo, hi := fields[0], fields[1]
	w := hi.Put(0, 7)
	w = lo.Put(w, hi.Max()<<20) // far wider than lo's 15 bits
	if got := hi.Get(w); got != 7 {
		t.Fatalf("an oversized Put spilled into the field above: got %d, want 7", got)
	}
	if got := lo.Get(w); got != hi.Max()<<20&lo.Max() {
		t.Fatalf("truncation is not the documented mask-to-width: got %d", got)
	}
}

// Reserved bits are never touched by any field on either side of the gap.
func TestReservedBitsStayUntouched(t *testing.T) {
	fields, _ := testLayout()
	var w uint64
	for _, f := range fields {
		w = f.Put(w, f.Max())
	}
	const gap = uint64(0xF) << 34 // the Reserve(4) span in testLayout
	if w&gap != 0 {
		t.Fatalf("a Put wrote into reserved bits: word %064b", w)
	}
	w = ^uint64(0) // reserved bits set by the caller...
	for _, f := range fields {
		w = f.Put(w, 0)
	}
	if w&gap != gap {
		t.Fatalf("a Put cleared reserved bits it does not own: word %064b", w)
	}
}

func TestLayoutAccounting(t *testing.T) {
	fields, used := testLayout()
	var total uint = 4 // the reserved gap
	for _, f := range fields {
		total += f.Width()
	}
	if used != total {
		t.Fatalf("Used() = %d, want %d", used, total)
	}
	if used != 64 {
		t.Fatalf("test layout should fill the word exactly: %d", used)
	}
}

// A single 64-bit field is legal and must round-trip the full range,
// including the all-ones value the mask derivation could get wrong.
func TestFullWidthField(t *testing.T) {
	var l Layout
	f := l.Field(64)
	if f.Max() != ^uint64(0) {
		t.Fatalf("Max of a 64-bit field = %d", f.Max())
	}
	if got := f.Get(f.Put(0, ^uint64(0))); got != ^uint64(0) {
		t.Fatalf("round trip lost bits: %d", got)
	}
}

// A layout that cannot exist must die at declaration, not at first use.
func TestBadDeclarationsPanic(t *testing.T) {
	expectPanic := func(name string, fn func()) {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic; a bad layout survived declaration")
				}
			}()
			fn()
		})
	}
	expectPanic("zero width", func() { var l Layout; l.Field(0) })
	expectPanic("width over 64", func() { var l Layout; l.Field(65) })
	expectPanic("total over 64", func() { var l Layout; l.Field(60); l.Field(5) })
	expectPanic("reserve over 64", func() { var l Layout; l.Field(60); l.Reserve(5) })
}

// Get and Put must not allocate: they run on hot paths millions of times.
func TestZeroAlloc(t *testing.T) {
	fields, _ := testLayout()
	f := fields[1]
	if n := testing.AllocsPerRun(1000, func() {
		w := f.Put(0, 12345)
		if f.Get(w) != 12345 {
			t.Fatal("round trip failed")
		}
	}); n != 0 {
		t.Fatalf("Get/Put allocate: %.1f allocs/op", n)
	}
}

// Random layouts and values round-trip, and saturation holds for every one of
// them — the property tests above, without the hand-picked layout.
func FuzzBitsLayout(f *testing.F) {
	f.Add(uint64(0x0f0f0f0f0f0f0f0f), uint64(1))
	f.Add(uint64(0xffffffffffffffff), uint64(0xdeadbeef))
	f.Add(uint64(1), uint64(0))
	f.Fuzz(func(t *testing.T, widthSeed, valueSeed uint64) {
		var l Layout
		var fields []Field
		r := rand.New(rand.NewPCG(widthSeed, valueSeed))
		for l.Used() < 64 {
			max := 64 - l.Used()
			w := uint(r.Uint64N(uint64(max))) + 1
			fields = append(fields, l.Field(w))
		}
		if err := checkNoBleed(fields, getters(fields)); err != nil {
			t.Fatal(err)
		}
		var w uint64
		want := make([]uint64, len(fields))
		for i, fd := range fields {
			want[i] = r.Uint64() & fd.Max()
			w = fd.Put(w, want[i])
		}
		for i, fd := range fields {
			if got := fd.Get(w); got != want[i] {
				t.Fatalf("field %d (width %d): got %d, want %d", i, fd.Width(), got, want[i])
			}
		}
	})
}

func BenchmarkBitsGetSet(b *testing.B) {
	fields, _ := testLayout()
	f := fields[1]
	b.ReportAllocs()
	var w uint64
	for b.Loop() {
		w = f.Put(w, 20005)
		if f.Get(w) != 20005 {
			b.Fatal("round trip failed")
		}
	}
}
