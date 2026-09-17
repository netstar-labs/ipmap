//go:build unix

package ipmap

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The scale sweep (T-P8): build and lookup at 1×, 4× and 10× of a 10M-entry
// base, per family, hit and miss measured separately — and miss measured
// twice, because the two kinds are different code paths: a probe into a group
// that holds nothing (index says empty, no scan) and a probe into a populated
// group (scanned or binary-searched, then absent). Only the second can degrade
// as groups fill, so averaging them would hide exactly the curve this sweep
// exists to draw.
//
// Everything here is opt-in via IPMAP_SCALE: the sweep allocates gigabytes and
// runs for minutes, which is a measurement session, not a test run.
//
//	IPMAP_SCALE=1 go test -run TestBuildMemoryCurve -v
//	IPMAP_SCALE=1 go test -run xxx -bench BenchmarkScale -timeout 60m -v
//
// Leave -benchtime at its default: a full-scale build exceeds it and so runs
// exactly once, while the lookup benchmarks need the whole second — forcing
// -benchtime 1x would time a single cold probe and call it a number.
//
// The distribution is clustered, not uniform (T-P8 caveat): 32-bit entries
// arrive as runs of 1-40 suffixes within a shared /24, 128-bit entries as 1-6
// suffixes under a shared /64 — the shape of scanner and sensor feeds, per the
// corpus measurements in docs/architecture.md.
const scaleBase = 10_000_000

// scaleBaseN is the sweep's 1× size: scaleBase, unless IPMAP_SCALE itself is a
// number — a smaller base is the smoke path for exercising the machinery
// without the minutes.
func scaleBaseN() int {
	if v, err := strconv.Atoi(os.Getenv("IPMAP_SCALE")); err == nil && v >= 1000 {
		return v
	}
	return scaleBase
}

// scaleValLen is the sweep's value width: 4 bytes, direct (uninterned) — the
// layout with per-entry cost, so build memory scales visibly with n.
const scaleValLen = 4

func scaleVal4(ip uint32) []byte {
	return []byte{byte(ip>>24) ^ 0xA5, byte(ip >> 16), byte(ip >> 8), byte(ip)}
}

func scaleVal6(a addr6) []byte {
	x := a.hi ^ a.lo*0x9E3779B97F4A7C15
	return []byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)}
}

// scale4 generates ~n clustered 32-bit entries plus disjoint probe sets.
// Cluster bases are assigned bijectively over the even /24 indices, so no two
// clusters share a group and a group's excluded suffix is a guaranteed miss;
// the odd /24 indices are never populated and supply the empty-group misses.
type scale4 struct {
	addrs     []uint32
	hit       []netip.Addr // members, sampled and shuffled
	missGroup []netip.Addr // absent suffix inside a populated /24
	missEmpty []netip.Addr // address in a /24 that holds nothing
}

func genScale4(n int, probes bool) *scale4 {
	r := rand.New(rand.NewPCG(uint64(n), 0x5ca1e))
	g := &scale4{addrs: make([]uint32, 0, n+40)}
	var missG []uint32
	for cluster := 0; len(g.addrs) < n; cluster++ {
		// Bijective scatter over 2^23 even /24 indices: odd multiplier mod 2^23.
		if cluster == 1<<23 {
			panic("genScale4: /24 space exhausted — bases would repeat and poison the miss probes")
		}
		base := (uint32(cluster) * 2654435761 & (1<<23 - 1)) << 9
		excl := uint32(r.IntN(256))
		if probes && len(missG) < 1_000_000 {
			missG = append(missG, base|excl)
		}
		for k, kn := 0, 1+r.IntN(40); k < kn; k++ {
			s := (excl + 1 + uint32(r.IntN(255))) & 255 // any suffix but excl
			g.addrs = append(g.addrs, base|s)
		}
	}
	if !probes {
		return g
	}
	sample := max(1, n/2_000_000)
	for i := 0; i < len(g.addrs); i += sample {
		g.hit = append(g.hit, v4addr(g.addrs[i]))
	}
	r.Shuffle(len(g.hit), func(i, j int) { g.hit[i], g.hit[j] = g.hit[j], g.hit[i] })
	r.Shuffle(len(missG), func(i, j int) { missG[i], missG[j] = missG[j], missG[i] })
	for _, ip := range missG {
		g.missGroup = append(g.missGroup, v4addr(ip))
	}
	for i := 0; i < min(1_000_000, n/10); i++ {
		g.missEmpty = append(g.missEmpty, v4addr(r.Uint32()|1<<8)) // odd /24 index
	}
	return g
}

// scale6 is the 128-bit twin: clusters of 1-6 suffixes under bijectively
// distinct even high words; odd high words are never populated.
type scale6 struct {
	addrs     []addr6
	hit       []netip.Addr
	missGroup []netip.Addr
	missEmpty []netip.Addr
}

