//go:build race

package studio

// raceSlowdown scales the time limits of tests: the race detector makes this
// CPU-bound code several times slower, and CI runs with it.
const raceSlowdown = 10
