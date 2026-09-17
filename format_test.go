package ipmap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"
)

// artifact builds a Map, serialises it, and returns both.
func artifact(t testing.TB, seed uint64, n int, opt Options) (*Map, []byte) {
	t.Helper()
	m, _ := buildRandomOpt(t, seed, n, opt)
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return m, buf.Bytes()
}

func openTemp(t testing.TB, data []byte) (*Map, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.ipmap")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return Open(p)
}

// Build → write → open → re-write must be byte-identical, and the opened map
// must answer exactly as the built one — every key, both modes, plus the
// single-family and empty shapes.
func TestArtifactRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  Options
	}{
		{"direct", Options{ValLen: testValLen}},
		{"interned", Options{ValLen: testValLen, Intern: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, o := buildRandomOpt(t, 31, 1500, tc.opt)
			var one bytes.Buffer
			if _, err := built.WriteTo(&one); err != nil {
				t.Fatal(err)
			}
			opened, err := openTemp(t, one.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()

			var two bytes.Buffer
			if _, err := opened.WriteTo(&two); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(one.Bytes(), two.Bytes()) {
				t.Fatal("open → re-write is not byte-identical")
			}
			if opened.Stats() != built.Stats() {
				t.Fatalf("stats changed through the file: %+v vs %+v", opened.Stats(), built.Stats())
			}
			if opened.Epoch() != built.Epoch() || opened.Epoch() == 0 {
				t.Fatalf("epoch lost: %d vs %d", opened.Epoch(), built.Epoch())
			}
			for k, want := range o {
				got, ok := opened.Lookup(k)
				if !ok || !bytes.Equal(got, want) {
					t.Fatalf("opened map disagrees at %v", k)
				}
			}
		})
	}
}

func TestArtifactSingleFamilyAndEmpty(t *testing.T) {
	for _, build := range []struct {
		name string
		fill func(b *Builder)
	}{
		{"empty", func(b *Builder) {}},
		{"v4-only", func(b *Builder) { _ = b.Add(netip.MustParseAddr("1.2.3.4"), []byte{1, 2}) }},
		{"v6-only", func(b *Builder) { _ = b.Add(netip.MustParseAddr("2001:db8::1"), []byte{3, 4}) }},
	} {
		t.Run(build.name, func(t *testing.T) {
			b := NewBuilder(Options{ValLen: 2})
			build.fill(b)
			m, err := b.Build()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if _, err := m.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			opened, err := openTemp(t, buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			var again bytes.Buffer
			if _, err := opened.WriteTo(&again); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), again.Bytes()) {
				t.Fatal("round trip not byte-identical")
			}
		})
	}
}

// --- the corruption corpus -------------------------------------------------

func le() binary.ByteOrder { return binary.LittleEndian }

func fixHeaderCRC(b []byte) { le().PutUint32(b[272:], crc32.ChecksumIEEE(b[:272])) }

func fixSectionCRC(b []byte, slot int) {
	at := 80 + slot*24
	off, l := le().Uint64(b[at:]), le().Uint64(b[at+8:])
	le().PutUint32(b[at+16:], crc32.ChecksumIEEE(b[off:off+l]))
	fixHeaderCRC(b)
}

func section(b []byte, slot int) (off, length uint64) {
	at := 80 + slot*24
	return le().Uint64(b[at:]), le().Uint64(b[at+8:])
}