func genScale6(n int, probes bool) *scale6 {
	r := rand.New(rand.NewPCG(uint64(n), 0x5ca1e6))
	g := &scale6{addrs: make([]addr6, 0, n+6)}
	var missG []addr6
	for cluster := 0; len(g.addrs) < n; cluster++ {
		hi := uint64(cluster) * 0x9E3779B97F4A7C15 << 1 // bijective, even
		excl := r.Uint64()
		if probes && len(missG) < 1_000_000 {
			missG = append(missG, addr6{hi: hi, lo: excl})
		}
		for k, kn := 0, 1+r.IntN(6); k < kn; k++ {
			lo := excl + 1 + uint64(r.IntN(1<<30)) // never wraps back to excl
			g.addrs = append(g.addrs, addr6{hi: hi, lo: lo})
		}
	}
	if !probes {
		return g
	}
	sample := max(1, n/2_000_000)
	for i := 0; i < len(g.addrs); i += sample {
		g.hit = append(g.hit, g.addrs[i].addr())
	}
	r.Shuffle(len(g.hit), func(i, j int) { g.hit[i], g.hit[j] = g.hit[j], g.hit[i] })
	r.Shuffle(len(missG), func(i, j int) { missG[i], missG[j] = missG[j], missG[i] })
	for _, a := range missG {
		g.missGroup = append(g.missGroup, a.addr())
	}
	for i := 0; i < min(1_000_000, n/10); i++ {
		g.missEmpty = append(g.missEmpty, addr6{hi: r.Uint64() | 1, lo: r.Uint64()}.addr())
	}
	return g
}

// buildScale4 / buildScale6 run the real Add loop and Build, timing them
// separately: Add is the streaming half a feed pays per line, Build is the
// sort-dedupe-fill half paid once at the end.
func buildScale4(tb testing.TB, addrs []uint32) (*Map, time.Duration, time.Duration) {
	tb.Helper()
	b := NewBuilder(Options{ValLen: scaleValLen})
	t0 := time.Now()
	for _, ip := range addrs {
		if err := b.Add(v4addr(ip), scaleVal4(ip)); err != nil {
			tb.Fatal(err)
		}
	}
	t1 := time.Now()
	m, err := b.Build()
	if err != nil {
		tb.Fatal(err)
	}
	return m, t1.Sub(t0), time.Since(t1)
}

func buildScale6(tb testing.TB, addrs []addr6) (*Map, time.Duration, time.Duration) {
	tb.Helper()
	b := NewBuilder(Options{ValLen: scaleValLen})
	t0 := time.Now()
	for _, a := range addrs {
		if err := b.Add(a.addr(), scaleVal6(a)); err != nil {
			tb.Fatal(err)
		}
	}
	t1 := time.Now()
	m, err := b.Build()
	if err != nil {
		tb.Fatal(err)
	}
	return m, t1.Sub(t0), time.Since(t1)
}

func skipUnlessScale(tb testing.TB) {
	tb.Helper()
	if os.Getenv("IPMAP_SCALE") == "" {
		tb.Skip("IPMAP_SCALE not set; the scale sweep allocates gigabytes and runs minutes — a measurement session, run locally")
	}
}

// BenchmarkScale is the sweep: per family and per multiplier, one build
// (reporting the Add and Build halves separately) and the three lookup
// variants against the map it produced.
func BenchmarkScale(b *testing.B) {
	skipUnlessScale(b)
	lookupBench := func(m *Map, probes []netip.Addr, wantHit bool) func(*testing.B) {
		return func(b *testing.B) {
			b.ReportAllocs()
			hits := 0
			i := 0
			for b.Loop() {
				if _, ok := m.Lookup(probes[i]); ok {
					hits++
				}
				if i++; i == len(probes) {
					i = 0
				}
			}
			if wantHit && hits != b.N {
				b.Fatalf("hit probes missed: %d of %d", b.N-hits, b.N)
			}
			if !wantHit && hits != 0 {
				b.Fatalf("miss probes hit %d times — the probe sets are broken, not the map", hits)
			}
		}
	}
	for _, mult := range []int{1, 4, 10} {
		n := scaleBaseN() * mult
		func() { // scope each family+scale so its gigabytes release before the next
			g := genScale4(n, true)
			var m *Map
			b.Run(fmt.Sprintf("v4/%dx/build", mult), func(b *testing.B) {
				for b.Loop() {
					var addD, bldD time.Duration
					m, addD, bldD = buildScale4(b, g.addrs)
					b.ReportMetric(addD.Seconds(), "add-s/op")
					b.ReportMetric(bldD.Seconds(), "build-s/op")
				}
			})
			b.Logf("v4/%dx: %d added, %d kept", mult, len(g.addrs), m.Stats().Addrs4)
			b.Run(fmt.Sprintf("v4/%dx/hit", mult), lookupBench(m, g.hit, true))
			b.Run(fmt.Sprintf("v4/%dx/miss-group", mult), lookupBench(m, g.missGroup, false))
			b.Run(fmt.Sprintf("v4/%dx/miss-empty", mult), lookupBench(m, g.missEmpty, false))
		}()
		runtime.GC()
		func() {
			g := genScale6(n, true)
			var m *Map
			b.Run(fmt.Sprintf("v6/%dx/build", mult), func(b *testing.B) {
				for b.Loop() {
					var addD, bldD time.Duration
					m, addD, bldD = buildScale6(b, g.addrs)
					b.ReportMetric(addD.Seconds(), "add-s/op")
					b.ReportMetric(bldD.Seconds(), "build-s/op")
				}
			})
			b.Logf("v6/%dx: %d added, %d kept across %d prefixes", mult, len(g.addrs), m.Stats().Addrs6, len(m.s6.prefix))
			b.Run(fmt.Sprintf("v6/%dx/hit", mult), lookupBench(m, g.hit, true))
			b.Run(fmt.Sprintf("v6/%dx/miss-group", mult), lookupBench(m, g.missGroup, false))
			b.Run(fmt.Sprintf("v6/%dx/miss-empty", mult), lookupBench(m, g.missEmpty, false))
		}()
		runtime.GC()
	}
}

