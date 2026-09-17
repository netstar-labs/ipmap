// Command ipmap builds and queries an ipmap artifact.
//
//	ipmap build  -in <spec> -out <artifact> [-intern]   compile a text spec
//	ipmap lookup -db <artifact> [-json] <addr> ...      query; also reads stdin
//	ipmap verify -db <artifact>                         exit non-zero unless valid
//	ipmap stats  -db <artifact> [-json]                 what the artifact holds
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
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/netstar-labs/ipmap"
)

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
		err = build(args[1:], stderr)
	case "lookup":
		err = lookup(args[1:], stdin, stdout)
	case "verify":
		err = verify(args[1:], stdout)
	case "stats":
		err = stats(args[1:], stdout)
	case "help", "-h", "--help":
		usage(stderr)
		return 2
	default:
		fmt.Fprintf(stderr, "ipmap: unknown command %q\n", args[0])
		usage(stderr)
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

The spec is one "<address> <value-hex>" entry per line; '#' comments and blank
lines are ignored, both address families may share one file, and the value
width is taken from the first entry.
`)
}

func build(args []string, stderr io.Writer) error {
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

	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()

	var b *ipmap.Builder
	lineNo := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("%s:%d: want \"<address> <value-hex>\", got %d fields", *in, lineNo, len(fields))
		}
		addr, err := netip.ParseAddr(fields[0])
		if err != nil {
			return fmt.Errorf("%s:%d: %v", *in, lineNo, err)
		}
		val, err := hex.DecodeString(fields[1])
		if err != nil {
			return fmt.Errorf("%s:%d: value: %v", *in, lineNo, err)
		}
		if b == nil { // the first entry sets the value width for the artifact
			if len(val) == 0 {
				return fmt.Errorf("%s:%d: empty value", *in, lineNo)
			}
			b = ipmap.NewBuilder(ipmap.Options{ValLen: len(val), Intern: *intern})
		}
		if err := b.Add(addr, val); err != nil {
			return fmt.Errorf("%s:%d: %v", *in, lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("%s: no entries", *in)
	}
	m, err := b.Build()
	if err != nil {
		return err
	}

	// Write to a sibling temp path and rename, so a crash mid-write cannot
	// leave a half-artifact under the final name — Open would refuse it, but a
	// name that exists implies a file that serves.
	tmp := *out + ".tmp"
	of, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := m.WriteTo(of); err != nil {
		of.Close()
		os.Remove(tmp)
		return err
	}
	if err := of.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		os.Remove(tmp)
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

func lookup(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
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
	defer w.Flush()
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
func verify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
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
	fmt.Fprintf(stdout, "ok: %d v4 + %d v6 addresses, value width %d\n", st.Addrs4, st.Addrs6, m.ValLen())
	return nil
}

func stats(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
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
	defer w.Flush()
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
