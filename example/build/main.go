// Command build constructs an artifact from a text spec, both address
// families in one file — and shows the intended way to give the opaque value
// bytes internal structure: declare a bit layout once with pkg/bits and pack
// fields into a word, instead of hand-maintaining masks that drift.
//
//	go run ./example/build
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/netstar-labs/ipmap"
	"github.com/netstar-labs/ipmap/pkg/bits"
)

// The value layout: 4 bytes per address, three fields and an explicit gap.
// ipmap never looks inside the value — the layout is this program's contract
// with itself, which is exactly why deriving the masks beats writing them.
var (
	l        bits.Layout
	kind     = l.Field(4)  // 0..15 — a category of this program's choosing
	score    = l.Field(10) // 0..1023
	firstDay = l.Field(15) // days since 2020-01-01; overflows in 2109
	_        = l.Reserve(3)
)

// The spec: address, kind, score, first-seen day. Both families, any order.
const spec = `# address        kind score day
192.0.2.1         3    812  2145
192.0.2.200       3    790  2151
2001:db8::7       9    417  1998
198.51.100.7      1     55  2400
2001:db8:1::1     9    633  2007
`

func main() {
	b := ipmap.NewBuilder(ipmap.Options{ValLen: 4})
	sc := bufio.NewScanner(strings.NewReader(spec))
	for sc.Scan() {
		line := strings.Fields(strings.TrimSpace(sc.Text()))
		if len(line) == 0 || strings.HasPrefix(line[0], "#") {
			continue
		}
		addr := netip.MustParseAddr(line[0])
		k, _ := strconv.ParseUint(line[1], 10, 64)
		s, _ := strconv.ParseUint(line[2], 10, 64)
		d, _ := strconv.ParseUint(line[3], 10, 64)
		if k > kind.Max() || s > score.Max() || d > firstDay.Max() {
			log.Fatalf("%s: a field does not fit its declared width", line[0])
		}
		var w uint64
		w = kind.Put(w, k)
		w = score.Put(w, s)
		w = firstDay.Put(w, d)
		var val [4]byte
		binary.LittleEndian.PutUint32(val[:], uint32(w))
		if err := b.Add(addr, val[:]); err != nil {
			log.Fatal(err)
		}
	}
	m, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}

	path := filepath.Join(os.TempDir(), "build-example.ipmap")
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := m.WriteTo(f); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	defer os.Remove(path)
	st := m.Stats()
	fmt.Printf("%s: %d v4 + %d v6 addresses\n", filepath.Base(path), st.Addrs4, st.Addrs6)

	// Read one back and unpack with the same declarations — no masks in sight.
	if v, ok := m.Lookup(netip.MustParseAddr("2001:db8::7")); ok {
		w := uint64(binary.LittleEndian.Uint32(v))
		fmt.Printf("2001:db8::7 -> kind=%d score=%d firstDay=%d\n",
			kind.Get(w), score.Get(w), firstDay.Get(w))
	}
}
