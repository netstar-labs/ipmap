package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/netstar-labs/ipmap"
)

// exec runs the real command path — run(), not a parallel reimplementation —
// and returns exit code and both streams.
func exec(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

const spec = `# a comment, then a blank line

1.2.3.4        0a1b2c
2001:db8::1    0a1b2c
9.9.9.9        ffffff
9.9.9.9        eeeeee
`

func buildArtifact(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "in.spec")
	if err := os.WriteFile(sp, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "out.ipmap")
	args := append([]string{"build", "-in", sp, "-out", db}, extra...)
	code, _, errs := exec(t, "", args...)
	if code != 0 {
		t.Fatalf("build exited %d: %s", code, errs)
	}
	return db
}

func TestBuildReceipt(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "in.spec")
	if err := os.WriteFile(sp, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "out.ipmap")
	code, _, errs := exec(t, "", "build", "-in", sp, "-out", db, "-intern")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	// The receipt is the operator's one line of truth; pin it exactly.
	want := "ipmap: " + db + ": 2 v4 + 1 v6 addresses, 1 duplicates dropped (1 conflicting), 3 distinct values interned\n"
	if errs != want {
		t.Fatalf("receipt:\n got %q\nwant %q", errs, want)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmps) != 0 {
		t.Fatalf("temp files survived a successful build: %v", tmps)
	}
}

// The temp file's name used to be the fixed <out>.tmp, which a pre-planted
// symlink could redirect: os.Create followed it and the build overwrote
// whatever it pointed at. A unique name opened with O_EXCL cannot be steered
// that way, and the planted link must come through untouched.
func TestBuildDoesNotFollowPlantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "in.spec")
	if err := os.WriteFile(sp, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	const precious = "do not overwrite"
	if err := os.WriteFile(victim, []byte(precious), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "out.ipmap")
	if err := os.Symlink(victim, db+".tmp"); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if code, _, errs := exec(t, "", "build", "-in", sp, "-out", db); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != precious {
		t.Fatalf("the symlink target was written through: %q, %v", got, err)
	}
	if code, _, errs := exec(t, "", "verify", "-db", db); code != 0 {
		t.Fatalf("the artifact is not valid: exit %d: %s", code, errs)
	}
}

// The artifact's mode is whatever umask leaves of 0666, as os.Create gives.
// os.CreateTemp would make it 0600 and silently lock out readers running as
// another user, so the temp file is opened by hand; this pins that choice.
func TestBuildOutputModeMatchesCreate(t *testing.T) {
	db := buildArtifact(t)
	dir := filepath.Dir(db)
	ref := filepath.Join(dir, "ref")
	f, err := os.Create(ref)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	want, err := os.Stat(ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode() != want.Mode() {
		t.Fatalf("artifact mode %v, os.Create gives %v", got.Mode(), want.Mode())
	}
}

// Builds racing to one -out used to share a single temp file: interleaved
// writes produced corrupt bytes under the final name, and one build's rename
// stole another's, failing it. With a temp file per build every one succeeds
// and what lands is a whole artifact.
func TestConcurrentBuildsToOneOutput(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "in.spec")
	if err := os.WriteFile(sp, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "out.ipmap")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			if code := run([]string{"build", "-in", sp, "-out", db}, strings.NewReader(""), &out, &errb); code != 0 {
				t.Errorf("concurrent build exited %d: %s", code, errb.String())
			}
		}()
	}
	wg.Wait()
	if code, _, errs := exec(t, "", "verify", "-db", db); code != 0 {
		t.Fatalf("the artifact is not valid: exit %d: %s", code, errs)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmps) != 0 {
		t.Fatalf("temp files left behind: %v", tmps)
	}
}

