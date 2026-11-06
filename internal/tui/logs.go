package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// logsSince is how far back the logs start when a function is opened, and olderStep how much
// further back each read of older lines goes.
const (
	logsSince = 30 * time.Minute
	olderStep = time.Hour
)

// maxLogLines bounds the lines kept. Reading newer lines drops the oldest, and reading older
// ones the newest.
const maxLogLines = 5000

// stampWidth is the width of the time in front of a line of the logs, with the gap after it, and
// agoWidth the width of how long ago it was.
const (
	stampWidth = 14
	agoWidth   = 10
)

// stampMode is what the column in front of the lines of the logs shows: the time of each, how
// long ago it was, or nothing, leaving its room to the lines.
type stampMode int

const (
	stampClock stampMode = iota
	stampAgo
	stampNone
	stampModes
)

// width is the room the column of times takes, with the gap after it.
func (s stampMode) width() int {
	switch s {
	case stampClock:
		return stampWidth
	case stampAgo:
		return agoWidth
	}
	return 0
}

// logRank orders the levels a line is logged at, for the level the logs show from. A line that
// names none ranks as info, or as error when it says something failed, as a timeout or a crash.
type logRank int

const (
	rankAll logRank = iota
	rankDebug
	rankInfo
	rankWarn
	rankError
)

// rankNames are the levels the logs can show from, as the frame names them.
var rankNames = map[logRank]string{rankInfo: levelInfo, rankWarn: levelWarn, rankError: levelError}

// next is the level shown from after r: every line, then info, warn and error and worse.
func (r logRank) next() logRank {
	switch r {
	case rankAll:
		return rankInfo
	case rankInfo:
		return rankWarn
	case rankWarn:
		return rankError
	}
	return rankAll
}

// rankOf is the rank of a level levelName wrote, or of "", which is info.
func rankOf(level string) logRank {
	switch level {
	case levelTrace, levelDebug:
		return rankDebug
	case levelWarn:
		return rankWarn
	case levelError, levelFatal, levelCrit, levelPanic:
		return rankError
	}
	return rankInfo
}

// levelField is the level a line of json or logfmt names, a word or pino's number.
var levelField = regexp.MustCompile(`(?i)"(?:level|lvl|severity)"\s*:\s*"?([a-z]+|\d+)|(?:^|\s)(?:level|lvl|severity)="?([a-z]+|\d+)`)

// levelScan is how far into a line its level is looked for in json or logfmt.
const levelScan = 4 << 10

// lineLevel is the level a raw line of the logs was logged at, as levelName writes it: the one a
// node or python line names in front of its message, or a field of json or logfmt, or error for
// a line that says something failed, as a timeout, a crash or a panic does. It is "" for a line
// that says neither.
func lineLevel(raw string) string {
	first := raw
	if i := strings.IndexAny(raw, "\r\n"); i >= 0 {
		first = raw[:i]
	}
	var l shownLine
	l.dropPrefix(first, "")
	if l.level != "" {
		return l.level
	}
	head := first[:min(len(first), levelScan)]
	if mayNameLevel(head) {
		if m := levelField.FindStringSubmatch(head); m != nil {
			return levelName(m[1] + m[2])
		}
	}
	if failed, _ := failure(raw); failed {
		return levelError
	}
	return ""
}

// mayNameLevel reports whether s holds a word levelField looks for, ignoring case, so the
// expression runs only on the lines it can match.
func mayNameLevel(s string) bool {
	for i := range len(s) {
		switch s[i] | 0x20 {
		case 'l':
			if hasFoldAt(s, i, "level") || hasFoldAt(s, i, "lvl") {
				return true
			}
		case 's':
			if hasFoldAt(s, i, "severity") {
				return true
			}
		}
	}
	return false
}

