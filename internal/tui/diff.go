package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// The runes around a value presentable stands in for, so jsonLines can write it without quotes.
// They are in the private use area, which json leaves as it is.
const (
	placeholderOpen  = ""
	placeholderClose = ""
)

// placeholders strips the runes of a placeholder from strings that hold them already.
var placeholders = strings.NewReplacer(placeholderOpen, "", placeholderClose, "")

// presentable replaces koldstart's own values, refs, secrets and outputs, with what they stand
// for, so a diff shows them as a person writes them.
func presentable(v any) any {
	switch x := v.(type) {
	case string:
		return placeholders.Replace(x)
	case map[string]any:
		if _, ok := x["$koldstart"]; ok {
			return placeholderOpen + placeholders.Replace(describe(x)) + placeholderClose
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = presentable(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = presentable(e)
		}
		return out
	}
	return v
}

// describe says what one of koldstart's own values stands for: secret ssm:/orders/key, ref
// resource/table.Arn, and a join with its refs in ${}, as ${ref resource/table.Arn}/index/*.
func describe(v map[string]any) string {
	switch {
	case engine.IsSecret(v):
		return "secret " + engine.Describe(v)
	case v["$koldstart"] == "join":
		parts, _ := v["parts"].([]any)
		var b strings.Builder
		for _, part := range parts {
			if s, ok := part.(string); ok {
				b.WriteString(s)
				continue
			}
			b.WriteString("${" + engine.Describe(part) + "}")
		}
		return b.String()
	}
	return engine.Describe(v)
}

// unquote writes the placeholders json quoted in parentheses instead.
var unquote = strings.NewReplacer(`"`+placeholderOpen, "(", placeholderClose+`"`, ")")

// jsonLines writes a value as indented json, a line each, or none for nil. A ref, secret or
// output shows in parentheses, unquoted, so it does not read as a string set to that text.
func jsonLines(v any) []string {
	if v == nil {
		return nil
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(presentable(v)); err != nil {
		return []string{styles.Clean(err.Error())}
	}
	data := unquote.Replace(strings.TrimSuffix(b.String(), "\n"))
	return strings.Split(styles.CleanBlock(data), "\n")
}

// diffState is the full change of one resource: in a plan, or between two versions of the state.
type diffState struct {
	title  string
	detail string
	lines  []diffLine
	scroll int
	from   page
	// change is the change navigated to, numbered from 0, or -1 when none is.
	change int
	// opened are the folds shown, by the first line each hides, and unfolded shows them all.
	opened   map[int]bool
	unfolded bool
	// folds counts the folds opened and closed, so the rows kept are laid out again.
	folds int
	// respaced is set when the two sides differ only in how json in a string is spaced.
	respaced bool
	// wrapped is the rows as the last frame laid them out, so the next does not again.
	wrapped *diffRows
}

// openDiff shows what changes in a resource, from before to after, on its first change: scrolled
// so it is in sight, with a few lines above it, as the inputs of a function run longer than a
// short terminal and the line that changed is often below them.
func (m model) openDiff(title, detail string, before, after any) (model, tea.Cmd) {
	d := &m.state.diff
	*d = diffState{title: title, detail: detail, lines: diffDocs(before, after), from: m.page, change: -1, opened: map[int]bool{}, wrapped: &diffRows{}}
	l := d.layout(diffTextWidth())
	if len(l.changes) > 0 {
		d.change = 0
		if diffHeaderLines+l.changes[0][0] >= contentHeight {
			d.reveal(l, 0)
		}
	} else {
		d.respaced = !slices.Equal(jsonLines(before), jsonLines(after))
	}
	return m.switchPage(diffPage), nil
}

// diffWidth is the width the diff page draws its lines in, and diffTextWidth the width of the
// json in them, after the marker of the change navigated to and the gutter of + and -, which
// starts rowInset in as the rows of every page do.
func diffWidth() int     { return detailWidth() }
func diffTextWidth() int { return diffWidth() - rowInset - 2 }

// diffHeaderLines are the lines above a diff's json, diffContext the lines kept around each
// change, and minFold the fewest kept lines folded into one.
const (
	diffHeaderLines = 3
	diffContext     = 3
	minFold         = 2
)

func (m model) diffUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	d := &m.state.diff
	k := m.keys
	switch {
	case key.Matches(msg, k.Back, k.Quit):
		return m.switchPage(d.from), nil
	case key.Matches(msg, k.NextDiff):
		d.jump(1)
	case key.Matches(msg, k.PrevDiff):
		d.jump(-1)
	case key.Matches(msg, k.View):
		d.unfold()
	case key.Matches(msg, k.Unfold):
		d.toggleFolds()
	default:
		l := d.layout(diffTextWidth())
		d.scroll = scrollBy(msg, k, d.scroll, contentHeight, maxOffset(d.total(l), contentHeight))
		d.follow(l)
	}
	return m, nil
}

// total is how many lines the diff page has, laid out as l.
func (d diffState) total(l diffRows) int {
	if len(d.lines) == 0 {
		return diffHeaderLines + 1
	}
	return diffHeaderLines + len(l.rows)
}

// top is the first line in sight, the scroll as far as the lines go.
func (d diffState) top(l diffRows) int {
	return max(0, min(d.scroll, maxOffset(d.total(l), contentHeight)))
}

// inSight reports whether any of the rows from to to is in sight.
func (d diffState) inSight(l diffRows, from, to int) bool {
	top := d.top(l)
	return diffHeaderLines+to > top && diffHeaderLines+from < top+contentHeight
}

// jump goes to the next change, or the one before, from the change navigated to, or from the
// lines in sight when none is.
func (d *diffState) jump(dir int) {
	l := d.layout(diffTextWidth())
	n := len(l.changes)
	if n == 0 {
		return
	}
	next := d.change + dir
	if d.change < 0 {
		top := d.top(l)
		next = n
		for c, ch := range l.changes {
			if diffHeaderLines+ch[0] >= top {
				next = c
				break
			}
		}
		if dir < 0 {
			next--
		}
	}
	d.change = max(0, min(next, n-1))
	d.reveal(l, d.change)
}

// reveal scrolls so change c is in sight, with diffContext lines above it, unless all of it is
// already.
func (d *diffState) reveal(l diffRows, c int) {
	from, to := diffHeaderLines+l.changes[c][0], diffHeaderLines+l.changes[c][1]
	if top := d.top(l); from >= top && to <= top+contentHeight {
		return
	}
	d.scroll = max(0, min(from-diffContext, maxOffset(d.total(l), contentHeight)))
}

// follow forgets the change navigated to once it is scrolled out of sight, for the first change
// in sight, if any.
func (d *diffState) follow(l diffRows) {
	if d.change >= 0 && d.change < len(l.changes) && d.inSight(l, l.changes[d.change][0], l.changes[d.change][1]) {
		return
	}
	d.change = -1
	for c, ch := range l.changes {
		if d.inSight(l, ch[0], ch[1]) {
			d.change = c
			return
		}
	}
}

// foldInSight is the row of the first fold in sight, the one enter opens, or -1.
func (d diffState) foldInSight(l diffRows) int {
	top := d.top(l) - diffHeaderLines
	for i := max(0, top); i < min(len(l.rows), top+contentHeight); i++ {
		if l.rows[i].fold >= 0 {
			return i
		}
	}
	return -1
}

// unfold shows the lines of the first fold in sight.
func (d *diffState) unfold() {
	l := d.layout(diffTextWidth())
	r := d.foldInSight(l)
	if r < 0 {
		return
	}
	if d.opened == nil {
		d.opened = map[int]bool{}
	}
	d.opened[l.rows[r].fold] = true
	d.folds++
	d.scroll = d.top(d.layout(diffTextWidth()))
}

// toggleFolds shows every line, or folds them again, keeping the change navigated to in sight.
func (d *diffState) toggleFolds() {
	d.unfolded = !d.unfolded
	d.opened = map[int]bool{}
	d.folds++
	l := d.layout(diffTextWidth())
	d.scroll = d.top(l)
	if d.change >= 0 && d.change < len(l.changes) {
		d.reveal(l, d.change)
	}
}

// diffFold is a run of kept lines a diff hides, from the line at from to the one before to.
type diffFold struct{ from, to int }

// foldsOf are the runs of kept lines in a diff, but for diffContext lines on either side of each
// change, that are minFold lines or more.
func foldsOf(lines []diffLine) []diffFold {
	var out []diffFold
	for i := 0; i < len(lines); {
		if lines[i].op != ' ' {
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j].op == ' ' {
			j++
		}
		from, to := i, j
		if i > 0 {
			from += diffContext
		}
		if j < len(lines) {
			to -= diffContext
		}
		if to-from >= minFold {
			out = append(out, diffFold{from, to})
		}
		i = j
	}
	return out
}

// diffRows are the rows of a diff laid out to width in the palette numbered palette, with folds
// as they were after folds changes to them. They hold tones, not colours, but are laid out again
// in a new palette all the same, so nothing kept outlives one.
type diffRows struct {
	width   int
	palette int
	folds   int
	rows    []diffRow
	// changes are the rows of each change, from its first to the one after its last.
	changes [][2]int
	// foldable is how many folds the diff has, open or not.
	foldable int
}

// diffRow is a row of a diff as it is drawn: a part of a line, with the line's op, whether it is
// the line's first part, and the change it is in, -1 for none; or a fold, by the first line it
// hides, -1 for none.
type diffRow struct {
	op     byte
	first  bool
	spans  []diffSpan
	change int
	fold   int
}

// diffSpan is a run of a row in one tone, marked when it is the part of a field that changed.
type diffSpan struct {
	text string
	tone styles.Tone
	mark bool
}

// layout lays the lines of the diff out to width, folded, or takes them as the last frame did.
func (d diffState) layout(width int) diffRows {
	if w := d.wrapped; w != nil && w.width == width && w.palette == styles.Gen() && w.folds == d.folds && width > 0 && w.rows != nil {
		return *w
	}
	out := diffRows{width: width, palette: styles.Gen(), folds: d.folds, rows: make([]diffRow, 0, len(d.lines))}
	folds := foldsOf(d.lines)
	out.foldable = len(folds)
	next := 0
	for i := 0; i < len(d.lines); {
		if next < len(folds) && folds[next].from == i {
			f := folds[next]
			next++
			if !d.unfolded && !d.opened[f.from] {
				out.rows = append(out.rows, foldRow(d.lines[f.from].text, f.to-f.from, f.from, width))
				i = f.to
				continue
			}
		}
		l := d.lines[i]
		c := -1
		if l.op != ' ' {
			if i == 0 || d.lines[i-1].op == ' ' {
				out.changes = append(out.changes, [2]int{len(out.rows), len(out.rows)})
			}
			c = len(out.changes) - 1
		}
		spans := lineSpans(l)
		for k, p := range wrapParts(l.text, width) {
			out.rows = append(out.rows, diffRow{op: l.op, first: k == 0, spans: partSpans(spans, p), change: c, fold: -1})
		}
		if c >= 0 {
			out.changes[c][1] = len(out.rows)
		}
		i++
	}
	if d.wrapped != nil {
		*d.wrapped = out
	}
	return out
}

// foldRow is the row that stands for n kept lines folded away from the line at from, the first
// of them first, at its indent.
func foldRow(first string, n, from, width int) diffRow {
	lead := min(indentOf(first), width/2)
	label := truncate("⋯ "+plural(n, "unchanged line"), max(0, width-lead))
	return diffRow{op: ' ', first: true, spans: []diffSpan{{text: strings.Repeat(" ", lead) + label, tone: styles.ToneFaint}}, change: -1, fold: from}
}

// lineSpans tints a line of a diff within its tone: on a line kept, keys and punctuation faint,
// values muted, and refs, secrets and outputs in info; on a line added or removed, all but the
// punctuation in the tone of + or -, the parts that changed marked.
func lineSpans(l diffLine) []diffSpan {
	text := l.text
	tone, keyTone, brackets, held := styles.ToneMuted, styles.ToneFaint, styles.ToneFaint, styles.ToneInfo
	switch l.op {
	case '+':
		tone = styles.ToneSuccess
		keyTone, brackets, held = tone, tone, tone
	case '-':
		tone = styles.ToneDanger
		keyTone, brackets, held = tone, tone, tone
	}
	keyEnd, from, to := splitLine(text)
	lead := indentOf(text)
	spans := make([]diffSpan, 0, 6)
	add := func(s string, t styles.Tone) {
		if s != "" {
			spans = append(spans, diffSpan{text: s, tone: t})
		}
	}
	add(text[:lead], styles.ToneFaint)
	if keyEnd > 0 {
		add(text[lead:keyEnd], keyTone)
		add(text[keyEnd:from], styles.ToneFaint)
	}
	value := text[from:to]
	switch {
	case strings.HasPrefix(value, embeddedTag):
		add(embeddedTag, styles.ToneFaint)
		add(value[len(embeddedTag):], brackets)
	case value != "" && strings.ContainsRune("{}[]", rune(value[0])):
		add(value, brackets)
	case strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")"):
		add(value, held)
	default:
		add(value, tone)
	}
	add(text[to:], styles.ToneFaint)
	return markSpans(spans, l.marks, tone)
}

