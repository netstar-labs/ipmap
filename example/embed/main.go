// Command embed demonstrates using the library from another program — the
// full life of an artifact: build both families, write one file, mmap it back,
// and query it, hit and miss, from the file-backed map.
//
//	go run ./example/embed
package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/netstar-labs/ipmap"
)

func main() {
	// Build: fixed-width values (4 bytes here), any mix of families, duplicates
	// legal (last wins). Intern pays off when many addresses share a value.
	b := ipmap.NewBuilder(ipmap.Options{ValLen: 4, Intern: true})
	entries := []struct {
		addr  string
		value string
	}{
		{"192.0.2.1", "beef"},
		{"192.0.2.9", "beef"}, // same value: interning stores it once
		{"198.51.100.7", "cafe"},
		{"2001:db8::1", "beef"},
		{"2001:db8::2", "f00d"},
	}
	for _, e := range entries {
		var v [4]byte
		copy(v[:], e.value)
		if err := b.Add(netip.MustParseAddr(e.addr), v[:]); err != nil {
			log.Fatal(err)
		}
	}
	m, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}

	// Write: one artifact, both families, checksummed throughout.
	path := filepath.Join(os.TempDir(), "embed-example.ipmap")
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

	// Open: mmap and verify — a file that opens is a file that passed. Serve
	// from the returned Map; values alias the mapping, valid until Close.
	db, err := ipmap.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	st := db.Stats()
	fmt.Printf("%s: %d v4 + %d v6 addresses, %d distinct values interned\n",
		filepath.Base(path), st.Addrs4, st.Addrs6, st.Distinct)

	for _, probe := range []string{"192.0.2.9", "::ffff:192.0.2.9", "2001:db8::2", "203.0.113.1"} {
		a := netip.MustParseAddr(probe)
		if v, ok := db.Lookup(a); ok { // zero-alloc: v aliases the mapping
			fmt.Printf("%-18s -> %q\n", probe, v)
		} else {
			fmt.Printf("%-18s -> miss\n", probe)
		}
	}

	// LookupInto for callers that want their own buffer instead of an alias.
	dst := make([]byte, db.ValLen())
	if db.LookupInto(netip.MustParseAddr("198.51.100.7"), dst) {
		fmt.Printf("%-18s -> %q (copied)\n", "198.51.100.7", dst)
	}
}
