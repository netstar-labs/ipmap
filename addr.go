package ipmap

import "net/netip"

// The address model. Each family gets the representation its arithmetic wants:
// a 32-bit address is a uint32, where comparison and successor are single
// instructions and need no helper at all; a 128-bit address is two host-order
// words ordered by (hi, lo), so sorting and comparing never touch wide
// arithmetic.
//
// The parsers exist because the build path reads addresses from byte slices at
// feed scale, and netip.ParseAddr takes a string — one allocation per address
// is billions per build. They are differential-tested against net/netip rather
// than against hand-written expectations: for any input, each parser accepts
// exactly when the standard library accepts (zone-free inputs), and produces
// byte-identical addresses when both do.

// addr6 is a 128-bit address as two host-order words, high first. Ordering by
// (hi, lo) equals numeric address order, and both words fit machine registers.
type addr6 struct{ hi, lo uint64 }

// less reports whether a sorts before b numerically.
func (a addr6) less(b addr6) bool { return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo) }

// next returns the following address, carrying across the 64-bit word
// boundary. It wraps at the top of the space, which a caller detects by the
// result comparing less than the input.
func (a addr6) next() addr6 {
	if a.lo++; a.lo == 0 {
		a.hi++
	}
	return a
}

// addr converts to the standard library's form.
func (a addr6) addr() netip.Addr {
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(a.hi >> (56 - 8*i))
		b[8+i] = byte(a.lo >> (56 - 8*i))
	}
	return netip.AddrFrom16(b)
}

// addr6Of converts from the standard library's form. The caller guarantees a
// is a valid 16-byte address; a 4-byte address must be mapped first.
func addr6Of(a netip.Addr) addr6 {
	b := a.As16()
	var r addr6
	for i := 0; i < 8; i++ {
		r.hi = r.hi<<8 | uint64(b[i])
		r.lo = r.lo<<8 | uint64(b[8+i])
	}
	return r
}

// parseAddr4 parses a dotted quad without allocating. It accepts exactly what
// net/netip accepts: four octets, each 0-255, no leading zeros — "1.2.3.04" is
// two different values depending on who reads it, so it is nobody's address.
func parseAddr4(b []byte) (uint32, bool) {
	var ip uint32
	octets := 0
	for i := 0; i < len(b); octets++ {
		if octets == 4 {
			return 0, false
		}
		if octets > 0 {
			if b[i] != '.' {
				return 0, false
			}
			i++
		}
		v, digits := 0, 0
		start := i
		for ; i < len(b) && b[i] >= '0' && b[i] <= '9'; i++ {
			v = v*10 + int(b[i]-'0')
			if digits++; digits > 3 || v > 255 {
				return 0, false
			}
		}
		if digits == 0 || (digits > 1 && b[start] == '0') {
			return 0, false
		}
		ip = ip<<8 | uint32(v)
	}
	if octets != 4 {
		return 0, false // never leak a partial parse with the failure bit
	}
	return ip, true
}

// parseAddr6 parses a textual 128-bit address without allocating: full and
// "::"-compressed forms, with an optional trailing dotted quad for the low 32
// bits. A zone suffix is rejected — an artifact's addresses identify hosts,
// and a zone identifies a link on one machine.
func parseAddr6(b []byte) (addr6, bool) {
	var (
		groups [8]uint16
		n      int // groups written so far
		gap    = -1
	)
	if len(b) < 2 {
		return addr6{}, false
	}
	i := 0
	// A leading "::" is the only case where the address may start with a colon.
	if b[0] == ':' {
		if b[1] != ':' {
			return addr6{}, false
		}
		gap, i = 0, 2
		if i == len(b) { // "::"
			return addr6{}, true
		}
	}
	for i < len(b) {
		// A dotted quad may only appear last and fills two groups. parseAddr4
		// demands the whole remainder be a quad, which is exactly the rule:
		// anything after it could only be another separator, and that fails here.
		if v4, ok := parseAddr4(b[i:]); ok {
			if n > 6 {
				return addr6{}, false
			}
			groups[n] = uint16(v4 >> 16)
			groups[n+1] = uint16(v4)
			n += 2
			break
		}
		v, digits := 0, 0
		for ; i < len(b) && isHex(b[i]); i++ {
			v = v<<4 | hexVal(b[i])
			if digits++; digits > 4 {
				return addr6{}, false
			}
		}
		if digits == 0 {
			return addr6{}, false
		}
		if n == 8 {
			return addr6{}, false
		}
		groups[n] = uint16(v)
		n++
		if i == len(b) {
			break
		}
		if b[i] != ':' {
			return addr6{}, false
		}
		i++
		if i < len(b) && b[i] == ':' { // "::"
			if gap >= 0 {
				return addr6{}, false // only one gap is legal
			}
			gap = n
			i++
			if i == len(b) {
				break // trailing "::"
			}
		} else if i == len(b) {
			return addr6{}, false // a trailing single colon is not an address
		}
	}

	switch {
	case gap < 0:
		if n != 8 {
			return addr6{}, false
		}
	case n >= 8:
		return addr6{}, false // "::" must stand for at least one group
	default:
		// slide the groups after the gap down to the end
		shift := 8 - n
		for j := n - 1; j >= gap; j-- {
			groups[j+shift] = groups[j]
			groups[j] = 0
		}
	}

	var a addr6
	for j := 0; j < 4; j++ {
		a.hi = a.hi<<16 | uint64(groups[j])
		a.lo = a.lo<<16 | uint64(groups[4+j])
	}
	return a, true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c <= '9':
		return int(c - '0')
	case c >= 'a':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}
