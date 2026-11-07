//go:build !race

package tui

// fuzzStormSizes is how many window sizes a resize storm sends each page, and fuzzBigStormSizes
// each page of the big fixture.
const (
	fuzzStormSizes    = 200
	fuzzBigStormSizes = 50
)