// Every member of the corpus must be refused with the right error, and none
// may panic. The deep cases repair the checksums after mutating, so they prove
// the validation *behind* the CRCs — the corpus a fuzzer cannot reach, because
// mutation without CRC repair always dies at the checksum.
func TestCorruptionCorpus(t *testing.T) {
	_, direct := artifact(t, 41, 400, Options{ValLen: testValLen})
	_, interned := artifact(t, 41, 400, Options{ValLen: testValLen, Intern: true})

	type corrupt struct {
		name string
		want error
		mut  func(t *testing.T, b []byte) []byte
	}
	cases := []corrupt{
		{"empty file", ErrFormat, func(t *testing.T, b []byte) []byte { return nil }},
		{"short file", ErrFormat, func(t *testing.T, b []byte) []byte { return b[:headerSize-1] }},
		{"magic flipped", ErrFormat, func(t *testing.T, b []byte) []byte { b[0] ^= 0xFF; return b }},
		{"header bit flipped, checksum stale", ErrCorrupt, func(t *testing.T, b []byte) []byte { b[33] ^= 1; return b }},
		{"version bumped", ErrVersion, func(t *testing.T, b []byte) []byte {
			le().PutUint32(b[8:], formatVersion+1)
			fixHeaderCRC(b)
			return b
		}},
		{"unknown flag bit", ErrVersion, func(t *testing.T, b []byte) []byte {
			le().PutUint32(b[12:], le().Uint32(b[12:])|1<<7)
			fixHeaderCRC(b)
			return b
		}},
		{"value width zero", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			le().PutUint32(b[16:], 0)
			fixHeaderCRC(b)
			return b
		}},
		{"value width over the cap", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			le().PutUint32(b[16:], maxValLen+1)
			fixHeaderCRC(b)
			return b
		}},
		{"entry count inflated", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			le().PutUint64(b[32:], le().Uint64(b[32:])+1)
			fixHeaderCRC(b)
			return b
		}},
		{"more prefixes than entries", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			le().PutUint64(b[48:], le().Uint64(b[40:])+1)
			fixHeaderCRC(b)
			return b
		}},
		{"conflicts exceed dups", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			le().PutUint64(b[72:], le().Uint64(b[64:])+1)
			fixHeaderCRC(b)
			return b
		}},
		{"section offset misaligned", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			at := 80 + s4Suffix*24
			le().PutUint64(b[at:], le().Uint64(b[at:])+4)
			fixHeaderCRC(b)
			return b
		}},
		{"section overlaps its neighbour", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			at := 80 + s4Suffix*24
			le().PutUint64(b[at:], le().Uint64(b[at:])-8)
			fixHeaderCRC(b)
			return b
		}},
		{"trailing garbage", ErrCorrupt, func(t *testing.T, b []byte) []byte { return append(b, 0xAB) }},
		{"padding byte set", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			// suffix4 has length n4, padded to 8; poison a pad byte and repair
			// both checksums so only the padding rule can object.
			off, l := section(b, s4Suffix)
			if int(l)%8 == 0 {
				t.Skip("suffix length happens to be 8-aligned for this seed")
			}
			b[off+l] = 0xEE
			fixSectionCRC(b, s4Suffix)
			return b
		}},
	}
	// A flipped byte inside every non-empty section: the per-section CRC must
	// object even though the header checksum is intact.
	for slot := 0; slot < numSections; slot++ {
		slot := slot
		cases = append(cases, corrupt{
			name: "flipped byte in section " + string(rune('0'+slot)),
			want: ErrCorrupt,
			mut: func(t *testing.T, b []byte) []byte {
				off, l := section(b, slot)
				if l == 0 {
					t.Skip("section empty in this artifact")
				}
				b[off+l/2] ^= 0x01
				return b
			},
		})
	}
	// Truncation at, just before, and just after every section boundary.
	for slot := 0; slot < numSections; slot++ {
		slot := slot
		for _, d := range []int{-1, 0, 1} {
			d := d
			cases = append(cases, corrupt{
				name: "truncated around section " + string(rune('0'+slot)),
				want: ErrCorrupt,
				mut: func(t *testing.T, b []byte) []byte {
					off, _ := section(b, slot)
					cut := int(off) + d
					if cut <= 0 || cut >= len(b) {
						t.Skip("cut out of range")
					}
					if cut < headerSize {
						return b[:cut] // dies as short/format instead
					}
					return b[:cut]
				},
			})
		}
	}

	deep := []corrupt{
		{"32-bit suffixes unsorted", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			off, l := section(b, s4Suffix)
			if l < 2 {
				t.Skip("too few entries")
			}
			// find two adjacent in-group suffixes and swap them: idx stays
			// valid, checksums repaired, only the order rule can object
			for i := off; i < off+l-1; i++ {
				if b[i] < b[i+1] {
					b[i], b[i+1] = b[i+1], b[i]
					fixSectionCRC(b, s4Suffix)
					return b
				}
			}
			t.Skip("no adjacent pair in this artifact")
			return b
		}},
		{"128-bit suffixes unsorted", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			// Swap the first two suffixes of a multi-entry group — pidx and the
			// prefixes stay valid, checksums repaired, only the order rule can
			// object. The swap must stay inside one group: across a boundary the
			// result can still be legally ordered.
			poff, pl := section(b, s6Pidx)
			soff, _ := section(b, s6Suffix)
			for g := uint64(0); pl >= 8 && g < pl/4-1; g++ {
				lo, hi := le().Uint32(b[poff+g*4:]), le().Uint32(b[poff+(g+1)*4:])
				if hi-lo >= 2 {
					i := soff + uint64(lo)*8
					var tmp [8]byte
					copy(tmp[:], b[i:i+8])
					copy(b[i:i+8], b[i+8:i+16])
					copy(b[i+8:i+16], tmp[:])
					fixSectionCRC(b, s6Suffix)
					return b
				}
			}
			t.Skip("no 128-bit group with two entries in this artifact")
			return b
		}},
		{"128-bit prefix order broken", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			off, l := section(b, s6Prefix)
			if l < 16 {
				t.Skip("too few prefixes")
			}
			copy(b[off:off+8], b[off+8:off+16]) // duplicate the second prefix over the first
			fixSectionCRC(b, s6Prefix)
			return b
		}},
		{"pidx not monotone", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			off, l := section(b, s6Pidx)
			if l < 12 {
				t.Skip("too few groups")
			}
			le().PutUint32(b[off+4:], le().Uint32(b[off+8:])+1)
			fixSectionCRC(b, s6Pidx)
			return b
		}},
		{"32-bit index does not end at the entry count", ErrCorrupt, func(t *testing.T, b []byte) []byte {
			off, l := section(b, s4Idx)
			if l == 0 {
				t.Skip("no 32-bit family")
			}
			last := off + l - 4
			le().PutUint32(b[last:], le().Uint32(b[last:])+1)
			fixSectionCRC(b, s4Idx)
			return b
		}},
	}
	cases = append(cases, deep...)
	// The buggy-writer case a fuzzer can never reach: a well-checksummed id
	// pointing past the value table.
	internedOnly := corrupt{"value id out of the table", ErrCorrupt, func(t *testing.T, b []byte) []byte {
		off, l := section(b, s4Vals)
		if l == 0 {
			t.Skip("no 32-bit family")
		}
		for i := uint64(0); i < l; i++ {
			b[off+i] = 0xFF // ids saturated far past distinct
		}
		fixSectionCRC(b, s4Vals)
		return b
	}}

	// One scratch buffer for every case: a fresh 67 MB copy per case is ~9 GB
	// of allocation churn, which under the race detector's slower GC runs the
	// 8 GB CI machine out of memory. Nothing retains the bytes past its case —
	// openTemp writes them to a file and opens that.
	scratch := make([]byte, 0, max(len(direct), len(interned))+16)
	run := func(t *testing.T, base []byte, c corrupt) {
		t.Helper()
		mutated := c.mut(t, append(scratch[:0], base...))
		m, err := openTemp(t, mutated)
		if err == nil {
			m.Close()
			t.Fatalf("%s: accepted", c.name)
		}
		if !errors.Is(err, c.want) && !(errors.Is(err, ErrFormat) && c.want == ErrCorrupt && len(mutated) < headerSize) {
			t.Fatalf("%s: error %v, want %v", c.name, err, c.want)
		}
	}
	for _, c := range cases {
		t.Run("direct/"+c.name, func(t *testing.T) { run(t, direct, c) })
		t.Run("interned/"+c.name, func(t *testing.T) { run(t, interned, c) })
	}
	t.Run("interned/"+internedOnly.name, func(t *testing.T) { run(t, interned, internedOnly) })

	// Crafted, not mutated: the interned flag with zero entries in either
	// family. No build can produce it (the flag follows Distinct, which needs an
	// Add), it once opened cleanly, and its re-serialisation neither matched the
	// file nor was itself accepted — the one canonicality hole the audit found.
	t.Run("crafted/interned but empty", func(t *testing.T) {
		const valLen, distinct = 4, 1
		f := make([]byte, headerSize+8) // header + the value table, padded to 8
		copy(f, magic[:])
		le().PutUint32(f[8:], formatVersion)
		le().PutUint32(f[12:], flagInterned)
		le().PutUint32(f[16:], valLen)
		le().PutUint32(f[20:], 1) // idWidth(1)
		le().PutUint64(f[56:], distinct)
		for i := 0; i < numSections; i++ {
			le().PutUint64(f[80+i*24:], headerSize) // every empty slot sits at 280
		}
		at := 80 + sTab*24
		le().PutUint64(f[at+8:], distinct*valLen)
		le().PutUint32(f[at+16:], crc32.ChecksumIEEE(f[headerSize:headerSize+distinct*valLen]))
		fixHeaderCRC(f)
		m, err := openTemp(t, f)
		if err == nil {
			m.Close()
			t.Fatal("an interned artifact with no entries was accepted")
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("error %v, want %v", err, ErrCorrupt)
		}
	})
}

