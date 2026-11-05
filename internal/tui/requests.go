package tui

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kontrolplane/koldstart/internal/cli"
)

// request is one invocation of a function, as its log tells it.
type request struct {
	id    string
	start time.Time
	// last is the time of its last line.
	last time.Time
	// The durations are in milliseconds; init is the cold start, 0 for a warm one.
	duration  float64
	billed    float64
	init      float64
	memory    int
	maxMemory int
	reported  bool
	failed    bool
	timedOut  bool
	// lines are the indexes of the log lines the request wrote, START to REPORT.
	lines []int
}

var (
	startLine  = regexp.MustCompile(`^START RequestId: (\S+)`)
	reportLine = regexp.MustCompile(`^REPORT RequestId: (\S+)`)
	reportItem = regexp.MustCompile(`(Billed Duration|Init Duration|Duration|Max Memory Used|Memory Size): ([\d.]+)`)
	nodeLine   = regexp.MustCompile(`^\S+\t([0-9a-f-]{36})\t`)
)

// failure reports whether a line of a request says it failed: an error a runtime logs, a
// timeout, or a line logged at the error level.
func failure(line string) (failed, timedOut bool) {
	switch {
	case strings.Contains(line, "Task timed out"), strings.Contains(line, "Status: timeout"):
		return true, true
	case strings.Contains(line, "\tERROR\t"), strings.Contains(line, `"errorType"`), strings.Contains(line, "Runtime.ExitError"),
		strings.Contains(line, `"level":"ERROR"`), strings.Contains(line, `"level":"error"`), strings.Contains(line, "level=ERROR"):
		return true, false
	}
	return false, false
}

// requests reads the invocations out of a function's log lines, newest first. A line belongs to
// the request its log stream was running, from START to REPORT, or the one a node line names.
func requests(logs []cli.LogEvent) []request {
	byID := map[string]*request{}
	var order []string
	current := map[string]string{}
	get := func(id string, at time.Time) *request {
		r, ok := byID[id]
		if !ok {
			r = &request{id: id, start: at}
			byID[id] = r
			order = append(order, id)
		}
		return r
	}
	for i, e := range logs {
		var id string
		switch {
		case startLine.MatchString(e.Message):
			id = startLine.FindStringSubmatch(e.Message)[1]
			current[e.Stream] = id
		case reportLine.MatchString(e.Message):
			id = reportLine.FindStringSubmatch(e.Message)[1]
			r := get(id, e.Time)
			r.reported = true
			for _, m := range reportItem.FindAllStringSubmatch(e.Message, -1) {
				v, _ := strconv.ParseFloat(m[2], 64)
				switch m[1] {
				case "Duration":
					r.duration = v
				case "Billed Duration":
					r.billed = v
				case "Init Duration":
					r.init = v
				case "Memory Size":
					r.memory = int(v)
				case "Max Memory Used":
					r.maxMemory = int(v)
				}
			}
			delete(current, e.Stream)
		case nodeLine.MatchString(e.Message):
			id = nodeLine.FindStringSubmatch(e.Message)[1]
		default:
			id = current[e.Stream]
		}
		if id == "" {
			continue
		}
		r := get(id, e.Time)
		r.lines = append(r.lines, i)
		r.last = e.Time
		if failed, timedOut := failure(e.Message); failed {
			r.failed = true
			r.timedOut = r.timedOut || timedOut
		}
	}
	out := make([]request, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	slices.SortStableFunc(out, func(a, b request) int { return b.start.Compare(a.start) })
	return out
}
