//go:build race

package cmd

// raceEnabled says whether the race detector is on: a test that only it
// can fail says so instead of passing.
const raceEnabled = true
