//go:build race

package tui

// fuzzStormSizes is how many window sizes a resize storm sends each page, and fuzzBigStormSizes
// each page of the big fixture. The race detector makes a frame some twenty times slower, so under
// it a storm is a tenth of its size, and go test without -race sends the whole.
const (
	fuzzStormSizes    = 20
	fuzzBigStormSizes = 5
)
