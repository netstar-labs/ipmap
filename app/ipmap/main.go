// Command ipmap builds and queries an ipmap artifact.
//
//	ipmap build  -in <spec> -out <artifact>   compile a text spec into an artifact
//	ipmap lookup -db <artifact> <addr> ...    query it
//	ipmap verify -db <artifact>               check an artifact's invariants
//	ipmap stats  -db <artifact>               entry counts, value width, families
//
// Nothing is implemented yet; see docs/roadmap.md for the phase each subcommand
// belongs to.
package main

import (
	"fmt"
	"os"

	"github.com/netstar-labs/ipmap"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "build", "lookup", "verify", "stats":
		fmt.Fprintf(os.Stderr, "ipmap: %s is not implemented yet; see docs/roadmap.md\n", os.Args[1])
		os.Exit(1)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ipmap: unknown command %q\n", os.Args[1])
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ipmap <command> [flags]

  build  -in <spec> -out <artifact>    compile a text spec into an artifact
  lookup -db <artifact> <addr> [...]   query it
  verify -db <artifact>                check an artifact's invariants
  stats  -db <artifact>                entry counts, value width, families

See docs/roadmap.md for what is built and what is not.
`)
	os.Exit(2)
}

// _ keeps the library wired into the binary while the subcommands are stubs, so
// a build failure in the library is a build failure here.
var _ = ipmap.Options{}
