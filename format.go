package ipmap

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"time"
	"unsafe"
)

// The artifact: one self-describing file holding both families, designed to be
// memory-mapped and then trusted — which means Open earns that trust by
// verifying everything first. Little-endian throughout, every section 8-byte
// aligned, a CRC per section and one over the header, and a reader that
// refuses anything it cannot fully validate: an artifact that answers wrongly
// is strictly worse than one that does not answer.
//
// Layout:
//
//	  0  magic "IPMAPDB\x00"                      8 bytes
//	  8  format version (uint32)
//	 12  flags (uint32; bit 0 = interned)
//	 16  value width (uint32)
//	 20  id width (uint32; 0 when direct)
//	 24  build epoch, unix seconds (int64)
//	 32  n4, n6, prefixes, distinct, dups, dupConflicts   (uint64 each)
//	 80  section table: 8 × { offset u64, length u64, crc u32, pad u32 }
//	272  header CRC (uint32, over bytes [0,272)), 4 bytes pad
//	280  sections, each 8-aligned, in slot order, ending exactly at EOF
//
// The trailing "exactly at EOF" is deliberate: an accepted artifact has no
// unaccounted bytes anywhere, which is what makes accepted-implies-canonical
// testable — any file Open accepts re-serialises byte-identically.
const (
	formatVersion = 1
	headerSize    = 280

	flagInterned = 1 << 0
	knownFlags   = flagInterned
)

var magic = [8]byte{'I', 'P', 'M', 'A', 'P', 'D', 'B', 0}

// The section slots, in file order. A family's slots are zero-length when it
// has no entries; sVals holds direct values or packed ids, per the flag.
const (
	s4Idx = iota
	s4Suffix
	s4Vals
	s6Prefix
	s6Pidx
	s6Suffix
	s6Vals
	sTab
	numSections
)

