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
	// lines are the lines of the log the request wrote, START to REPORT.
	lines []cli.LogEvent
}

var (
	startLine  = regexp.MustCompile(`^START RequestId: (\S+)`)
	reportLine = regexp.MustCompile(`^REPORT RequestId: (\S+)`)
	reportItem = regexp.MustCompile(`(Billed Duration|Init Duration|Duration|Max Memory Used|Memory Size): ([\d.]+)`)
	nodeLine   = regexp.MustCompile(`^\S+\t([0-9a-f-]{36})\t`)
	// errorLevel is a line of json or logfmt logged at the error level or worse.
	errorLevel = regexp.MustCompile(`(?i)"(level|lvl|severity)"\s*:\s*"(error|err|fatal|critical|panic)"|(^|\s)(level|lvl|severity)="?(error|err|fatal|critical|panic)\b`)
)

// failure reports whether a line of a request says it failed: an error a runtime logs, a
// timeout, or a line logged at the error level.
func failure(line string) (failed, timedOut bool) {
	switch {
	case strings.Contains(line, "Task timed out"), strings.Contains(line, "Status: timeout"):
		return true, true
	case strings.Contains(line, "\tERROR\t"), strings.Contains(line, `"errorType"`), strings.Contains(line, "Runtime.ExitError"),
		strings.Contains(line, "Status: error"), mayLogError(line) && errorLevel.MatchString(line):
		return true, false
	}
	return false, false
}

// mayLogError reports whether a line holds a word errorLevel looks for, ignoring case, so the
// expression runs only on the lines it can match.
func mayLogError(line string) bool {
	for i := range len(line) {
		switch line[i] | 0x20 {
		case 'e':
			if hasFoldAt(line, i, "err") {
				return true
			}
		case 'f':
			if hasFoldAt(line, i, "fatal") {
				return true
			}
		case 'c':
			if hasFoldAt(line, i, "critical") {
				return true
			}
		case 'p':
			if hasFoldAt(line, i, "panic") {
				return true
			}
		}
	}
	return false
}

// hasFoldAt reports whether s holds word at i, ignoring case.
func hasFoldAt(s string, i int, word string) bool {
	return i+len(word) <= len(s) && strings.EqualFold(s[i:i+len(word)], word)
}

// lineIDs are the request a line starts, the one it reports, and the one a node line names, each
// "" when it is not that line. An expression runs only on a line that starts as it would match.
func lineIDs(line string) (start, report, node string) {
	switch {
	case strings.HasPrefix(line, "START RequestId:"):
		if m := startLine.FindStringSubmatch(line); m != nil {
			return m[1], "", ""
		}
	case strings.HasPrefix(line, "REPORT RequestId:"):
		if m := reportLine.FindStringSubmatch(line); m != nil {
			return "", m[1], ""
		}
	case strings.IndexByte(line, '\t') > 0:
		if m := nodeLine.FindStringSubmatch(line); m != nil {
			return "", "", m[1]
		}
	}
	return "", "", ""
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
	for _, e := range logs {
		var id string
		start, report, node := lineIDs(e.Message)
		switch {
		case start != "":
			id = start
			current[e.Stream] = id
		case report != "":
			id = report
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
		case node != "":
			id = node
		default:
			id = current[e.Stream]
		}
		if id == "" {
			continue
		}
		r := get(id, e.Time)
		r.lines = append(r.lines, e)
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
