//go:build race

package tools

// raceEnabled says whether the race detector is on: it empties
// sync.Pool on purpose, so a test that counts allocations says so
// instead of failing.
const raceEnabled = true
