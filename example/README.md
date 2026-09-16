# Examples

One runnable program per way `ipmap` is used. Each is a `main.go` under its own
directory; run it with `go run ./example/<topic>`.

| Example | What it shows | Run |
|---|---|---|
| — | *No examples yet.* The first lands with the in-memory store; see [docs/roadmap.md](../docs/roadmap.md), P3. | |

The set to expect, in the order the roadmap builds them:

| Topic | Shows |
|---|---|
| `build` | constructing an artifact from a spec, both address families |
| `lookup` | opening an artifact and querying it, including the miss path |
| `intern` | the same data with and without value interning, and what it costs |
| `embed` | using the library from another program rather than the CLI |
