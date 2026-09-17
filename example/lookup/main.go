// Command lookup opens an artifact and queries it — hit, miss, owned-buffer —
// and then demonstrates the zero-downtime reload: many goroutines querying
// through one atomic pointer while the artifact underneath is swapped for a
// newly built one. No reader ever locks, blocks, or sees a partial state.
//
//	go run ./example/lookup
package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/netstar-labs/ipmap"
)

// buildArtifact writes a tiny two-family artifact whose values carry tag, so
// the swap below is observable: v1 answers "aa..", v2 answers "bb..".
func buildArtifact(path string, tag byte) {
	b := ipmap.NewBuilder(ipmap.Options{ValLen: 2})
	for _, a := range []string{"192.0.2.1", "198.51.100.7", "2001:db8::7"} {
		if err := b.Add(netip.MustParseAddr(a), []byte{tag, tag}); err != nil {
			log.Fatal(err)
		}
	}
	m, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}
	// Temp-and-rename, so a name that exists is always a complete artifact.
	f, err := os.Create(path + ".tmp")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := m.WriteTo(f); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		log.Fatal(err)
	}
}

func main() {
	path := filepath.Join(os.TempDir(), "lookup-example.ipmap")
	defer os.Remove(path)

	buildArtifact(path, 0xAA)
	m, err := ipmap.Open(path)
	if err != nil {
		log.Fatal(err)
	}

	// The basics: hit, miss, and a caller-owned buffer.
	hit := netip.MustParseAddr("2001:db8::7")
	if v, ok := m.Lookup(hit); ok {
		fmt.Printf("%-14s -> %x   (aliases the mapping; copy it to keep it)\n", hit, v)
	}
	if _, ok := m.Lookup(netip.MustParseAddr("203.0.113.9")); !ok {
		fmt.Printf("%-14s -> miss\n", "203.0.113.9")
	}
	dst := make([]byte, m.ValLen())
	if m.LookupInto(hit, dst) {
		fmt.Printf("%-14s -> %x   (LookupInto: your buffer, your lifetime)\n", hit, dst)
	}

	// The reload: readers query through one atomic pointer, never a lock. The
	// old mapping stays valid for readers mid-flight — an mmap holds the file's
	// content, not its name, so rename-and-reopen disturbs nobody.
	var cur atomic.Pointer[ipmap.Map]
	cur.Store(m)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	sawNew := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if v, ok := cur.Load().Lookup(hit); ok && v[0] == 0xBB {
					select {
					case sawNew <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	buildArtifact(path, 0xBB) // build v2 and rename it over v1's name
	m2, err := ipmap.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	old := cur.Swap(m2) // the whole cutover: one atomic store
	<-sawNew            // readers are answering from v2
	close(stop)
	wg.Wait()
	// Close the old mapping only after its readers are provably done — Close
	// unmaps, and a value returned by the old Map dies with it. Here the wait
	// group is that proof; a server would use a grace period or a refcount,
	// or simply keep the old mapping until process exit.
	old.Close()
	defer m2.Close()

	v, _ := cur.Load().Lookup(hit)
	fmt.Printf("%-14s -> %x   (after the swap; no reader ever blocked)\n", hit, v)
}