// markSpans marks the byte ranges marks of the text of spans, in tone.
func markSpans(spans []diffSpan, marks [][2]int, tone styles.Tone) []diffSpan {
	if len(marks) == 0 {
		return spans
	}
	var out []diffSpan
	at := 0
	for _, s := range spans {
		start, end := at, at+len(s.text)
		for at < end {
			cut, mark := end, false
			for _, m := range marks {
				if m[0] <= at && at < m[1] {
					cut, mark = min(end, m[1]), true
					break
				}
				if at < m[0] && m[0] < cut {
					cut = m[0]
				}
			}
			piece := diffSpan{text: s.text[at-start : cut-start], tone: s.tone}
			if mark {
				piece.tone, piece.mark = tone, true
			}
			out = append(out, piece)
			at = cut
		}
	}
	return out
}

// partSpans are the spans of the part p of a line, behind the part's indent.
func partSpans(spans []diffSpan, p wrapPart) []diffSpan {
	out := make([]diffSpan, 0, len(spans)+1)
	if p.indent > 0 {
		out = append(out, diffSpan{text: strings.Repeat(" ", p.indent), tone: styles.ToneFaint})
	}
	if p.from < 0 {
		return append(out, diffSpan{text: p.text, tone: styles.ToneBody})
	}
	from, to, at := p.from, p.from+len(p.text), 0
	for _, s := range spans {
		end := at + len(s.text)
		if lo, hi := max(from, at), min(to, end); lo < hi {
			piece := s
			piece.text = s.text[lo-at : hi-at]
			out = append(out, piece)
		}
		at = end
	}
	return out
}