// Close is documented idempotent, concurrent calls included: racing Closes on
// one Map must neither double-release nor call through a half-cleared field.
// The teeth are the -race pass — a bare nil-check here was a demonstrated
// check-then-act crash.
func TestConcurrentCloseSameMap(t *testing.T) {
	_, raw := artifact(t, 47, 60, Options{ValLen: testValLen})
	m, err := openTemp(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := m.Close(); err != nil { // and once more, sequentially
		t.Fatal(err)
	}
}

// Anything Open accepts must serve lookups without panicking and re-serialise
// byte-identically: accepted implies canonical, so there is exactly one file
// for any given content and nothing accepted is half-trusted.
func FuzzOpen(f *testing.F) {
	// Seeds must be small (the fuzz worker shares ~100 MB with its inputs) and
	// any artifact with a 32-bit family carries the fixed 67 MB dense index —
	// so the seeds here are the 128-bit-only and empty shapes, and the 32-bit
	// reader path gets its adversarial coverage from TestOpenMutational below.
	seed6 := func(seed uint64, opt Options) []byte {
		b := NewBuilder(opt)
		r := rand.New(rand.NewPCG(seed, 3))
		for i := 0; i < 40; i++ {
			a := addr6{hi: r.Uint64() >> 40, lo: r.Uint64() >> 32}.addr()
			if err := b.Add(a, valN(a, 0, opt.ValLen)); err != nil {
				f.Fatal(err)
			}
		}
		m, err := b.Build()
		if err != nil {
			f.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := m.WriteTo(&buf); err != nil {
			f.Fatal(err)
		}
		return buf.Bytes()
	}
	b := NewBuilder(Options{ValLen: 3})
	empty, _ := b.Build()
	var ebuf bytes.Buffer
	_, _ = empty.WriteTo(&ebuf)
	f.Add(seed6(51, Options{ValLen: 3}))
	f.Add(seed6(52, Options{ValLen: 3, Intern: true}))
	f.Add(ebuf.Bytes())
	f.Add([]byte("IPMAPDB\x00 not really"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		m, err := openBytes(data)
		if err != nil {
			return
		}
		for _, s := range []string{"0.0.0.0", "1.2.3.4", "255.255.255.255", "::", "2001:db8::1", "::ffff:9.9.9.9"} {
			m.Lookup(netip.MustParseAddr(s))
		}
		var out bytes.Buffer
		if _, err := m.WriteTo(&out); err != nil {
			t.Fatalf("accepted but cannot re-serialise: %v", err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("accepted a non-canonical artifact: %d in, %d out", len(data), out.Len())
		}
	})
}

// The 32-bit reader path, adversarially: thousands of seeded random mutations
// — byte flips, truncations, splices — against artifacts that carry the 67 MB
// dense index the fuzzer cannot hold. Same contract as FuzzOpen: never panic,
// and anything accepted re-serialises byte-identically.
func TestOpenMutational(t *testing.T) {
	_, direct := artifact(t, 53, 200, Options{ValLen: testValLen})
	_, interned := artifact(t, 54, 200, Options{ValLen: testValLen, Intern: true})
	r := rand.New(rand.NewPCG(55, 56))
	rounds := 1200
	if raceEnabled {
		rounds = 200
	}
	// One scratch buffer across all rounds, for the same reason as the
	// corruption corpus: hundreds of fresh 67 MB copies are what pushes the
	// race pass past the CI machine's memory, and no round retains its bytes.
	scratch := make([]byte, 0, max(len(direct), len(interned)))
	for _, base := range [][]byte{direct, interned} {
		for i := 0; i < rounds; i++ {
			data := append(scratch[:0], base...)
			switch r.IntN(4) {
			case 0: // flip a byte anywhere
				data[r.IntN(len(data))] ^= byte(1 + r.IntN(255))
			case 1: // truncate anywhere
				data = data[:r.IntN(len(data))]
			case 2: // flip in the header/table, then repair the header CRC so
				// deeper validation is what gets exercised
				data[r.IntN(headerSize-8)] ^= byte(1 + r.IntN(255))
				fixHeaderCRC(data)
			case 3: // flip inside a section and repair its CRC: semantic checks
				slot := r.IntN(numSections)
				off, l := section(data, slot)
				if l == 0 {
					continue
				}
				data[off+uint64(r.IntN(int(l)))] ^= byte(1 + r.IntN(255))
				fixSectionCRC(data, slot)
			}
			m, err := openBytes(data)
			if err != nil {
				continue
			}
			for _, sdr := range []string{"1.2.3.4", "10.11.12.13", "2001:db8::1"} {
				m.Lookup(netip.MustParseAddr(sdr))
			}
			var out bytes.Buffer
			if _, err := m.WriteTo(&out); err != nil || !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("mutation %d accepted a non-canonical artifact", i)
			}
		}
	}
}

// A value from an opened map aliases the mapping — the zero-copy promise.
func TestOpenedLookupAliasesMapping(t *testing.T) {
	m, o := buildRandomOpt(t, 61, 300, Options{ValLen: testValLen})
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	opened, err := openBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	lo := uintptr(unsafe.Pointer(&data[0]))
	hi := lo + uintptr(len(data))
	checked := 0
	for k := range o {
		v, ok := opened.Lookup(k)
		if !ok {
			t.Fatal("miss")
		}
		p := uintptr(unsafe.Pointer(&v[0]))
		if p < lo || p >= hi {
			t.Fatal("a returned value does not alias the artifact bytes")
		}
		if checked++; checked > 50 {
			break
		}
	}
	var dst [testValLen]byte
	for k := range o {
		if n := testing.AllocsPerRun(500, func() {
			opened.Lookup(k)
			opened.LookupInto(k, dst[:])
		}); n != 0 {
			t.Fatalf("opened-map lookup allocates: %.1f", n)
		}
		break
	}
}

func TestConcurrentReaders(t *testing.T) {
	m, keys := artifactOnDisk(t, 71, 2000)
	defer m.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var dst [testValLen]byte
			for i := 0; i < 5000; i++ {
				k := keys[(i*7+g)%len(keys)]
				if _, ok := m.Lookup(k); !ok {
					t.Error("concurrent miss on a stored key")
					return
				}
				m.LookupInto(k, dst[:])
			}
		}(g)
	}
	wg.Wait()
}