// logState is the logs of the function on screen: the lines read, whether the newest are
// followed, as a live tail brings them or by reading again, and how far back they go.
type logState struct {
	gen int
	// answered is the generation of the last read of new lines that came back.
	answered int
	loaded   bool
	// err is why the first read of the logs failed.
	err   string
	lines []cli.LogEvent
	// from is the start of the oldest window read; lines before it are not read yet.
	from time.Time
	// follow keeps the newest line in sight; scroll is how many lines the view is above it.
	follow bool
	scroll int
	filter filterBox
	// cursor is the line under the cursor once pinned is set. While following, and before it is
	// pinned, the cursor is on the newest line shown.
	cursor cli.LogEvent
	pinned bool
	// least is the level the lines are shown from, grouped puts the lines of each request
	// together, and stamps is what the column of times shows.
	least   logRank
	grouped bool
	stamps  stampMode

	tailGen  int
	tailing  bool
	noTail   bool
	stopTail context.CancelFunc

	olderGen     int
	loadingOlder bool
	// emptyOlder counts the reads of older lines in a row that found none.
	emptyOlder int
	// cache keeps the lines drawn of the events, tinted and wrapped, and layout the lines the tab
	// shows, in order.
	cache  *rowCache
	layout *logLayout
}

func newLogState() logState {
	return logState{follow: true, filter: newFilterBox("filter the lines…"), cache: newRowCache(), layout: &logLayout{}}
}

func (m model) applyLogs(msg logsLoadedMsg) (model, tea.Cmd) {
	f := &m.state.function
	l := &f.logs
	switch {
	case msg.how == logsPrepend && msg.gen != l.olderGen, msg.how != logsPrepend && msg.gen != l.gen:
		return m, nil
	}
	if msg.how == logsPrepend {
		l.loadingOlder = false
	} else {
		l.answered = msg.gen
	}
	if msg.err != nil {
		if !l.loaded && msg.how != logsPrepend {
			l.err = msg.err.Error()
		}
		return m.loadError("could not read the logs of "+f.name, msg.err, l.loaded)
	}
	events, from := readWindow(msg.events, msg.from)
	switch msg.how {
	case logsAppend:
		l.lines = appendNew(l.lines, events)
		l.trimOldest()
	case logsPrepend:
		l.from = from
		if len(events) == 0 {
			l.emptyOlder++
		} else {
			l.emptyOlder = 0
		}
		l.lines = append(slices.Clone(events), l.lines...)
		dropped := l.trimNewest()
		l.scroll = max(0, l.scroll-len(m.logRows(dropped)))
	default:
		l.lines, l.from = events, from
		l.trimOldest()
	}
	l.loaded, l.err = true, ""
	if m.page == functionPage {
		m = m.markRead()
	}
	return m.updateRequests(), nil
}

// readWindow is what a read of the logs from from brings, and where the window it covers starts.
// A read that hit cli.MaxLogEvents kept its newest lines, so the window starts after the
// millisecond of its first line, whose other lines may be cut, and a read of the lines before
// takes them.
func readWindow(events []cli.LogEvent, from time.Time) ([]cli.LogEvent, time.Time) {
	if len(events) < cli.MaxLogEvents {
		return events, from
	}
	first := events[0].Time
	i := 0
	for i < len(events) && !events[i].Time.After(first) {
		i++
	}
	return events[i:], first.Add(time.Millisecond)
}

// trimOldest keeps the newest maxLogLines lines, and the rest of the millisecond the oldest kept
// is in, so a read of the lines before from takes what was dropped.
func (l *logState) trimOldest() {
	if len(l.lines) <= maxLogLines {
		return
	}
	cut := l.lines[len(l.lines)-maxLogLines].Time
	i := 0
	for i < len(l.lines) && l.lines[i].Time.Before(cut) {
		i++
	}
	l.lines, l.from = slices.Delete(l.lines, 0, i), cut
}

// trimNewest keeps the oldest maxLogLines lines, and the rest of the millisecond the newest kept
// is in, so following again reads what was dropped. It returns the lines it dropped.
func (l *logState) trimNewest() []cli.LogEvent {
	if len(l.lines) <= maxLogLines {
		return nil
	}
	cut := l.lines[maxLogLines-1].Time
	i := maxLogLines
	for i < len(l.lines) && !l.lines[i].Time.After(cut) {
		i++
	}
	dropped := slices.Clone(l.lines[i:])
	l.lines = l.lines[:i]
	return dropped
}