// markStyle is how a changed part of a field is painted: in reverse of its tone, bold, so it
// stands out of a line in the same tone.
func markStyle(t styles.Tone) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	if styles.Reverse() {
		return s.Foreground(styles.P.Color(t)).Reverse(true)
	}
	return s.Foreground(styles.P.Base).Background(styles.P.Color(t))
}

// drawRow draws a row of the diff: the marker of the change navigated to, the gutter of + and -,
// rowInset in, on every part of a line, and the row's spans. target is set on the fold enter
// opens.
func drawRow(r diffRow, current, target bool) string {
	var b strings.Builder
	if current {
		b.WriteString(styles.Accent("▌") + pad(rowInset-1, nil))
	} else {
		b.WriteString(pad(rowInset, nil))
	}
	switch {
	case r.fold >= 0:
		b.WriteString("  ")
		tone := styles.ToneFaint
		if target {
			tone = styles.ToneAccent
		}
		for _, s := range r.spans {
			b.WriteString(styles.Fg(tone).Render(s.text))
		}
		return b.String()
	case r.op == '+':
		b.WriteString(styles.Fg(styles.ToneSuccess).Render("+ "))
	case r.op == '-':
		b.WriteString(styles.Fg(styles.ToneDanger).Render("- "))
	default:
		b.WriteString("  ")
	}
	for _, s := range r.spans {
		if s.mark {
			b.WriteString(markStyle(s.tone).Render(s.text))
			continue
		}
		b.WriteString(styles.Fg(s.tone).Render(s.text))
	}
	return b.String()
}

