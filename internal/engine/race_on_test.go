//go:build race

package engine

// raceEnabledMain reports whether the binary was built with -race.
//
// The detector instruments every memory access, so a service time measured
// under it is an order of magnitude out and any assertion about a trap rate
// becomes noise. CI runs -race.
const raceEnabledMain = true
