// Command ipmap builds and queries an ipmap artifact.
//
//	ipmap build  -in <spec> -out <artifact> [-intern]   compile a text spec
//	ipmap lookup -db <artifact> [-json] <addr> ...      query; stdin when no args
//	ipmap verify -db <artifact>                         exit non-zero unless valid
//	ipmap stats  -db <artifact> [-json]                 what the artifact holds
//	ipmap version                                       what build this is
//
// The spec is one entry per line — an address and a hex value — with '#'
// comments and blank lines ignored. The value width is taken from the first
// entry and every later entry must match it. Both families may share one spec.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/netstar-labs/ipmap"
)

// version is stamped at link time by build/ipmap (-X main.version). It stays
// "dev" for a plain `go build`, which is the honest answer for one.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main with its edges injected, so the golden tests execute the real
// command paths rather than a parallel implementation.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "build":
		err = build(args[1:], stdin, stderr)
	case "lookup":
		err = lookup(args[1:], stdin, stdout, stderr)
	case "verify":
		err = verify(args[1:], stdout, stderr)
	case "stats":
		err = stats(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		_, err = fmt.Fprintln(stdout, version)
	case "help", "-h", "--help":
		usage(stderr)
		return 2
	default:
		fmt.Fprintf(stderr, "ipmap: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	// A subcommand's -h is help, not failure: exit 2 like top-level help does.
	// The flag package has already printed the usage to stderr, so a further
	// "ipmap: flag: help requested" line would only be noise.
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "ipmap:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: ipmap <command> [flags]

  build  -in <spec> -out <artifact> [-intern]   compile a text spec
  lookup -db <artifact> [-json] <addr> [...]    query; reads stdin when no args
  verify -db <artifact>                         exit non-zero unless valid
  stats  -db <artifact> [-json]                 what the artifact holds
  version                                       what build this is

The spec is one "<address> <value-hex>" entry per line; '#' comments and blank
lines are ignored, both address families may share one file, and the value
width is taken from the first entry.
`)
}

func build(args []string, stdin io.Reader, stderr io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "spec file (- for stdin)")
	out := fs.String("out", "", "artifact to write")
	intern := fs.Bool("intern", false, "deduplicate repeated values into a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("build: -in and -out are required")
	}

	src, name := stdin, "stdin" // the name errors cite: line numbers need a file
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			return err
		}
		defer f.Close()
		src, name = f, *in
	}

	b, err := parseSpec(src, name, *intern)
	if err != nil {
		return err
	}
	m, err := b.Build()
	if err != nil {
		return err
	}

	// Write to a sibling temp path and rename, so a process crash mid-write
	// cannot leave a half-artifact under the final name — Open would refuse
	// it, but a name that exists implies a file that serves. The temp name is
	// unique to this build: concurrent builds to one -out must not share (and
	// interleave writes into) one file. The deferred Remove covers every error
	// return, and after a successful rename it finds nothing and is a no-op.
	// There is deliberately no fsync: the promise is crash safety for the
	// process, not durability across power loss.
	tmp, of, err := createTemp(*out)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := m.WriteTo(of); err != nil {
		of.Close()
		return err
	}
	if err := of.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	st := m.Stats()
	fmt.Fprintf(stderr, "ipmap: %s: %d v4 + %d v6 addresses", *out, st.Addrs4, st.Addrs6)
	if st.Dups > 0 {
		fmt.Fprintf(stderr, ", %d duplicates dropped (%d conflicting)", st.Dups, st.DupConflicts)
	}
	if st.Distinct > 0 {
		fmt.Fprintf(stderr, ", %d distinct values interned", st.Distinct)
	}
	fmt.Fprintln(stderr)
	return nil
}

// createTemp creates a new file named <out>.<random>.tmp beside out and
// returns it open for writing. O_EXCL makes the create fail rather than reuse
// or follow anything already at that name — a symlink planted in a shared
// directory included — so each build owns its temp file outright; a collision
// is retried under a fresh suffix. os.CreateTemp is not used because it makes
// the file 0600, which would silently narrow the artifact's mode from the
// umask default and lock out readers running as another user. The .tmp suffix
// is kept because the user guide names it for anything watching the directory.
func createTemp(out string) (string, *os.File, error) {
	var err error
	for range 10 {
		name := out + "." + strconv.FormatUint(rand.Uint64(), 36) + ".tmp"
		var f *os.File
		if f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666); err == nil {
			return name, f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	return "", nil, err
}

// parseSpec reads the text spec from r into a Builder: one "<address>
// <value-hex>" entry per line, '#' comments and blank lines ignored, the value
// width fixed by the first entry. name is what errors cite ("stdin" or the
// path). Split out of build so the reader can be fuzzed on its own — a text
// parser fed by operators is exactly where a panic would otherwise hide.
func parseSpec(r io.Reader, name string, intern bool) (*ipmap.Builder, error) {
	var b *ipmap.Builder
	lineNo := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"<address> <value-hex>\", got %d fields", name, lineNo, len(fields))
		}
		addr, err := netip.ParseAddr(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", name, lineNo, err)
		}
		val, err := hex.DecodeString(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: value: %v", name, lineNo, err)
		}
		if b == nil { // the first entry sets the value width for the artifact
			if len(val) == 0 {
				return nil, fmt.Errorf("%s:%d: empty value", name, lineNo)
			}
			// NewBuilder panics on a width it cannot serve; operator input
			// must get an error with a line number, not a goroutine trace.
			if len(val) > ipmap.MaxValLen {
				return nil, fmt.Errorf("%s:%d: value is %d bytes, limit is %d", name, lineNo, len(val), ipmap.MaxValLen)
			}
			b = ipmap.NewBuilder(ipmap.Options{ValLen: len(val), Intern: intern})
		}
		if err := b.Add(addr, val); err != nil {
			return nil, fmt.Errorf("%s:%d: %v", name, lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("%s: no entries", name)
	}
	return b, nil
}

func lookup(args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
	fs.SetOutput(stderr) // the flag package defaults to os.Stderr, bypassing run's
	db := fs.String("db", "", "artifact to query")
	asJSON := fs.Bool("json", false, "one JSON object per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("lookup: -db is required")
	}
	m, err := ipmap.Open(*db)
	if err != nil {
		return err
	}
	defer m.Close()

	w := bufio.NewWriter(stdout)
	// A short result sits entirely in the buffer, so a dead stdout surfaces
	// only here. It must not be swallowed, but it must not mask the error that
	// ended the loop either.
	defer func() {
		if ferr := w.Flush(); err == nil {
			err = ferr
		}
	}()
	enc := json.NewEncoder(w)

	one := func(raw string) error {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return err
		}
		v, ok := m.Lookup(addr)
		if *asJSON {
			return enc.Encode(struct {
				Addr  string `json:"addr"`
				Found bool   `json:"found"`
				Value string `json:"value,omitempty"`
			}{raw, ok, hex.EncodeToString(v)})
		}
		if ok {
			_, err = fmt.Fprintf(w, "%s\t%s\n", raw, hex.EncodeToString(v))
		} else {
			_, err = fmt.Fprintf(w, "%s\tmiss\n", raw)
		}
		return err
	}

	if fs.NArg() > 0 {
		for _, a := range fs.Args() {
			if err := one(a); err != nil {
				return err
			}
		}
		return nil
	}
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := one(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// verify is Open: the reader already validates every checksum and structural
// invariant before returning, so an artifact that opens is an artifact that
// passed. The command exists to give scripts an exit code and a one-line
// receipt.
func verify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "artifact to verify")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("verify: -db is required")
	}
	m, err := ipmap.Open(*db)
	if err != nil {
		return err
	}
	defer m.Close()
	st := m.Stats()
	// The duplicate counts ride along unconditionally: a script comparing two
	// verify receipts should see the same fields either way, and a duplicate
	// carrying a different value means the input disagreed with itself.
	_, err = fmt.Fprintf(stdout, "ok: %d v4 + %d v6 addresses, value width %d, %d duplicates dropped (%d conflicting)\n",
		st.Addrs4, st.Addrs6, m.ValLen(), st.Dups, st.DupConflicts)
	return err
}

func stats(args []string, stdout, stderr io.Writer) (err error) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "artifact to describe")
	asJSON := fs.Bool("json", false, "a single JSON object")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("stats: -db is required")
	}
	m, err := ipmap.Open(*db)
	if err != nil {
		return err
	}
	defer m.Close()
	st := m.Stats()

	if *asJSON {
		return json.NewEncoder(stdout).Encode(struct {
			Addrs4       int   `json:"addrs4"`
			Addrs6       int   `json:"addrs6"`
			ValLen       int   `json:"val_len"`
			Interned     bool  `json:"interned"`
			Distinct     int   `json:"distinct,omitempty"`
			Dups         int   `json:"dups"`
			DupConflicts int   `json:"dup_conflicts"`
			Epoch        int64 `json:"epoch"`
		}{st.Addrs4, st.Addrs6, m.ValLen(), st.Distinct > 0, st.Distinct, st.Dups, st.DupConflicts, m.Epoch()})
	}
	w := bufio.NewWriter(stdout)
	// Buffered, so the Fprintf errors below are sticky in w and Flush reports
	// the first of them; returning Flush's error is therefore enough.
	defer func() {
		if ferr := w.Flush(); err == nil {
			err = ferr
		}
	}()
	fmt.Fprintf(w, "addresses     %d v4, %d v6\n", st.Addrs4, st.Addrs6)
	fmt.Fprintf(w, "value width   %d bytes\n", m.ValLen())
	if st.Distinct > 0 {
		fmt.Fprintf(w, "interned      %d distinct values\n", st.Distinct)
	} else {
		fmt.Fprintf(w, "interned      no\n")
	}
	fmt.Fprintf(w, "duplicates    %d dropped, %d carried a conflicting value\n", st.Dups, st.DupConflicts)
	fmt.Fprintf(w, "built         %s (%d)\n", time.Unix(m.Epoch(), 0).UTC().Format(time.RFC3339), m.Epoch())
	return nil
}