// diffCounts are the fields a diff adds, changes and removes.
func (d diffState) diffCounts() (added, changed, removed int) {
	for _, l := range d.lines {
		switch {
		case l.op == '+' && l.counted:
			added++
		case l.op == '-' && l.counted:
			removed++
		case l.op == '-' && l.paired:
			changed++
		}
	}
	return added, changed, removed
}

// changeCounts says how many things were added, changed and removed, as +3 added  ~1 changed
// -2 removed, leaving out what is none.
func changeCounts(added, changed, removed int) []styles.Span {
	var out []styles.Span
	for _, c := range []struct {
		n    int
		sign string
		what string
		tone styles.Tone
	}{{added, "+", " added", styles.ToneSuccess}, {changed, "~", " changed", styles.ToneWarning}, {removed, "-", " removed", styles.ToneDanger}} {
		if c.n == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, styles.S("  ", styles.ToneFaint))
		}
		out = append(out, styles.S(c.sign+formatInt(c.n), c.tone), styles.S(c.what, styles.ToneMuted))
	}
	return out
}

// diffLines are the lines of the diff page, drawn only where they are in sight: the title, what
// happens to the resource and how many fields change, and the diff, folded.
func (m model) diffLines() lazyLines {
	d := m.state.diff
	width := diffWidth()
	counts := changeCounts(d.diffCounts())
	if len(counts) == 0 {
		note := "no change"
		if d.respaced {
			note = "no change but the spacing of json in a string"
		}
		counts = []styles.Span{styles.S(note, styles.ToneFaint)}
	}
	counts = append([]styles.Span{sep()}, counts...)
	countsWidth := ansi.StringWidth(plainText(counts))
	if countsWidth > width {
		counts, countsWidth = nil, 0
	}
	detail := truncate(strings.TrimSpace(styles.Clean(d.detail)), width-countsWidth)
	var lines lazyLines
	lines.add(
		styles.SectionHeader(d.title, width),
		styles.Render(append([]styles.Span{{Text: detail, Tone: styles.ToneMuted}}, counts...)...),
		"",
	)
	if len(d.lines) == 0 {
		lines.add(insetLines([]string{styles.Faint("no inputs on either side")})...)
		return lines
	}
	l := d.layout(diffTextWidth())
	target := d.foldInSight(l)
	lines.addDrawn(len(l.rows), func(i int) string {
		r := l.rows[i]
		return drawRow(r, r.change >= 0 && r.change == d.change, i == target)
	})
	return lines
}