// -in - reads the spec from stdin, as the flag help promises; errors then cite
// "stdin" as the file name. The artifact must be byte-identical to a file build.
func TestBuildFromStdin(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "out.ipmap")
	code, _, errs := exec(t, spec, "build", "-in", "-", "-out", db)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	fromStdin, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := os.ReadFile(buildArtifact(t))
	if err != nil {
		t.Fatal(err)
	}
	// The build epoch is stamped at build time; the two runs may straddle a
	// second, so compare everything but the epoch field (bytes 24..32).
	fromStdin, fromFile = append([]byte(nil), fromStdin...), append([]byte(nil), fromFile...)
	copy(fromStdin[24:32], make([]byte, 8))
	copy(fromFile[24:32], make([]byte, 8))
	copy(fromStdin[272:276], make([]byte, 4)) // header CRC covers the epoch
	copy(fromFile[272:276], make([]byte, 4))
	if !bytes.Equal(fromStdin, fromFile) {
		t.Fatal("a stdin build differs from the same spec built from a file")
	}

	code, _, errs = exec(t, "not-an-address aa\n", "build", "-in", "-", "-out", db)
	if code != 1 || !strings.Contains(errs, "stdin:1:") {
		t.Fatalf("exit %d, stderr %q; want a stdin:1: error", code, errs)
	}
}

func TestBuildErrorsCarryLineNumbers(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body, want string
	}{
		{"bad address", "1.2.3.4 aa\nnot-an-address bb\n", ":2:"},
		{"bad hex", "1.2.3.4 aa\n5.6.7.8 zz\n", ":2: value:"},
		{"wrong width", "1.2.3.4 aabb\n5.6.7.8 aa\n", ":2:"},
		{"three fields", "1.2.3.4 aa bb\n", ":1:"},
		{"empty spec", "# nothing\n", "no entries"},
		{"value too wide", "1.2.3.4 " + strings.Repeat("ab", ipmap.MaxValLen+1) + "\n", ":1: value is 4097 bytes, limit is 4096"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := filepath.Join(dir, "s.spec")
			if err := os.WriteFile(sp, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			code, _, errs := exec(t, "", "build", "-in", sp, "-out", filepath.Join(dir, "o"))
			if code != 1 || !strings.Contains(errs, tc.want) {
				t.Fatalf("exit %d, stderr %q; want exit 1 containing %q", code, errs, tc.want)
			}
		})
	}
}

// The widest legal value still builds: the limit is inclusive, and the check
// that rejects 4097 bytes must not have moved the boundary.
func TestBuildMaxWidthValue(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "s.spec")
	body := "1.2.3.4 " + strings.Repeat("ab", ipmap.MaxValLen) + "\n"
	if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "o")
	if code, _, errs := exec(t, "", "build", "-in", sp, "-out", db); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if code, _, errs := exec(t, "", "verify", "-db", db); code != 0 {
		t.Fatalf("verify exit %d: %s", code, errs)
	}
}

