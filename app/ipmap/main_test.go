package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err := os.Stat(db + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temp file survived a successful build")
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
	if want := "ok: 2 v4 + 1 v6 addresses, value width 3\n"; out != want {
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
