package tui

import (
	"encoding/json"
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
	// runtime is the version of the runtime a cold start loaded, as INIT_START names it, and
	// initStatus how its init ended when INIT_REPORT says it did not succeed: error or timeout.
	runtime    string
	initStatus string
	// cause is what it failed with, as the most telling of its lines says.
	cause cause
	// lines are the lines of the log the request wrote, START to REPORT, after those of the init
	// of a cold start.
	lines []cli.LogEvent
}

// cause is what an invocation failed with: the type of its error, as TypeError, and its message.
// rank orders the lines that say it: a timeout, then an error the runtime reports, then one
// logged at the error level.
type cause struct {
	rank    int
	kind    string
	message string
}

var (
	startLine  = regexp.MustCompile(`^START RequestId: (\S+)`)
	reportLine = regexp.MustCompile(`^REPORT RequestId: (\S+)`)
	reportItem = regexp.MustCompile(`(Billed Duration|Init Duration|Duration|Max Memory Used|Memory Size): ([\d.]+)`)
	nodeLine   = regexp.MustCompile(`^\S+\t([0-9a-f-]{36})\t`)
	// errorLevel is a line of json or logfmt logged at the error level or worse.
	errorLevel = regexp.MustCompile(`(?i)"(level|lvl|severity)"\s*:\s*"(error|err|fatal|critical|panic)"|(^|\s)(level|lvl|severity)="?(error|err|fatal|critical|panic)\b`)
	// errorName is a message that starts with the type of an error: TypeError: boom.
	errorName = regexp.MustCompile(`^((?:[A-Za-z_][\w.$]*)?(?:Error|Exception|Fault)\w*): (.*)`)
)

// failure reports whether a line of a request says it failed: an error a runtime logs, a
// timeout, a panic, or a line logged at the error level, pino's 50 included.
func failure(line string) (failed, timedOut bool) {
	switch {
	case strings.Contains(line, "Task timed out"), strings.Contains(line, "Status: timeout"):
		return true, true
	case strings.Contains(line, "\tERROR\t"), strings.Contains(line, `"errorType"`), strings.Contains(line, "Runtime.ExitError"),
		strings.Contains(line, "Status: error"), strings.HasPrefix(line, "[ERROR]"), strings.HasPrefix(line, "[CRITICAL]"),
		strings.HasPrefix(line, "panic: "), pinoLevel(line) >= 50, mayLogError(line) && errorLevel.MatchString(line):
		return true, false
	}
	return false, false
}