// appendNew adds the lines that are not on screen yet, as a tail and a read can bring the same.
// It compares them with every line on screen as new as the oldest of them.
func appendNew(have, lines []cli.LogEvent) []cli.LogEvent {
	if len(have) == 0 {
		return slices.Clone(lines)
	}
	if len(lines) == 0 {
		return have
	}
	oldest := slices.MinFunc(lines, func(a, b cli.LogEvent) int { return a.Time.Compare(b.Time) }).Time
	seen := map[cli.LogEvent]bool{}
	for i := len(have) - 1; i >= 0 && !have[i].Time.Before(oldest); i-- {
		seen[have[i]] = true
	}
	out := slices.Clip(have)
	for _, e := range lines {
		if !seen[e] {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b cli.LogEvent) int { return a.Time.Compare(b.Time) })
	return out
}

// applyTail adds what the live tail sent. Where no live tail can run, as on an emulator, or it
// stopped on a bug, the logs are read again on every refresh instead.
func (m model) applyTail(msg tailMsg) (model, tea.Cmd) {
	l := &m.state.function.logs
	if msg.gen != l.tailGen {
		return m, nil
	}
	if msg.ended {
		l.tailing, l.stopTail = false, nil
		if _, bug := errors.AsType[*panicError](msg.err); bug {
			l.noTail = true
			return m.setStatus("the live tail of the logs failed: "+msg.err.Error(), styles.ToneDanger)
		}
		if errors.Is(msg.err, cli.ErrNoLiveTail) {
			l.noTail = true
		}
		return m, nil
	}
	if l.follow {
		l.lines = appendNew(l.lines, msg.lines)
		l.trimOldest()
		m = m.updateRequests()
	}
	return m, waitFor(msg.ch)
}

// refreshLogs reads the lines that came since the newest on screen.
func (m model) refreshLogs() (model, tea.Cmd) {
	f := m.state.function
	if f.logGroup == "" || !f.logs.follow {
		return m, nil
	}
	if len(f.logs.lines) == 0 {
		return m.loadLogs(time.Now().Add(-logsSince), time.Time{}, logsReplace)
	}
	return m.loadLogs(f.logs.lines[len(f.logs.lines)-1].Time.Add(time.Millisecond), time.Time{}, logsAppend)
}

// followLogs keeps the newest line in sight again, reading what came while it was not.
func (m model) followLogs() (model, tea.Cmd) {
	l := &m.state.function.logs
	l.follow, l.scroll, l.pinned = true, 0, false
	m, cmd := m.refreshLogs()
	m, tail := m.startTail()
	return m, tea.Batch(cmd, tail)
}

// loadOlder reads the hour of logs before the oldest window read.
func (m model) loadOlder() (model, tea.Cmd) {
	l := m.state.function.logs
	if l.loadingOlder || m.state.function.logGroup == "" || l.from.IsZero() {
		return m, nil
	}
	return m.loadLogs(l.from.Add(-olderStep), l.from.Add(-time.Millisecond), logsPrepend)
}

func (m model) logsUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	l := &m.state.function.logs
	switch {
	case key.Matches(msg, m.keys.Filter):
		return m, l.filter.open()
	case key.Matches(msg, m.keys.Follow):
		if l.follow {
			m = m.pinCursor()
			m.state.function.logs.follow = false
			return m.stopTail(), nil
		}
		return m.followLogs()
	case key.Matches(msg, m.keys.Bottom) && !l.follow:
		return m.followLogs()
	case key.Matches(msg, m.keys.View):
		if e, ok := m.logsCursorLine(); ok {
			return m.openLogLine(e), nil
		}
		return m, nil
	case key.Matches(msg, m.keys.Level, m.keys.Group, m.keys.Stamps):
		if !l.pinned {
			m = m.pinCursor()
			l = &m.state.function.logs
		}
		switch {
		case key.Matches(msg, m.keys.Level):
			l.least = l.least.next()
		case key.Matches(msg, m.keys.Group):
			l.grouped = !l.grouped
		default:
			l.stamps = (l.stamps + 1) % stampModes
		}
		return m.logsRelaid(), nil
	}
	return m.moveLogsCursor(msg)
}