func TestLookupGolden(t *testing.T) {
	db := buildArtifact(t)
	code, out, _ := exec(t, "", "lookup", "-db", db, "1.2.3.4", "2001:db8::1", "9.9.9.9", "8.8.8.8", "::ffff:1.2.3.4")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := "" +
		"1.2.3.4\t0a1b2c\n" +
		"2001:db8::1\t0a1b2c\n" +
		"9.9.9.9\teeeeee\n" + // the later spec line won: last-wins through the CLI
		"8.8.8.8\tmiss\n" +
		"::ffff:1.2.3.4\t0a1b2c\n" // the mapped spelling answers identically
	if out != want {
		t.Fatalf("golden mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestLookupStdinAndJSON(t *testing.T) {
	db := buildArtifact(t)
	code, out, _ := exec(t, "1.2.3.4\n\n8.8.8.8\n", "lookup", "-db", db, "-json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := "" +
		`{"addr":"1.2.3.4","found":true,"value":"0a1b2c"}` + "\n" +
		`{"addr":"8.8.8.8","found":false}` + "\n"
	if out != want {
		t.Fatalf("golden mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestLookupRejectsBadAddress(t *testing.T) {
	db := buildArtifact(t)
	code, _, errs := exec(t, "", "lookup", "-db", db, "not-an-ip")
	if code != 1 || !strings.Contains(errs, "not-an-ip") {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
}

func TestVerifyGolden(t *testing.T) {
	db := buildArtifact(t)
	code, out, _ := exec(t, "", "verify", "-db", db)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := "ok: 2 v4 + 1 v6 addresses, value width 3, 1 duplicates dropped (1 conflicting)\n"; out != want {
		t.Fatalf("golden mismatch:\n got %q\nwant %q", out, want)
	}
}

// The adversarial question of the phase: verify must never bless a damaged
// artifact. Flip one byte anywhere past the header and the exit code flips.
func TestVerifyRejectsCorruption(t *testing.T) {
	db := buildArtifact(t)
	raw, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{0, 100, len(raw) / 2, len(raw) - 1} {
		bad := append([]byte(nil), raw...)
		bad[at] ^= 0x40
		p := filepath.Join(t.TempDir(), "bad.ipmap")
		if err := os.WriteFile(p, bad, 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := exec(t, "", "verify", "-db", p)
		if code != 1 || out != "" {
			t.Fatalf("byte %d: exit %d, stdout %q, stderr %q — a damaged artifact was blessed", at, code, out, errs)
		}
	}
	// And a truncated one.
	p := filepath.Join(t.TempDir(), "trunc.ipmap")
	if err := os.WriteFile(p, raw[:len(raw)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := exec(t, "", "verify", "-db", p); code != 1 {
		t.Fatal("a truncated artifact was blessed")
	}
}

func TestStatsGolden(t *testing.T) {
	db := buildArtifact(t, "-intern")
	code, out, _ := exec(t, "", "stats", "-db", db, "-json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		Addrs4       int   `json:"addrs4"`
		Addrs6       int   `json:"addrs6"`
		ValLen       int   `json:"val_len"`
		Interned     bool  `json:"interned"`
		Distinct     int   `json:"distinct"`
		Dups         int   `json:"dups"`
		DupConflicts int   `json:"dup_conflicts"`
		Epoch        int64 `json:"epoch"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Addrs4 != 2 || got.Addrs6 != 1 || got.ValLen != 3 || !got.Interned ||
		got.Distinct != 3 || got.Dups != 1 || got.DupConflicts != 1 || got.Epoch == 0 {
		t.Fatalf("stats fields: %+v", got)
	}

	code, human, _ := exec(t, "", "stats", "-db", db)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, line := range []string{
		"addresses     2 v4, 1 v6\n",
		"value width   3 bytes\n",
		"interned      3 distinct values\n",
		"duplicates    1 dropped, 1 carried a conflicting value\n",
	} {
		if !strings.Contains(human, line) {
			t.Fatalf("human stats missing %q in:\n%s", line, human)
		}
	}
}

// The spec reader is a text parser fed by operators — the roadmap's rule (R5)
// is that every parser gets fuzzed. The promise here is modest and absolute:
// parseSpec never panics, and when it reports success it hands back a builder
// with at least one entry in it.
func FuzzSpecParse(f *testing.F) {
	f.Add([]byte(spec))
	f.Add([]byte("1.2.3.4 aa\n"))
	f.Add([]byte("# only a comment\n\n"))
	f.Add([]byte("::1 ffff\n1.2.3.4 ffff\n"))
	f.Add([]byte("1.2.3.4 aa bb\n"))
	f.Add([]byte("not-an-address zz\n"))
	f.Add([]byte("1.2.3.4 \n"))
	f.Add([]byte("1.2.3.4\x00aa\n\xff\xfe"))
	f.Add([]byte("1.2.3.4 " + strings.Repeat("ab", 4097) + "\n")) // wider than NewBuilder allows
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		b, err := parseSpec(bytes.NewReader(data), "fuzz", false)
		if (err == nil) != (b != nil) {
			t.Fatalf("success and builder disagree: err=%v b=%v", err, b != nil)
		}
	})
}

// The build script stamps -X main.version, and Go's linker discards an -X for
// a symbol that does not exist *silently* — which is how every build before
// this test went out unversioned. The variable must therefore exist and be
// reachable from the command line; the release audit found the gap, this keeps
// it closed.
func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "-version", "--version"} {
		code, out, errs := exec(t, "", arg)
		if code != 0 || strings.TrimSpace(out) != version {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q; want exit 0 and %q", arg, code, out, errs, version)
		}
	}
	if version == "" {
		t.Fatal("version is empty: an unstamped build must still say something")
	}
}

// failWriter stands in for a stdout that cannot be written: a closed pipe, a
// full disk, a revoked descriptor.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// A command whose output never arrived must not report success: a script
// checking only the exit code would otherwise proceed on an empty result.
// stats -json is in the list so the one path that already behaved keeps doing so.
func TestStdoutWriteErrorsExitNonZero(t *testing.T) {
	db := buildArtifact(t)
	for _, args := range [][]string{
		{"lookup", "-db", db, "1.2.3.4"},
		{"lookup", "-db", db, "-json", "1.2.3.4"},
		{"stats", "-db", db},
		{"stats", "-db", db, "-json"},
		{"verify", "-db", db},
		{"version"},
	} {
		var errb bytes.Buffer
		code := run(args, strings.NewReader(""), failWriter{}, &errb)
		if code != 1 || !strings.Contains(errb.String(), "write failed") {
			t.Errorf("%v: exit %d, stderr %q; want exit 1 naming the write error", args, code, errb.String())
		}
	}
}

// The flag package writes its own diagnostics and usage to the FlagSet's
// output, which defaults to os.Stderr — not the stderr run was handed. The
// "ipmap:" line run prints for the returned error also carries the message, so
// the usage banner is the part that proves where the FlagSet wrote.
func TestFlagErrorsUseInjectedStderr(t *testing.T) {
	for _, cmd := range []string{"build", "lookup", "verify", "stats"} {
		code, out, errs := exec(t, "", cmd, "-bogus")
		if code != 1 || out != "" {
			t.Errorf("%s -bogus: exit %d, stdout %q; want exit 1 and no stdout", cmd, code, out)
		}
		for _, want := range []string{"flag provided but not defined", "Usage of " + cmd + ":"} {
			if !strings.Contains(errs, want) {
				t.Errorf("%s -bogus: injected stderr %q lacks %q", cmd, errs, want)
			}
		}
	}
}

// Asking for help is not a failure of the invocation, and the code's
// convention is that help exits 2 — as top-level `ipmap -h` does. The flag
// package has already printed the subcommand's usage; run must not follow it
// with an "ipmap: flag: help requested" line.
func TestSubcommandHelpExitsTwo(t *testing.T) {
	for _, cmd := range []string{"build", "lookup", "verify", "stats"} {
		code, out, errs := exec(t, "", cmd, "-h")
		if code != 2 || out != "" {
			t.Errorf("%s -h: exit %d, stdout %q; want exit 2 and no stdout", cmd, code, out)
		}
		if !strings.Contains(errs, "Usage of "+cmd+":") {
			t.Errorf("%s -h: stderr %q lacks the usage banner", cmd, errs)
		}
		if strings.Contains(errs, "help requested") || strings.Contains(errs, "ipmap:") {
			t.Errorf("%s -h: stderr %q carries an error line for a help request", cmd, errs)
		}
	}
}

// A binary from `go install ...@v0.2.0` is not stamped with -X, yet the module
// version is embedded in its build info; reporting "dev" for it would hide
// exactly the release a bug report needs. The -X stamp, when present, wins.
func TestVersionFromBuildInfo(t *testing.T) {
	oldRead, oldVersion := readBuildInfo, version
	defer func() { readBuildInfo, version = oldRead, oldVersion }()

	for _, tc := range []struct {
		name, stamped string
		info          *debug.BuildInfo // nil: no build info at all
		want          string
	}{
		{"go install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, "v0.2.0"},
		{"stamp wins", "v9.9.9-stamped", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, "v9.9.9-stamped"},
		{"devel", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"empty", "dev", &debug.BuildInfo{}, "dev"},
		{"unavailable", "dev", nil, "dev"},
	} {
		version = tc.stamped
		readBuildInfo = func() (*debug.BuildInfo, bool) { return tc.info, tc.info != nil }
		code, out, errs := exec(t, "", "version")
		if code != 0 || strings.TrimSpace(out) != tc.want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %q", tc.name, code, out, errs, tc.want)
		}
	}
}

func TestUsageAndUnknown(t *testing.T) {
	if code, _, errs := exec(t, ""); code != 2 || !strings.Contains(errs, "usage:") {
		t.Fatalf("no-args: exit %d, %q", code, errs)
	}
	if code, _, errs := exec(t, "", "frobnicate"); code != 2 || !strings.Contains(errs, "unknown command") {
		t.Fatalf("unknown: exit %d, %q", code, errs)
	}
	for _, cmd := range []string{"build", "lookup", "verify", "stats"} {
		if code, _, _ := exec(t, "", cmd); code != 1 {
			t.Fatalf("%s with no flags should exit 1", cmd)
		}
	}
}
