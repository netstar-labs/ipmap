package ipmap

import "fmt"

// values resolves an entry position to its value bytes, in one of two layouts:
//
//   - direct: one ValLen-byte value per entry, in entry order. Simple, and the
//     right choice when values rarely repeat.
//   - interned: a table of the distinct values, plus a fixed-width id per
//     entry. When many addresses share a value — the common case for
//     observation data — the table collapses and each entry pays only the id.
//
// The id width is derived from the distinct count at build time, never
// assumed: 1, 2, 3 or 4 bytes, the narrowest that holds every id. Both
// families of a Map share one table; a value's identity has no family.
type values struct {
	valLen int

	direct []byte // direct layout: valLen bytes per entry; nil when interned

	tab []byte // interned layout: the distinct values, valLen bytes each
	ids []byte // idW bytes per entry, little-endian
	idW int
}

// get returns entry i's value, capacity-capped so a caller's append cannot
// write into the store. Zero allocations on either layout.
func (v *values) get(i int) []byte {
	if v.direct != nil {
		return v.direct[i*v.valLen : (i+1)*v.valLen : (i+1)*v.valLen]
	}
	id := v.id(i)
	return v.tab[id*v.valLen : (id+1)*v.valLen : (id+1)*v.valLen]
}

// id decodes entry i's packed value id. This is the one decoder: the open-time
// validator bounds ids with the same code the lookup path decodes with, so the
// two cannot drift — a drifted decode would turn the open-time guarantee into
// a query-time panic. Small enough to inline everywhere it is called.
func (v *values) id(i int) int {
	id := 0
	for b := v.idW - 1; b >= 0; b-- {
		id = id<<8 | int(v.ids[i*v.idW+b])
	}
	return id
}

// idWidth is the narrowest byte width that holds ids for n distinct values
// (ids run 0..n-1).
func idWidth(n int) int {
	switch {
	case n <= 1<<8:
		return 1
	case n <= 1<<16:
		return 2
	case n <= 1<<24:
		return 3
	default:
		return 4
	}
}

// putID packs id into entry i's slot. The width was derived from the distinct
// count, so an id that does not fit means the build's own bookkeeping is
// wrong — that is a bug, and it dies here rather than corrupting a neighbour
// and surfacing as a wrong answer at query time.
func (v *values) putID(i int, id uint32) {
	if v.idW < 4 && id >= 1<<(8*v.idW) {
		panic(fmt.Sprintf("ipmap: id %d does not fit %d-byte width", id, v.idW))
	}
	for b := 0; b < v.idW; b++ {
		v.ids[i*v.idW+b] = byte(id >> (8 * b))
	}
}
