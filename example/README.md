# Examples

Runnable demonstrations, one per way the library is used. Each is a `main.go`
under its own directory; run one with `go run ./example/<topic>`.

| Example | What it shows | Run |
|---|---|---|
| `embed` | the full life of an artifact from another program: build both families, write, `Open`, query — hit, miss, mapped spelling, `LookupInto` | `go run ./example/embed` |
| `build` | constructing an artifact from a text spec, both families — with the value bytes given internal structure via [`pkg/bits`](../pkg/bits), packed and unpacked with no hand-written mask | `go run ./example/build` |
| `lookup` | querying — hit, miss, owned buffer — and the zero-downtime reload: goroutines reading through one atomic pointer while the artifact is swapped underneath | `go run ./example/lookup` |
| `intern` | the same data with and without value interning: what it costs, what it buys, and that lookups cannot tell the difference | `go run ./example/intern` |