// moveLogsCursor moves the cursor by a line, a page or to the first or last line shown, and
// scrolls as little as keeps it in sight. Moving it up stops following, and up on the first line
// reads the hour before.
func (m model) moveLogsCursor(msg tea.KeyPressMsg) (model, tea.Cmd) {
	list := m.logList()
	cur := m.cursorItem(list)
	up := key.Matches(msg, m.keys.Up, m.keys.PageUp)
	if cur < 0 {
		if up {
			return m.loadOlder()
		}
		return m, nil
	}
	page := functionBodyHeight()
	var next int
	switch {
	case key.Matches(msg, m.keys.Up):
		next = list.step(cur, -1)
	case key.Matches(msg, m.keys.Down):
		next = list.step(cur, 1)
	case key.Matches(msg, m.keys.PageUp):
		next = m.stepRows(list, cur, -page)
	case key.Matches(msg, m.keys.PageDown):
		next = m.stepRows(list, cur, page)
	case key.Matches(msg, m.keys.Top):
		next = list.first()
	case key.Matches(msg, m.keys.Bottom):
		next = list.last()
	default:
		return m, nil
	}
	if up || key.Matches(msg, m.keys.Top) {
		m.state.function.logs.follow = false
	}
	m = m.putCursor(list, next).scrollTo(list, next)
	if up && cur == list.first() {
		return m.loadOlder()
	}
	return m, nil
}

// pinCursor fixes the cursor on the line it is on, so following or a change of what is shown
// does not move it.
func (m model) pinCursor() model {
	list := m.logList()
	return m.putCursor(list, m.cursorItem(list))
}

// putCursor puts the cursor on item i of list. Off the newest line it stops following.
func (m model) putCursor(list logList, i int) model {
	l := &m.state.function.logs
	if i < 0 || i >= len(list.items) || list.items[i].line < 0 {
		return m
	}
	l.cursor, l.pinned = l.lines[list.items[i].line], true
	if i != list.last() {
		l.follow = false
	}
	return m
}

// logsRelaid keeps the cursor in sight once what is shown changed: the filter, the level, the
// grouping or the column of times.
func (m model) logsRelaid() model {
	list := m.logList()
	return m.scrollTo(list, m.cursorItem(list))
}

// scrollTo scrolls as little as keeps item cur of list in sight, its first rows when it is
// taller than the view, and the head above it too when it is the first line.
func (m model) scrollTo(list logList, cur int) model {
	l := &m.state.function.logs
	if cur < 0 {
		l.scroll = 0
		return m
	}
	height := functionBodyHeight()
	below, rows := m.rowsAround(list, cur)
	if below < l.scroll {
		l.scroll = below
	}
	if below+rows > l.scroll+height {
		l.scroll = below + rows - height
	}
	if cur == list.first() {
		l.scroll = below + rows + 1 - height
	}
	l.scroll = max(0, l.scroll)
	return m
}

// rowsAround is how many drawn rows are below item cur of list, and how many it takes.
func (m model) rowsAround(list logList, cur int) (below, rows int) {
	l := &m.state.function.logs
	for i := len(list.items) - 1; i > cur; i-- {
		below += l.itemRows(list.items[i])
	}
	return below, l.itemRows(list.items[cur])
}

// itemRows is how many rows an item of the tab takes drawn.
func (l *logState) itemRows(it logItem) int {
	if it.line < 0 {
		return 1
	}
	return len(l.cache.drawn(l.lines[it.line], l.filter.text, l.room()))
}

// stepRows is the line about by rows from item cur of list, down when by is above 0 and up when
// below: the first one that far, or the last line on the way.
func (m model) stepRows(list logList, cur, by int) int {
	dir := 1
	if by < 0 {
		dir, by = -1, -by
	}
	l := &m.state.function.logs
	moved := 0
	for moved < by {
		next := list.step(cur, dir)
		if next == cur {
			break
		}
		moved += l.itemRows(list.items[next])
		cur = next
	}
	return cur
}

// logsCursorLine is the line under the cursor, if any is shown.
func (m model) logsCursorLine() (cli.LogEvent, bool) {
	list := m.logList()
	cur := m.cursorItem(list)
	if cur < 0 {
		return cli.LogEvent{}, false
	}
	return m.state.function.logs.lines[list.items[cur].line], true
}

