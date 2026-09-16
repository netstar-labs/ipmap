package ipmap

import (
	"bufio"
	"bytes"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"
)

// The real-world oracle (T5, R4): address lists too large and too real to
// commit, supplied locally — one address per line, duplicates and all. Values
// are derived from the address (val), so the expected answer needs no table:
// the exhaustive pass re-streams the file and checks every line, which keeps
// the test's own memory flat no matter how large the corpus is.
//
//	IPMAP_CORPUS4=v4.addrs IPMAP_CORPUS6=v6.addrs go test -run Corpus -v
//
// Optional IPMAP_EXPECT4 / IPMAP_EXPECT6 assert the unique-address count
// against a number computed independently (sort -u | wc -l). CI sets none of
// these; the generated oracles are its coverage.
func TestCorpusExhaustive(t *testing.T) {
	if os.Getenv("IPMAP_CORPUS4") == "" && os.Getenv("IPMAP_CORPUS6") == "" {
		t.Skip("IPMAP_CORPUS4 / IPMAP_CORPUS6 not set; the real-world oracle runs locally only")
	}
	t.Run("v4", func(t *testing.T) { corpusRun(t, os.Getenv("IPMAP_CORPUS4"), os.Getenv("IPMAP_EXPECT4")) })
	t.Run("v6", func(t *testing.T) { corpusRun(t, os.Getenv("IPMAP_CORPUS6"), os.Getenv("IPMAP_EXPECT6")) })
}

func corpusRun(t *testing.T, path, expect string) {
	if path == "" {
		t.Skip("not set")
	}

	// tracked bounds the test's memory: the first trackCap distinct addresses
	// get duplicate bookkeeping and salted-conflict injection; the rest are
	// added with salt 0, so their expected value needs no state at all.
	const trackCap = 2_000_000
	tracked := make(map[netip.Addr]byte, trackCap)

	b := NewBuilder(Options{ValLen: testValLen})
	var lines, conflicts int
	start := time.Now()
	scan(t, path, func(line []byte) {
		a, ok := parseAny(line)
		if !ok {
			t.Fatalf("unparseable address %q", line)
		}
		a = a.Unmap()
		salt, seen := tracked[a]
		if seen {
			if lines%16 == 0 { // inject a conflicting re-add now and then
				salt++
				conflicts++
			}
			tracked[a] = salt
		} else if len(tracked) < trackCap {
			tracked[a] = 0
		}
		if err := b.Add(a, val(a, salt)); err != nil {
			t.Fatal(err)
		}
		lines++
	})
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	st := m.Stats()
	unique := st.Addrs4 + st.Addrs6
	t.Logf("built %d lines → %d unique in %s (dups=%d conflicts=%d)",
		lines, unique, time.Since(start).Round(time.Millisecond), st.Dups, st.DupConflicts)

	if st.Dups != lines-unique {
		t.Fatalf("Dups = %d, want lines-unique = %d", st.Dups, lines-unique)
	}
	// Conflicts are injected only on tracked addresses, and untracked
	// duplicates always agree (salt 0), so the builder's count must equal the
	// injection count exactly — an independent tally, not the builder's own.
	if st.DupConflicts != conflicts {
		t.Fatalf("DupConflicts = %d, injected %d", st.DupConflicts, conflicts)
	}
	if expect != "" {
		want, err := strconv.Atoi(expect)
		if err != nil {
			t.Fatal(err)
		}
		if unique != want {
			t.Fatalf("unique = %d, independent count says %d", unique, want)
		}
	}

	// The exhaustive pass: re-stream the file and verify every line — every
	// unique address appears as at least one line, so this covers all keys
	// with no test-side table. A sample once passed a store that was wrong
	// for 45 keys in 10^8.
	start = time.Now()
	var checked int
	var dst [testValLen]byte
	scan(t, path, func(line []byte) {
		a, _ := parseAny(line)
		a = a.Unmap()
		salt := tracked[a] // zero for untracked, by construction
		got, ok := m.Lookup(a)
		if !ok || !bytes.Equal(got, val(a, salt)) {
			t.Fatalf("Lookup(%v) = %x,%v; want %x", a, got, ok, val(a, salt))
		}
		if !m.LookupInto(a, dst[:]) || !bytes.Equal(dst[:], got) {
			t.Fatalf("LookupInto(%v) disagrees with Lookup", a)
		}
		checked++
	})
	t.Logf("exhaustive: %d lookups verified in %s", checked, time.Since(start).Round(time.Millisecond))
}

func scan(t *testing.T, path string, fn func(line []byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			fn(sc.Bytes())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

// parseAny routes a line to whichever family parser matches its syntax.
func parseAny(b []byte) (netip.Addr, bool) {
	for _, c := range b {
		if c == ':' {
			a, ok := parseAddr6(b)
			return a.addr(), ok
		}
	}
	ip, ok := parseAddr4(b)
	return v4addr(ip), ok
}
