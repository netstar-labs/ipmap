// Command intern builds the same data twice — values interned and not — and
// prints what interning costs and buys. Observation data repeats values
// heavily (the feed that motivated ipmap ran 312 addresses per distinct
// value); interning stores each distinct value once and a small id per entry,
// and changes nothing a caller can observe: the same bytes come back.
//
//	go run ./example/intern
package main

import (
	"bytes"
	"fmt"
	"log"
	"net/netip"

	"github.com/netstar-labs/ipmap"
)

func main() {
	// 60,000 128-bit addresses drawn from 40 distinct 8-byte values: the
	// repetition real feeds have, at a size that builds in milliseconds. The
	// 128-bit family is used here because its artifact is all payload; a
	// 32-bit artifact carries a fixed 67 MB index that would drown the ratio
	// at toy scale (at real scale it is 4.6 bytes per address — see the
	// sizing note in docs/userguide.md).
	addr := func(i int) netip.Addr {
		return netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 8: byte(i >> 16), byte(i >> 8), byte(i), 15: 0x01})
	}
	build := func(intern bool) *ipmap.Map {
		b := ipmap.NewBuilder(ipmap.Options{ValLen: 8, Intern: intern})
		for i := 0; i < 60_000; i++ {
			v := [8]byte{0xC0, byte(i % 40), 0xFF, 0xEE, 0, 0, byte(i % 40), 1}
			if err := b.Add(addr(i), v[:]); err != nil {
				log.Fatal(err)
			}
		}
		m, err := b.Build()
		if err != nil {
			log.Fatal(err)
		}
		return m
	}
	size := func(m *ipmap.Map) int64 {
		var buf bytes.Buffer
		n, err := m.WriteTo(&buf)
		if err != nil {
			log.Fatal(err)
		}
		return n
	}

	direct, interned := build(false), build(true)
	ds, is := size(direct), size(interned)
	fmt.Printf("direct    %8d bytes\n", ds)
	fmt.Printf("interned  %8d bytes  (%d distinct values, %.1f%% of direct)\n",
		is, interned.Stats().Distinct, 100*float64(is)/float64(ds))

	// The contract: interning is invisible to lookups. Same keys, same bytes.
	for i := 0; i < 60_000; i++ {
		dv, dok := direct.Lookup(addr(i))
		iv, iok := interned.Lookup(addr(i))
		if !dok || !iok || !bytes.Equal(dv, iv) {
			log.Fatalf("interning changed an answer at %v", addr(i))
		}
	}
	fmt.Println("60,000 lookups: interned and direct answers are byte-identical")
}