// cursorItem is the item of list under the cursor: the newest line while following or before the
// cursor is pinned, the line it is pinned to, or, when that one is not shown, the newest shown
// before it. It is -1 when no line is shown.
func (m model) cursorItem(list logList) int {
	l := &m.state.function.logs
	last := list.last()
	if l.follow || !l.pinned || last < 0 {
		return last
	}
	near := -1
	for i, it := range slices.Backward(list.items) {
		if it.line < 0 {
			continue
		}
		e := l.lines[it.line]
		if e.Time.Equal(l.cursor.Time) && e == l.cursor {
			return i
		}
		if near < 0 && !e.Time.After(l.cursor.Time) {
			near = i
		}
		if !l.grouped && e.Time.Before(l.cursor.Time) {
			break
		}
	}
	if near < 0 {
		return list.first()
	}
	return near
}

// logItem is what the logs tab shows a row or more of: a line of the logs, or the rule after the
// lines of a request when they are grouped.
type logItem struct {
	// line is the index of the line among the lines read, -1 for a rule.
	line int
	// req is the index of the request the line belongs to among the function's, -1 for none.
	req int
}

// logList is what the logs tab shows, in order, and how many of its items are lines.
type logList struct {
	items []logItem
	lines int
}

// first is the first item of the list that is a line, or -1.
func (l logList) first() int {
	for i, it := range l.items {
		if it.line >= 0 {
			return i
		}
	}
	return -1
}

// last is the last item of the list that is a line, or -1.
func (l logList) last() int {
	for i, it := range slices.Backward(l.items) {
		if it.line >= 0 {
			return i
		}
	}
	return -1
}

// step is the line dir lines from item i, past the rules, or i at either end.
func (l logList) step(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(l.items); j += dir {
		if l.items[j].line >= 0 {
			return j
		}
	}
	return i
}

// logLayout keeps what the logs tab shows, so a frame does not read every line again: the rank
// of each line read, and the list laid out last, with what it was laid out for.
type logLayout struct {
	ranks map[cli.LogEvent]logRank
	key   layoutKey
	list  logList
}

// layoutKey is what a list of the logs was laid out for. The lines are told apart by how many
// there are and the first and the last of them, as a read adds to or drops from either end.
type layoutKey struct {
	lines, requests int
	first, last     cli.LogEvent
	filter          string
	least           logRank
	grouped         bool
}

// rank is the rank of the level line e was logged at, read once.
func (c *logLayout) rank(e cli.LogEvent) logRank {
	if c == nil {
		return rankOf(lineLevel(e.Message))
	}
	if c.ranks == nil || len(c.ranks) > 2*maxLogLines {
		c.ranks = map[cli.LogEvent]logRank{}
	}
	r, ok := c.ranks[e]
	if !ok {
		r = rankOf(lineLevel(e.Message))
		c.ranks[e] = r
	}
	return r
}

// keeps reports whether the tab shows line e: the filter holds it, and it is at the level shown
// from or above.
func (l *logState) keeps(e cli.LogEvent) bool {
	return l.filter.matches(e.Message) && (l.least == rankAll || l.layout.rank(e) >= l.least)
}

// room is the width beside the column of times.
func (l *logState) room() int { return logWidth() - l.stamps.width() }

// logList is what the logs tab shows, as the layout last laid it out while the lines, the
// filter, the level and the grouping are those it was laid out for.
func (m model) logList() logList {
	f := &m.state.function
	l := &f.logs
	k := layoutKey{lines: len(l.lines), requests: len(f.requests), filter: l.filter.text, least: l.least, grouped: l.grouped}
	if len(l.lines) > 0 {
		k.first, k.last = l.lines[0], l.lines[len(l.lines)-1]
	}
	if c := l.layout; c != nil && c.list.items != nil && c.key == k {
		return c.list
	}
	list := m.layLogs()
	if l.layout != nil {
		l.layout.key, l.layout.list = k, list
	}
	return list
}