// diffPosition is where the diff page is, for the frame's foot: the lines in sight and the change
// navigated to, as lines 12–30 of 80 · change 2 of 5.
func (m model) diffPosition() string {
	d := m.state.diff
	l := d.layout(diffTextWidth())
	parts := []string{scrollCue(d.top(l), contentHeight, d.total(l))}
	if d.change >= 0 && d.change < len(l.changes) {
		parts = append(parts, "change "+formatInt(d.change+1)+" of "+formatInt(len(l.changes)))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// diffHints are the keys of the diff page: ] and [ when there is more than one change, enter on a
// fold in sight, and z when there are folds.
func (m model) diffHints() [][2]string {
	k := m.keys
	d := m.state.diff
	l := d.layout(diffTextWidth())
	h := [][2]string{scrollHint}
	if len(l.changes) > 1 {
		h = append(h, [2]string{k.NextDiff.Help().Key + "/" + k.PrevDiff.Help().Key, "change"})
	}
	if d.foldInSight(l) >= 0 {
		h = append(h, hint(k.View, "unfold"))
	}
	switch {
	case l.foldable > 0 && d.unfolded:
		h = append(h, hint(k.Unfold, "fold all"))
	case l.foldable > 0:
		h = append(h, hint(k.Unfold))
	}
	return append(h, hint(k.Help), hint(k.Quit))
}

// wrapPart is a part of a line as wrapIndented wraps it: text, the bytes of the line from from,
// or -1 when it is not found there, behind indent spaces.
type wrapPart struct {
	indent int
	text   string
	from   int
}

// wrapIndented wraps a line of indented json to width, its continuation lines indented two
// columns past the line's own indent, so a long value stays readable and in the json's shape.
func wrapIndented(s string, width int) []string {
	parts := wrapParts(s, width)
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.Repeat(" ", p.indent) + p.text
	}
	return out
}

// wrapParts is wrapIndented, each part with where it is in the line, so its tones can follow it.
func wrapParts(s string, width int) []wrapPart {
	if ansi.StringWidth(s) <= width {
		return []wrapPart{{text: s}}
	}
	lead := min(len(s)-len(strings.TrimLeft(s, " ")), width/2, max(0, width-2))
	body := s[lead:]
	first := wrapWords(body, max(1, width-lead))[0]
	if !strings.HasPrefix(body, first) {
		first = ""
	}
	out := []wrapPart{{text: s[:lead+len(first)]}}
	rest := strings.TrimLeft(body[len(first):], " ")
	if rest == "" {
		return out
	}
	hang, room := lead+2, width-lead-2
	if first == "" {
		out, hang = nil, lead
	}
	if room < 2 {
		room = max(1, min(2, width))
		hang = max(0, min(hang, width-room))
	}
	at := len(s) - len(rest)
	for _, part := range wrapWords(rest, room) {
		from := -1
		if i := strings.Index(s[at:], part); i >= 0 {
			from = at + i
			at = from + len(part)
		}
		out = append(out, wrapPart{indent: hang, text: part, from: from})
	}
	return out
}

// wrapWords wraps s to width at its spaces and hyphens, breaking a word only where it does not
// fit on a line of its own, as ansi.Wrap does, and hard where ansi.Wrap leaves a part too wide.
func wrapWords(s string, width int) []string {
	var out []string
	for part := range strings.SplitSeq(ansi.Wrap(s, width, ""), "\n") {
		if ansi.StringWidth(part) <= width {
			out = append(out, part)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(part, width, true), "\n")...)
	}
	return out
}

func (m model) diffView() string {
	return indent(scrolledLines(m.diffLines(), m.state.diff.scroll, contentHeight))
}