// WriteTo serialises the Map. Everything is sized and checksummed up front, so
// the write is a single forward pass and works on any io.Writer.
func (m *Map) WriteTo(w io.Writer) (int64, error) {
	secs := m.sections()

	hdr := make([]byte, headerSize)
	copy(hdr, magic[:])
	le := binary.LittleEndian
	le.PutUint32(hdr[8:], formatVersion)
	var flags uint32
	if m.interned() {
		flags |= flagInterned
	}
	le.PutUint32(hdr[12:], flags)
	le.PutUint32(hdr[16:], uint32(m.valLen))
	le.PutUint32(hdr[20:], uint32(m.idW()))
	le.PutUint64(hdr[24:], uint64(m.epoch))
	le.PutUint64(hdr[32:], uint64(m.stats.Addrs4))
	le.PutUint64(hdr[40:], uint64(m.stats.Addrs6))
	le.PutUint64(hdr[48:], uint64(len(m.s6.prefix)))
	le.PutUint64(hdr[56:], uint64(m.stats.Distinct))
	le.PutUint64(hdr[64:], uint64(m.stats.Dups))
	le.PutUint64(hdr[72:], uint64(m.stats.DupConflicts))

	off := uint64(headerSize)
	for i, s := range secs {
		at := 80 + i*24
		le.PutUint64(hdr[at:], off)
		le.PutUint64(hdr[at+8:], uint64(len(s)))
		le.PutUint32(hdr[at+16:], crc32.ChecksumIEEE(s))
		off += uint64(pad8(len(s)))
	}
	le.PutUint32(hdr[272:], crc32.ChecksumIEEE(hdr[:272]))

	total := int64(0)
	n, err := w.Write(hdr)
	total += int64(n)
	if err != nil {
		return total, err
	}
	var zero [8]byte
	for _, s := range secs {
		n, err := w.Write(s)
		total += int64(n)
		if err != nil {
			return total, err
		}
		if p := pad8(len(s)) - len(s); p > 0 {
			n, err := w.Write(zero[:p])
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// sections returns each slot's payload bytes, in slot order. Views, not
// copies — WriteTo streams them straight out.
func (m *Map) sections() [numSections][]byte {
	var s [numSections][]byte
	if m.stats.Addrs4 > 0 {
		s[s4Idx] = u32bytes(m.s4.idx)
		s[s4Suffix] = m.s4.suffix
		s[s4Vals] = m.s4.vals.payload()
	}
	if m.stats.Addrs6 > 0 {
		s[s6Prefix] = u64bytes(m.s6.prefix)
		s[s6Pidx] = u32bytes(m.s6.pidx[:len(m.s6.prefix)+1])
		s[s6Suffix] = u64bytes(m.s6.suffix)
		s[s6Vals] = m.s6.vals.payload()
	}
	if m.interned() {
		s[sTab] = m.tabBytes()
	}
	return s
}

// payload is the per-entry value bytes: the direct array, or the packed ids.
func (v *values) payload() []byte {
	if v.direct != nil {
		return v.direct
	}
	return v.ids
}

func (m *Map) interned() bool { return m.stats.Distinct > 0 }

func (m *Map) idW() int {
	if !m.interned() {
		return 0
	}
	return idWidth(m.stats.Distinct)
}

// tabBytes is the shared value table; both stores reference the same one, so
// take whichever family is populated.
func (m *Map) tabBytes() []byte {
	if m.s4.vals.tab != nil {
		return m.s4.vals.tab
	}
	return m.s6.vals.tab
}

// Open memory-maps an artifact and verifies all of it — header, every section
// checksum, and every structural invariant a lookup depends on — before
// returning. The cost is one sequential pass over the file, paid once at open
// rather than as wrong answers later. The returned Map's values alias the
// mapping and are valid until Close.
func Open(path string) (*Map, error) {
	data, closer, err := mapFile(path)
	if err != nil {
		return nil, err
	}
	m, err := openBytes(data)
	if err != nil {
		closer()
		return nil, err
	}
	m.close = closer
	return m, nil
}

// Close releases the mapping. Lookups against the Map, and values previously
// returned by them, must not be used after Close. A Map from Build has nothing
// to release and Close is a no-op.
func (m *Map) Close() error {
	if m.close != nil {
		c := m.close
		m.close = nil
		c()
	}
	return nil
}

// openBytes validates data as an artifact and builds a Map viewing it.
func openBytes(data []byte) (*Map, error) {
	le := binary.LittleEndian
	if len(data) < headerSize || [8]byte(data[:8]) != magic {
		return nil, fmt.Errorf("%w", ErrFormat)
	}
	if crc32.ChecksumIEEE(data[:272]) != le.Uint32(data[272:]) {
		return nil, fmt.Errorf("header checksum: %w", ErrCorrupt)
	}
	if v := le.Uint32(data[8:]); v != formatVersion {
		return nil, fmt.Errorf("format version %d: %w", v, ErrVersion)
	}
	flags := le.Uint32(data[12:])
	if flags&^uint32(knownFlags) != 0 {
		return nil, fmt.Errorf("unknown flags %#x: %w", flags, ErrVersion)
	}
	// Reserved bytes must be zero — the table rows' pad words and the header's
	// tail. Anything unvalidated is a byte an accepted file could disagree on
	// with its own re-serialisation, and accepted must imply canonical.
	for i := 0; i < numSections; i++ {
		if le.Uint32(data[80+i*24+20:]) != 0 {
			return nil, fmt.Errorf("section table reserved bytes: %w", ErrCorrupt)
		}
	}
	if le.Uint32(data[276:]) != 0 {
		return nil, fmt.Errorf("header reserved bytes: %w", ErrCorrupt)
	}
	interned := flags&flagInterned != 0

	valLen := int(le.Uint32(data[16:]))
	idW := int(le.Uint32(data[20:]))
	epoch := int64(le.Uint64(data[24:]))
	n4 := le.Uint64(data[32:])
	n6 := le.Uint64(data[40:])
	np := le.Uint64(data[48:])
	distinct := le.Uint64(data[56:])
	dups := le.Uint64(data[64:])
	confl := le.Uint64(data[72:])

	switch {
	case valLen < 1 || valLen > maxValLen:
		return nil, fmt.Errorf("value width %d: %w", valLen, ErrCorrupt)
	case n4 > math.MaxUint32 || n6 > math.MaxUint32 || np > n6,
		n6 > 0 && np == 0,
		dups > math.MaxInt64 || confl > dups,
		interned && (idW != idWidth(int(distinct)) || distinct == 0 || distinct > math.MaxUint32),
		!interned && (idW != 0 || distinct != 0):
		return nil, fmt.Errorf("header counts: %w", ErrCorrupt)
	}

	// The section table: expected lengths are functions of the counts, offsets
	// are aligned, in order, gap-free after padding, and end exactly at EOF.
	perVal := valLen
	if interned {
		perVal = idW
	}
	expect := [numSections]uint64{}
	if n4 > 0 {
		expect[s4Idx] = (1<<24 + 1) * 4
		expect[s4Suffix] = n4
		expect[s4Vals] = n4 * uint64(perVal)
	}
	if n6 > 0 {
		expect[s6Prefix] = np * 8
		expect[s6Pidx] = (np + 1) * 4
		expect[s6Suffix] = n6 * 8
		expect[s6Vals] = n6 * uint64(perVal)
	}
	if interned {
		expect[sTab] = distinct * uint64(valLen)
	}

	var secs [numSections][]byte
	off := uint64(headerSize)
	for i := 0; i < numSections; i++ {
		at := 80 + i*24
		o, l := le.Uint64(data[at:]), le.Uint64(data[at+8:])
		padEnd := o + uint64(pad8(int(l)))
		// The padded end is the bound that matters: the padding bytes are read
		// below, so a file truncated inside a section's padding must die here,
		// not as a slice panic. (Found by the corruption corpus, not foreseen.)
		if l != expect[i] || o != off || o%8 != 0 || padEnd < o || padEnd > uint64(len(data)) {
			return nil, fmt.Errorf("section %d geometry: %w", i, ErrCorrupt)
		}
		secs[i] = data[o : o+l : o+l]
		if crc32.ChecksumIEEE(secs[i]) != le.Uint32(data[at+16:]) {
			return nil, fmt.Errorf("section %d checksum: %w", i, ErrCorrupt)
		}
		for _, p := range data[o+l : o+uint64(pad8(int(l)))] {
			if p != 0 {
				return nil, fmt.Errorf("section %d padding: %w", i, ErrCorrupt)
			}
		}
		off += uint64(pad8(int(l)))
	}
	if off != uint64(len(data)) {
		return nil, fmt.Errorf("trailing bytes: %w", ErrCorrupt)
	}

	m := &Map{valLen: valLen, epoch: epoch, stats: Stats{
		Addrs4: int(n4), Addrs6: int(n6),
		Dups: int(dups), DupConflicts: int(confl), Distinct: int(distinct),
	}}

	var tab []byte
	if interned {
		tab = secs[sTab]
	}
	mkvals := func(sec []byte) values {
		v := values{valLen: valLen}
		if interned {
			v.tab, v.ids, v.idW = tab, sec, idW
		} else {
			v.direct = sec
		}
		return v
	}
	if n4 > 0 {
		m.s4 = store4{idx: u32view(secs[s4Idx]), suffix: secs[s4Suffix], vals: mkvals(secs[s4Vals])}
	}
	if n6 > 0 {
		m.s6 = store6{
			prefix: u64view(secs[s6Prefix]), pidx: u32view(secs[s6Pidx]),
			suffix: u64view(secs[s6Suffix]), vals: mkvals(secs[s6Vals]),
		}
	}

	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// validate checks every structural invariant the lookup path relies on, so a
// well-checksummed file from a buggy or hostile writer still cannot produce an
// out-of-bounds access or a wrong answer.
func (m *Map) validate() error {
	bad := func(what string) error { return fmt.Errorf("%s: %w", what, ErrCorrupt) }

	if n := m.stats.Addrs4; n > 0 {
		s := &m.s4
		if s.idx[0] != 0 || int(s.idx[len(s.idx)-1]) != n {
			return bad("32-bit index bounds")
		}
		for i := 1; i < len(s.idx); i++ {
			lo, hi := s.idx[i-1], s.idx[i]
			if hi < lo {
				return bad("32-bit index order")
			}
			for j := lo + 1; j < hi; j++ { // strictly ascending within a group
				if s.suffix[j-1] >= s.suffix[j] {
					return bad("32-bit suffix order")
				}
			}
		}
	}
	if n := m.stats.Addrs6; n > 0 {
		s := &m.s6
		if s.pidx[0] != 0 || int(s.pidx[len(s.pidx)-1]) != n {
			return bad("128-bit index bounds")
		}
		for i := 1; i < len(s.prefix); i++ {
			if s.prefix[i-1] >= s.prefix[i] {
				return bad("128-bit prefix order")
			}
		}
		for i := 1; i < len(s.pidx); i++ {
			lo, hi := s.pidx[i-1], s.pidx[i]
			if hi <= lo { // a listed prefix owns at least one entry
				return bad("128-bit group bounds")
			}
			for j := lo + 1; j < hi; j++ {
				if s.suffix[j-1] >= s.suffix[j] {
					return bad("128-bit suffix order")
				}
			}
		}
	}
	if m.interned() {
		checkIDs := func(v *values, n int) error {
			for i := 0; i < n; i++ {
				id := 0
				for b := v.idW - 1; b >= 0; b-- {
					id = id<<8 | int(v.ids[i*v.idW+b])
				}
				if id >= m.stats.Distinct {
					return bad("value id out of table")
				}
			}
			return nil
		}
		if m.stats.Addrs4 > 0 {
			if err := checkIDs(&m.s4.vals, m.stats.Addrs4); err != nil {
				return err
			}
		}
		if m.stats.Addrs6 > 0 {
			if err := checkIDs(&m.s6.vals, m.stats.Addrs6); err != nil {
				return err
			}
		}
	}
	return nil
}

func pad8(n int) int { return (n + 7) &^ 7 }

// Epoch reports when the artifact was built, unix seconds. Zero for a Map that
// has not been through a file.
func (m *Map) Epoch() int64 { return m.epoch }

func nowEpoch() int64 { return time.Now().Unix() }

// nativeLE reports whether this machine's byte order matches the file's.
// Everything shipped runs little-endian; the copying fallback below keeps a
// big-endian host correct rather than fast.
var nativeLE = binary.NativeEndian.Uint16([]byte{0x01, 0x00}) == 1

func u32bytes(v []uint32) []byte {
	if len(v) == 0 {
		return nil
	}
	if nativeLE {
		return unsafe.Slice((*byte)(unsafe.Pointer(&v[0])), len(v)*4)
	}
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], x)
	}
	return b
}

func u64bytes(v []uint64) []byte {
	if len(v) == 0 {
		return nil
	}
	if nativeLE {
		return unsafe.Slice((*byte)(unsafe.Pointer(&v[0])), len(v)*8)
	}
	b := make([]byte, len(v)*8)
	for i, x := range v {
		binary.LittleEndian.PutUint64(b[i*8:], x)
	}
	return b
}

func u32view(b []byte) []uint32 {
	if len(b) == 0 {
		return nil
	}
	if nativeLE {
		return unsafe.Slice((*uint32)(unsafe.Pointer(&b[0])), len(b)/4)
	}
	v := make([]uint32, len(b)/4)
	for i := range v {
		v[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return v
}

func u64view(b []byte) []uint64 {
	if len(b) == 0 {
		return nil
	}
	if nativeLE {
		return unsafe.Slice((*uint64)(unsafe.Pointer(&b[0])), len(b)/8)
	}
	v := make([]uint64, len(b)/8)
	for i := range v {
		v[i] = binary.LittleEndian.Uint64(b[i*8:])
	}
	return v
}