// layLogs lays out the lines the tab shows: in the order they were logged, or, grouped, the lines
// of each request together where its first line is, with a rule after them. A line of no request
// stays where it is, and a request none of whose lines are shown has no rule.
func (m model) layLogs() logList {
	f := &m.state.function
	l := &f.logs
	list := logList{items: make([]logItem, 0, len(l.lines))}
	add := func(i, req int) {
		list.items = append(list.items, logItem{line: i, req: req})
		list.lines++
	}
	if !l.grouped {
		for i, e := range l.lines {
			if l.keeps(e) {
				add(i, -1)
			}
		}
		return list
	}
	of := make(map[cli.LogEvent]int, len(l.lines))
	for r, req := range f.requests {
		for _, e := range req.lines {
			of[e] = r
		}
	}
	groups := make([][]int, len(f.requests))
	reqs := make([]int, len(l.lines))
	for i, e := range l.lines {
		r, ok := of[e]
		if !ok {
			r = -1
		}
		reqs[i] = r
		if r >= 0 {
			groups[r] = append(groups[r], i)
		}
	}
	done := make([]bool, len(f.requests))
	for i, r := range reqs {
		switch {
		case r < 0:
			if l.keeps(l.lines[i]) {
				add(i, -1)
			}
		case !done[r]:
			done[r] = true
			shown := false
			for _, j := range groups[r] {
				if l.keeps(l.lines[j]) {
					add(j, r)
					shown = true
				}
			}
			if shown {
				list.items = append(list.items, logItem{line: -1, req: r})
			}
		}
	}
	return list
}

// logRow is one drawn line of the logs, before it is painted: a line of the message of an event,
// the first with the event's time in front of it, or the rule after the lines of a request.
type logRow struct {
	at    time.Time
	first bool
	c     cell
	// item is the item of the tab's list the row draws, and rule says it draws a rule.
	item int
	rule bool
}

// ruleRows are the rows of the rule after the lines of a request.
var ruleRows = []logRow{{rule: true}}

// logWidth is the width the lines of the logs are drawn in, right of the cursor's marker.
func logWidth() int { return contentWidth - 4 }

// logRows are the drawn lines of the events the tab keeps, tinted, with what the filter looks for
// marked, and wrapped to the width.
func (m model) logRows(events []cli.LogEvent) []logRow {
	l := &m.state.function.logs
	var out []logRow
	for _, e := range events {
		if l.keeps(e) {
			out = append(out, l.cache.drawn(e, l.filter.text, l.room())...)
		}
	}
	return out
}

// lastRows are the last n drawn rows of the logs tab, or more to finish an item, and whether
// they are all of them.
func (m model) lastRows(n int) ([]logRow, bool) {
	return m.tailRows(m.logList(), n)
}

// tailRows are the last n drawn rows of the items of list, or more to finish an item, each with
// the item it draws, and whether they are all of them. Only what is in sight is tinted, so a long
// log draws quickly.
func (m model) tailRows(list logList, n int) ([]logRow, bool) {
	l := &m.state.function.logs
	room := l.room()
	var chunks [][]logRow
	var of []int
	count, i := 0, len(list.items)-1
	for ; i >= 0 && count < n; i-- {
		rows := ruleRows
		if it := list.items[i]; it.line >= 0 {
			rows = l.cache.drawn(l.lines[it.line], l.filter.text, room)
		}
		chunks = append(chunks, rows)
		of = append(of, i)
		count += len(rows)
	}
	out := make([]logRow, 0, count)
	for k, rows := range slices.Backward(chunks) {
		for _, r := range rows {
			r.item = of[k]
			out = append(out, r)
		}
	}
	return out, i < 0
}

// entryRows are the drawn lines of one event, where it holds mark marked, in width with the time
// in front of it.
func entryRows(e cli.LogEvent, mark string, width int) []logRow {
	return stampedRows(e, mark, width-stampWidth)
}

// stampedRows are the drawn lines of one event, where it holds mark marked, wrapped to room, the
// width beside the column of times.
func stampedRows(e cli.LogEvent, mark string, room int) []logRow {
	cells := markedLogCells(e.Message, mark, room)
	out := make([]logRow, len(cells))
	for i, c := range cells {
		out[i] = logRow{at: e.Time, first: i == 0, c: c}
	}
	return out
}

// paint draws a row in width columns: the time, or the indent under it, and the row's spans.
func (r logRow) paint(width int) string { return r.paintIn(width, stampClock) }

