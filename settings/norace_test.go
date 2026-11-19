//go:build !race

package settings

// raceDetector says that the tests were built with the race detector: see
// reducedRun.
const raceDetector = false
