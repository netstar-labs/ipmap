//go:build race

package ipmap

// raceEnabled scales the heavyweight adversarial loops down under the race
// detector: race mode multiplies their cost several-fold while the properties
// they prove are data-shape properties, not concurrency ones — the full counts
// run in the normal test pass of the very same CI job.
const raceEnabled = true