// BenchmarkBuild is the inventory's build benchmark at everyday size: 200k
// clustered entries per family, the whole Add-then-Build life per iteration.
// The scale sweep above runs the same path at 1×/4×/10×.
func BenchmarkBuild(b *testing.B) {
	g4 := genScale4(200_000, false)
	b.Run("v4", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bl := NewBuilder(Options{ValLen: scaleValLen})
			for _, ip := range g4.addrs {
				if err := bl.Add(v4addr(ip), scaleVal4(ip)); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := bl.Build(); err != nil {
				b.Fatal(err)
			}
		}
	})
	g6 := genScale6(200_000, false)
	b.Run("v6", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bl := NewBuilder(Options{ValLen: scaleValLen})
			for _, a := range g6.addrs {
				if err := bl.Add(a.addr(), scaleVal6(a)); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := bl.Build(); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// maxRSS reports this process's peak resident set in bytes. Darwin reports
// bytes; Linux reports KiB.
func maxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	rss := int64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return rss
}

// TestScaleBuildChild is not a test: it is the subprocess body for
// TestBuildMemoryCurve. Peak RSS is a per-process high-water mark that nothing
// can reset, so each (family, scale) point must be measured in a process of
// its own — an in-process heap sample would understate the sort's real cost
// and be contaminated by every case before it.
func TestScaleBuildChild(t *testing.T) {
	spec := os.Getenv("IPMAP_SCALE_CHILD") // "<family>:<n>"
	if spec == "" {
		t.Skip("subprocess body for TestBuildMemoryCurve")
	}
	fam, ns, _ := strings.Cut(spec, ":")
	n, err := strconv.Atoi(ns)
	if err != nil {
		t.Fatal(err)
	}
	var baseline int64 // the input's own footprint, sampled after generation
	var addD, bldD time.Duration
	var kept int
	switch fam {
	case "4":
		g := genScale4(n, false) // no probe arrays: input only
		baseline = maxRSS()
		var m *Map
		m, addD, bldD = buildScale4(t, g.addrs)
		kept = m.Stats().Addrs4
	case "6":
		g := genScale6(n, false)
		baseline = maxRSS()
		var m *Map
		m, addD, bldD = buildScale6(t, g.addrs)
		kept = m.Stats().Addrs6
	default:
		t.Fatalf("bad family %q", fam)
	}
	fmt.Printf("CHILD family=%s n=%d kept=%d baseline=%d maxrss=%d add=%.2f build=%.2f\n",
		fam, n, kept, baseline, maxRSS(), addD.Seconds(), bldD.Seconds())
}

// TestBuildMemoryCurve measures peak build memory per (family, scale) in a
// fresh subprocess each, then reports bytes per entry so the number in
// docs/architecture.md is a measured function of n, not a guess. The input
// generator's own arrays are measured as the baseline and reported alongside:
// the builder's peak is what an embedding program pays on top of holding its
// own input.
func TestBuildMemoryCurve(t *testing.T) {
	skipUnlessScale(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range []string{"4", "6"} {
		for _, mult := range []int{1, 4, 10} {
			n := scaleBaseN() * mult
			cmd := exec.Command(self, "-test.run=TestScaleBuildChild$", "-test.v")
			cmd.Env = append(os.Environ(), fmt.Sprintf("IPMAP_SCALE_CHILD=%s:%d", fam, n))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child %s:%d: %v\n%s", fam, n, err, out)
			}
			line := ""
			for sc := bufio.NewScanner(strings.NewReader(string(out))); sc.Scan(); {
				if strings.HasPrefix(sc.Text(), "CHILD ") {
					line = sc.Text()
				}
			}
			if line == "" {
				t.Fatalf("child %s:%d printed no report:\n%s", fam, n, out)
			}
			f := map[string]float64{}
			for _, kv := range strings.Fields(line)[1:] {
				k, v, _ := strings.Cut(kv, "=")
				f[k], _ = strconv.ParseFloat(v, 64)
			}
			buildPeak := f["maxrss"] - f["baseline"]
			t.Logf("v%s %2dx: n=%9.0f kept=%9.0f peak=%6.0f MB (input baseline %5.0f MB) → %5.1f B/entry over input; add %5.1fs build %5.1fs",
				fam, mult, f["n"], f["kept"], f["maxrss"]/1e6, f["baseline"]/1e6, buildPeak/f["n"], f["add"], f["build"])
		}
	}
}