func TestConcurrentOpenClose(t *testing.T) {
	_, keys := artifactOnDisk(t, 72, 500)
	p := filepath.Join(t.TempDir(), "a.ipmap")
	m, o := buildRandomOpt(t, 72, 500, Options{ValLen: testValLen})
	_ = o
	fh, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteTo(fh); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				h, err := Open(p)
				if err != nil {
					t.Error(err)
					return
				}
				for j := 0; j < 50; j++ {
					h.Lookup(keys[j%len(keys)])
				}
				h.Close()
			}
		}()
	}
	wg.Wait()
}

func artifactOnDisk(t testing.TB, seed uint64, n int) (*Map, []netip.Addr) {
	t.Helper()
	m, o := buildRandomOpt(t, seed, n, Options{ValLen: testValLen})
	p := filepath.Join(t.TempDir(), "a.ipmap")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	opened, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]netip.Addr, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	return opened, keys
}

func BenchmarkOpen(b *testing.B) {
	m, _ := buildRandomOpt(b, 81, 200000, Options{ValLen: testValLen, Intern: true})
	p := filepath.Join(b.TempDir(), "a.ipmap")
	f, err := os.Create(p)
	if err != nil {
		b.Fatal(err)
	}
	sz, err := m.WriteTo(f)
	if err != nil {
		b.Fatal(err)
	}
	f.Close()
	b.SetBytes(sz)
	b.ReportAllocs()
	for b.Loop() {
		h, err := Open(p)
		if err != nil {
			b.Fatal(err)
		}
		h.Close()
	}
}
