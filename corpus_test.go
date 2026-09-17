package ipmap

import (
	"bufio"
	"bytes"
	"hash/crc64"
	"io"
	"net/netip"
	"os"
	"path/filepath"
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

// valC derives a deliberately low-cardinality value (≤65536 per salt) so the
// interned path is what the corpus actually exercises: high-entropy values
// would make every value distinct, which tests nothing the direct layout did
// not already cover — and the dedupe map would rival the store for memory.
func valC(a netip.Addr, salt byte) []byte {
	b := a.As16()
	var h uint16
	for i, c := range b {
		h = h*31 + uint16(c) + uint16(i)
	}
	out := make([]byte, testValLen)
	for i := range out {
		out[i] = byte(h>>(8*(i%2))) + byte(i)*7 + salt
	}
	out[0] |= 1
	return out
}

func corpusRun(t *testing.T, path, expect string) {
	if path == "" {
		t.Skip("not set")
	}

	// tracked bounds the test's memory: the first trackCap distinct addresses
	// get duplicate bookkeeping and salted-conflict injection; the rest are
	// added with salt 0, so their expected value needs no state at all.
	const trackCap = 2_000_000
	type track struct {
		salt   byte   // current value salt; bumping it injects a conflict
		adds   uint32 // every add of this address
		atSalt uint32 // adds since the last bump — these agree with the survivor
	}
	tracked := make(map[netip.Addr]track, trackCap)

	b := NewBuilder(Options{ValLen: testValLen, Intern: true})
	var lines int
	start := time.Now()
	scan(t, path, func(line []byte) {
		a, ok := parseAny(line)
		if !ok {
			t.Fatalf("unparseable address %q", line)
		}
		a = a.Unmap()
		tr, seen := tracked[a]
		if seen {
			// Inject a conflicting re-add now and then. The salt never wraps:
			// valC is distinct across salts only within one 256-run, and a wrap
			// would let a drop collide with the survivor and skew the tally.
			if lines%16 == 0 && tr.salt < 255 {
				tr.salt++
				tr.atSalt = 0
			}
			tr.adds++
			tr.atSalt++
			tracked[a] = tr
		} else if len(tracked) < trackCap {
			tr = track{adds: 1, atSalt: 1}
			tracked[a] = tr
		} else {
			tr = track{} // untracked: always salt 0, so duplicates always agree
		}
		if err := b.Add(a, valC(a, tr.salt)); err != nil {
			t.Fatal(err)
		}
		lines++
	})
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	// The independent conflict tally, survivor semantics: every add before an
	// address's final salt carries a value different from the one that wins.
	var conflicts int
	for _, tr := range tracked {
		conflicts += int(tr.adds - tr.atSalt)
	}
	st := m.Stats()
	unique := st.Addrs4 + st.Addrs6
	t.Logf("built %d lines → %d unique in %s (dups=%d conflicts=%d, interned %d distinct values)",
		lines, unique, time.Since(start).Round(time.Millisecond), st.Dups, st.DupConflicts, st.Distinct)

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

	// Through the artifact: write it, open it, and require the re-serialisation
	// of the opened map to hash identically to the file — accepted implies
	// canonical, at corpus scale.
	start = time.Now()
	ap := filepath.Join(t.TempDir(), "corpus.ipmap")
	af, err := os.Create(ap)
	if err != nil {
		t.Fatal(err)
	}
	h1 := crc64.New(crc64.MakeTable(crc64.ECMA))
	size, err := m.WriteTo(io.MultiWriter(af, h1))
	if err != nil {
		t.Fatal(err)
	}
	if err := af.Close(); err != nil {
		t.Fatal(err)
	}
	wrote := time.Since(start)
	start = time.Now()
	opened, err := Open(ap)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	openT := time.Since(start)
	h2 := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := opened.WriteTo(h2); err != nil {
		t.Fatal(err)
	}
	if h1.Sum64() != h2.Sum64() {
		t.Fatal("the opened artifact does not re-serialise identically")
	}
	if opened.Stats() != m.Stats() {
		t.Fatalf("stats changed through the file: %+v vs %+v", opened.Stats(), m.Stats())
	}
	t.Logf("artifact: %d bytes, written in %s, opened+verified in %s", size, wrote.Round(time.Millisecond), openT.Round(time.Millisecond))

	// The exhaustive pass: re-stream the input and verify every line against
	// the FILE-backed map — every unique address appears as at least one line,
	// so this covers all keys with no test-side table. A sample once passed a
	// store that was wrong for 45 keys in 10^8. The built map is probed too:
	// the two must agree everywhere.
	start = time.Now()
	var checked int
	var dst [testValLen]byte
	scan(t, path, func(line []byte) {
		a, _ := parseAny(line)
		a = a.Unmap()
		salt := tracked[a].salt // zero for untracked, by construction
		got, ok := opened.Lookup(a)
		if !ok || !bytes.Equal(got, valC(a, salt)) {
			t.Fatalf("opened Lookup(%v) = %x,%v; want %x", a, got, ok, valC(a, salt))
		}
		if built, bok := m.Lookup(a); !bok || !bytes.Equal(built, got) {
			t.Fatalf("built and opened maps disagree at %v", a)
		}
		if !opened.LookupInto(a, dst[:]) || !bytes.Equal(dst[:], got) {
			t.Fatalf("LookupInto(%v) disagrees with Lookup", a)
		}
		checked++
	})
	t.Logf("exhaustive through the artifact: %d lookups verified in %s", checked, time.Since(start).Round(time.Millisecond))
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