// paintIn draws a row in width columns with the column of times as s says: the time of its
// line, how long ago it was, or nothing, and the indent under it on the rows after the first.
func (r logRow) paintIn(width int, s stampMode) string {
	w := s.width()
	stamp := strings.Repeat(" ", w)
	if r.first {
		switch s {
		case stampClock:
			stamp = styles.Faint(r.at.Local().Format("15:04:05.000")) + "  "
		case stampAgo:
			stamp = styles.Faint(fmt.Sprintf("%*s", agoWidth-2, truncate(formatAgo(r.at), agoWidth-2))) + "  "
		}
	}
	return stamp + renderCell(r.c, column{width: width - w}, nil)
}

// paintLogRow draws a row of the tab in width columns, after the cursor's marker on the rows of
// the line under it and a space on the others.
func (m model) paintLogRow(r logRow, list logList, cursor, width int) string {
	gutter := " "
	if !r.rule && r.item == cursor {
		gutter = styles.Accent("▌")
	}
	if r.rule {
		return gutter + m.ruleLine(list.items[r.item].req, width)
	}
	return gutter + r.paintIn(width, m.state.function.logs.stamps)
}

// ruleLine is the faint rule under the lines of a request grouped together: its id and how long
// it ran, and how it ended when it did not end well.
func (m model) ruleLine(req, width int) string {
	f := &m.state.function
	c := cell{styles.S("╌╌ ", styles.ToneRule)}
	if req >= 0 && req < len(f.requests) {
		r := f.requests[req]
		c = append(c, styles.S(shortRequest(r.id), styles.ToneFaint))
		if r.reported {
			c = append(c, sep(), styles.S(formatMillis(r.duration), styles.ToneMuted))
		}
		if r.failed || !r.reported {
			c = append(c, sep())
			c = append(c, requestStatus(r, time.Duration(f.inputs.Timeout)*time.Second)...)
		}
		c = append(c, styles.S(" ", styles.ToneFaint))
	}
	c = append(c, styles.S(strings.Repeat("╌", max(0, width-cellWidth(c))), styles.ToneRule))
	return renderCell(c, column{width: width}, nil)
}

// rowCache keeps the drawn lines of the events a frame tinted, at one width and mark, so
// scrolling through a long log tints and wraps each line once. A row holds tones, not colours, so
// a change of palette repaints it as it is; it starts again on one all the same, should a row ever
// hold more. A page keeps one from when it opens; without one every line is drawn again.
type rowCache struct {
	room    int
	mark    string
	palette int
	rows    map[cli.LogEvent][]logRow
}

func newRowCache() *rowCache { return &rowCache{} }

// drawn are stampedRows, from the cache when it holds them. It starts again when the room, the
// mark or the palette changes, or when it holds twice the lines the logs keep.
func (c *rowCache) drawn(e cli.LogEvent, mark string, room int) []logRow {
	if c == nil {
		return stampedRows(e, mark, room)
	}
	if c.rows == nil || c.room != room || c.mark != mark || c.palette != styles.Gen() || len(c.rows) > 2*maxLogLines {
		c.room, c.mark, c.palette, c.rows = room, mark, styles.Gen(), map[cli.LogEvent][]logRow{}
	}
	if rows, ok := c.rows[e]; ok {
		return rows
	}
	rows := stampedRows(e, mark, room)
	c.rows[e] = rows
	return rows
}

// logsHead says how far back the logs go, how many lines the filter and the level keep, whether
// they are grouped, and what reads the lines before.
func (m model) logsHead() string {
	l := &m.state.function.logs
	var parts []string
	if l.filter.text != "" || l.least != rankAll {
		parts = append(parts, fmt.Sprintf("%s of %s lines", formatInt(m.logList().lines), formatInt(len(l.lines))))
	}
	if l.grouped {
		parts = append(parts, "grouped by request")
	}
	switch {
	case l.loadingOlder:
		parts = append(parts, "reading the hour before "+formatTime(l.from)+"…")
	case l.emptyOlder > 0:
		parts = append(parts, "nothing logged in "+hoursBefore(l.emptyOlder)+" "+formatTime(l.from.Add(time.Duration(l.emptyOlder)*olderStep)), "↑ reads further back")
	default:
		parts = append(parts, "from "+formatTime(l.from), "↑ at the top reads the hour before")
	}
	head := styles.Faint(strings.Join(parts, " · "))
	if l.loadingOlder {
		head = styles.Accent(m.spinner.View()) + " " + head
	}
	return head
}

