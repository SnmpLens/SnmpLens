//go:build !race

package engine

// raceEnabledMain reports whether the binary was built with -race. See race_on_test.go.
const raceEnabledMain = false