// pinoLevel is the level of a line of json that writes it as a number, as pino does, or -1.
func pinoLevel(line string) int {
	_, rest, ok := strings.Cut(line, `"level"`)
	if !ok {
		return -1
	}
	rest = strings.TrimLeft(rest, " ")
	rest, ok = strings.CutPrefix(rest, ":")
	if !ok {
		return -1
	}
	rest = strings.TrimLeft(rest, " ")
	n := 0
	for n < len(rest) && n < 3 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	level, err := strconv.Atoi(rest[:n])
	if err != nil {
		return -1
	}
	return level
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

// causeOf is what a line that says a request failed says it failed with. A line of json or one
// a runtime writes is read for the type and message of the error, any other for its words.
func causeOf(line, id string) cause {
	first, _, _ := strings.Cut(strings.ReplaceAll(line, "\r", "\n"), "\n")
	switch {
	case strings.Contains(first, "Task timed out after"):
		_, after, _ := strings.Cut(first, "Task timed out after")
		return cause{rank: 3, message: "Task timed out after" + after}
	case strings.HasPrefix(line, "REPORT RequestId:"):
		if kind := fieldAfter(line, "Error Type: "); kind != "" {
			return cause{rank: 1, kind: kind}
		}
		return cause{}
	case strings.HasPrefix(line, "panic: "):
		return cause{rank: 2, kind: levelPanic, message: strings.TrimPrefix(first, "panic: ")}
	case strings.HasPrefix(line, "[ERROR] "), strings.HasPrefix(line, "[CRITICAL] "):
		_, after, _ := strings.Cut(first, " ")
		return typedCause(2, after)
	}
	if before, _, ok := strings.Cut(line, `"errorType"`); ok {
		if j := strings.LastIndexByte(before, '{'); j >= 0 {
			var e struct{ ErrorType, ErrorMessage string }
			if json.NewDecoder(strings.NewReader(line[j:])).Decode(&e) == nil && (e.ErrorType != "" || e.ErrorMessage != "") {
				return cause{rank: 2, kind: e.ErrorType, message: e.ErrorMessage}
			}
		}
	}
	if _, after, ok := strings.Cut(first, "Error: Runtime exited"); ok {
		kind := ""
		if strings.Contains(line, "Runtime.ExitError") {
			kind = "Runtime.ExitError"
		}
		return cause{rank: 2, kind: kind, message: "Runtime exited" + after}
	}
	var l shownLine
	message := l.dropPrefix(first, id)
	if fields, ok := jsonFields(strings.TrimSpace(message)); ok {
		return jsonCause(fields)
	}
	return typedCause(1, message)
}

// typedCause is the cause a message says, its type split off when it starts with one.
func typedCause(rank int, message string) cause {
	if m := errorName.FindStringSubmatch(message); m != nil {
		return cause{rank: rank, kind: m[1], message: m[2]}
	}
	return cause{rank: rank, message: message}
}

// jsonCause is the cause a line of json logged at the error level says: its message, with the
// error it holds, and the error's type.
func jsonCause(fields []jsonField) cause {
	c := cause{rank: 1}
	if f, ok := fieldOf(fields, "msg", "message"); ok {
		c.message = f.text()
	}
	if f, ok := fieldOf(fields, "errorType", "exception_name", "error_type"); ok {
		c.kind = f.text()
	}
	f, ok := fieldOf(fields, "err", "error", "errorMessage")
	if !ok {
		return c
	}
	said := f.text()
	if nested, ok := jsonFields(string(f.value)); ok {
		said = ""
		if t, ok := fieldOf(nested, "type", "name"); ok && c.kind == "" {
			c.kind = t.text()
		}
		if m, ok := fieldOf(nested, "message"); ok {
			said = m.text()
		}
	}
	if said != c.message {
		c.message = joinNonEmpty(c.message, said)
	}
	return c
}

// fieldAfter is the word after label in a line lambda writes, or "".
func fieldAfter(line, label string) string {
	_, rest, ok := strings.Cut(line, label)
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = rest[:i]
	}
	return rest
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

// initLine reports whether lambda wrote the line as an instance starts, before its first request.
func initLine(line string) bool {
	return strings.HasPrefix(line, "INIT_START ") || strings.HasPrefix(line, "INIT_REPORT ") ||
		strings.HasPrefix(line, "RESTORE_START ") || strings.HasPrefix(line, "RESTORE_REPORT ")
}

// read takes a line of the request: its report, what its init says, and whether it says it
// failed and with what.
func (r *request) read(e cli.LogEvent) {
	r.lines = append(r.lines, e)
	if e.Time.After(r.last) {
		r.last = e.Time
	}
	line := e.Message
	switch {
	case strings.HasPrefix(line, "REPORT RequestId:"), strings.HasPrefix(line, "INIT_REPORT "):
		r.reported = r.reported || strings.HasPrefix(line, "REPORT")
		for _, m := range reportItem.FindAllStringSubmatch(line, -1) {
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
		if status := fieldAfter(line, "Status: "); strings.HasPrefix(line, "INIT_REPORT") && status != "" && status != "success" {
			r.initStatus = status
		}
	case strings.HasPrefix(line, "INIT_START "):
		r.runtime = fieldAfter(line, "Runtime Version: ")
	}
	if strings.HasPrefix(line, "INIT_REPORT ") && strings.Contains(line, "Phase: init") {
		return
	}
	failed, timedOut := failure(line)
	if !failed {
		return
	}
	r.failed = true
	r.timedOut = r.timedOut || timedOut
	if r.cause.rank >= 2 && !timedOut {
		return
	}
	if c := causeOf(line, r.id); c.rank > r.cause.rank {
		r.cause = c
	}
}

// requests reads the invocations out of a function's log lines, newest first. A line belongs to
// the request its log stream was running, from START to REPORT, or the one a node line names. The
// lines of the init of an instance, from INIT_START, go with the first request it runs.
func requests(logs []cli.LogEvent) []request {
	byID := map[string]*request{}
	var order []string
	current := map[string]string{}
	starting := map[string][]cli.LogEvent{}
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
			r := get(id, e.Time)
			for _, s := range starting[e.Stream] {
				r.read(s)
			}
			delete(starting, e.Stream)
		case report != "":
			id = report
			delete(current, e.Stream)
		case node != "":
			id = node
		default:
			id = current[e.Stream]
		}
		if id == "" {
			if _, ok := starting[e.Stream]; ok || initLine(e.Message) {
				starting[e.Stream] = append(starting[e.Stream], e)
			}
			continue
		}
		get(id, e.Time).read(e)
	}
	out := make([]request, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	slices.SortStableFunc(out, func(a, b request) int { return b.start.Compare(a.start) })
	return out
}
