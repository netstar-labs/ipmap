# Examples

Runnable demonstrations, each a `main.go` under its own directory; run one with
`go run ./example/<topic>`.

| Example | What it shows | Run |
|---|---|---|
| `embed` | the full life of an artifact from another program: build both families, write, `Open`, query — hit, miss, mapped spelling, `LookupInto` | `go run ./example/embed` |

Still to come, landing before the public release (P9 in [docs/roadmap.md](../docs/roadmap.md)):

| Topic | Shows |
|---|---|
| `build` | constructing an artifact from a spec, both address families |
| `lookup` | opening an artifact and querying it, including the miss path |
| `intern` | the same data with and without value interning, and what it costs |