// logsMeta is what the frame says of the level the logs are shown from, beside the filter, as
// level ≥ warn, or nothing while every line is.
func (m model) logsMeta() string {
	least := m.state.function.logs.least
	if least == rankAll {
		return ""
	}
	return styles.Render(styles.S("level ", styles.ToneFaint), styles.S("≥ "+rankNames[least], styles.ToneText))
}

// hoursBefore names n steps of older lines: the hour before, the 3 hours before.
func hoursBefore(n int) string {
	if n == 1 {
		return "the hour before"
	}
	return fmt.Sprintf("the %d hours before", n)
}

// logsError says why the logs could not be read, and how to try again.
func (m model) logsError() string {
	return strings.Join(cellLines(m.couldNotRead("the logs", m.state.function.logs.err, styles.ToneDanger), logWidth()), "\n ")
}

// logsEmpty says why the tab shows no line: nothing was logged, the filter matches none, or none
// is at the level shown from.
func (m model) logsEmpty() string {
	l := &m.state.function.logs
	level := ""
	if l.least != rankAll {
		level = " at " + rankNames[l.least] + " or above"
	}
	switch {
	case l.filter.text != "" && level != "":
		return "no line" + level + " matches " + styles.Clean(l.filter.text) + ": " + m.keys.Back.Help().Key + " clears the filter"
	case l.filter.text != "":
		return noMatch("line", styles.Clean(l.filter.text))
	case level != "":
		return "no line" + level + ": " + m.keys.Level.Help().Key + " changes the level"
	}
	return fmt.Sprintf("nothing logged since %s", formatTime(l.from))
}

func (m model) logsView() string {
	f := &m.state.function
	l := &f.logs
	switch {
	case f.logGroup == "":
		return " " + styles.Muted("no log group: deploy the function first")
	case !l.loaded && l.err != "":
		return " " + m.logsError()
	case !l.loaded:
		return " " + styles.Muted(readingNote("the logs"))
	}
	w := m.logsWindow()
	if w.all && len(w.rows) == 0 {
		return lipgloss.NewStyle().PaddingLeft(1).Render(m.logsHead() + "\n\n" + styles.Muted(m.logsEmpty()))
	}
	lines := make([]string, 0, w.end-w.start)
	for i := w.start; i < w.end; i++ {
		if i < w.head {
			lines = append(lines, " "+m.logsHead())
			continue
		}
		lines = append(lines, m.paintLogRow(w.rows[i-w.head], w.list, w.cursor, logWidth()))
	}
	return strings.Join(lines, "\n")
}

// logsWindow is what the logs tab has in sight: the list it shows and the item under the cursor,
// the last drawn rows, a head line above them once they are all of them, and the lines from start
// to end of the head and the rows.
type logsWindow struct {
	list       logList
	cursor     int
	rows       []logRow
	all        bool
	head       int
	start, end int
}

// logsWindow works out the lines of the logs in sight at the scroll, the newest at the bottom.
func (m model) logsWindow() logsWindow {
	l := &m.state.function.logs
	height := functionBodyHeight()
	w := logsWindow{list: m.logList()}
	w.cursor = m.cursorItem(w.list)
	w.rows, w.all = m.tailRows(w.list, l.scroll+height)
	if w.all {
		w.head = 1
	}
	total := len(w.rows) + w.head
	w.end = total - min(l.scroll, max(0, total-height))
	w.start = max(0, w.end-height)
	return w
}

// logsPosition is the lines of the logs in sight, counted as the tab counts them, a line for each
// event it shows, however many rows it wraps to.
func (m model) logsPosition() string {
	f := &m.state.function
	if f.logGroup == "" || !f.logs.loaded {
		return ""
	}
	w := m.logsWindow()
	total := w.list.lines
	from, to := max(0, w.start-w.head), max(0, w.end-w.head)
	below, shown := 0, 0
	for _, r := range w.rows[to:] {
		if r.first && !r.rule {
			below++
		}
	}
	for i, r := range w.rows[from:to] {
		if !r.rule && (r.first || i == 0) {
			shown++
		}
	}
	return scrollCue(total-below-shown, shown, total)
}
