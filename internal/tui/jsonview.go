package tui

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// The tones of json by role, as the styles package names them: keys are field labels, as on the
// logs and the diff, strings are values, numbers and booleans are a payload's numbers, null an
// empty value, and a ref, secret or output stands for something koldstart resolves. json written
// out of a string follows embeddedTag, faint, as the diff writes it.
const (
	jsonKeyTone    = styles.ToneFaint
	jsonStringTone = styles.ToneBody
	jsonNumberTone = styles.ToneWarm
	jsonBoolTone   = styles.ToneWarm
	jsonNullTone   = styles.ToneFaint
	jsonPunctTone  = styles.ToneFaint
	jsonRefTone    = styles.ToneInfo
	jsonNoteTone   = styles.ToneFaint
)

// An array or object of more than jsonFoldItems items, or one jsonFoldDepth levels down, starts
// folded. A string of json up to jsonEmbedBytes is shown nested, down to jsonEmbedDepth strings in
// strings.
const (
	jsonFoldItems  = 50
	jsonFoldDepth  = 8
	jsonEmbedBytes = 1 << 20
	jsonEmbedDepth = 8
)

// jsonGutter is the columns left of every line of a jsonView: the cursor's marker, or a drifted
// line's ▲, and a space.
const jsonGutter = 2

// jsonKind is what a node of a jsonView holds.
type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonPlaceholder
	jsonObject
	jsonArray
	jsonText
)

// jsonNode is a value of a document, in a list of them in document order, each followed by its
// descendants up to end.
type jsonNode struct {
	// key is the node's key in its object as the source quotes it, or empty in an array.
	key string
	// text is a scalar as the source writes it, a placeholder as (what it stands for), a line of
	// what is not json, or the quoted string an embedded object or array was written in.
	text   string
	parent int32
	end    int32
	count  int32
	index  int32
	depth  int32
	kind   jsonKind
	// embedded says the object or array was a string of json.
	embedded bool
}

func (n jsonNode) composite() bool { return n.kind == jsonObject || n.kind == jsonArray }

// jsonView shows a document of json a line a value, coloured by role, long strings wrapped under
// their key, objects and arrays folded and unfolded, with a cursor, the path of the line under it,
// copying and search. A page keeps one in its state, hands it the keys with update, and draws it
// with lines or view. It is a value: what it shares between copies, its nodes, marks, folds and
// cache, is never changed in place but for the cache, which holds only what can be drawn again.
type jsonView struct {
	nodes []jsonNode
	// marks are the nodes marked drifted, each with what it was applied with.
	marks  map[int32]string
	folds  *jsonFolds
	cursor int
	scroll int
	// blurred draws no cursor, for a page where the keys go elsewhere.
	blurred bool
	search  filterBox
	query   string
	// from is the node the cursor was on when the search was opened.
	from int32
	// source is the document as it was given in bytes, which c on its top copies as it is, and
	// sourceName what the footer says was copied then, "all of it" when it is empty.
	source     string
	sourceName string
	cache      *jsonCache
}

// jsonFolds are the objects and arrays folded otherwise than by default, by all and then set; a
// new one is made for each change, so a view's copies do not share a change.
type jsonFolds struct {
	all jsonFoldAll
	set map[int32]bool
}

// jsonFoldAll is how every object and array is folded, but those set otherwise.
type jsonFoldAll uint8

const (
	jsonFoldDefault jsonFoldAll = iota
	jsonFoldShut
	jsonFoldOpen
)

// with is the folds with node i folded, or not.
func (f *jsonFolds) with(i int32, shut bool) *jsonFolds {
	out := &jsonFolds{set: map[int32]bool{}}
	if f != nil {
		out.all = f.all
		maps.Copy(out.set, f.set)
	}
	out.set[i] = shut
	return out
}

// jsonRow is a line of a document as it is folded: a node, or the closing bracket of one.
type jsonRow struct {
	node  int32
	close bool
}

// jsonSpan is the bytes from and to of a row's text drawn on one line.
type jsonSpan struct{ from, to int }

// jsonCache is what a view keeps of its drawing: the rows for its folds, where each starts and
// how it wraps at a width, and the lines drawn at that width in a palette for a search. It is
// emptied whenever what it was kept for changes.
type jsonCache struct {
	folds  *jsonFolds
	rowsOK bool
	rows   []jsonRow
	geom   *jsonGeometry
	drawn  map[int]string
	key    jsonDrawnKey
	// query is what matches were found for.
	query     string
	matchesOK bool
	matches   []int32
	// memo is the cell of the row drawn last, so a row wrapped onto many lines is not tinted again
	// for each.
	memo *jsonMemo
}

// jsonMemo is the cell of row k, its matches of query marked.
type jsonMemo struct {
	k     int
	query string
	c     cell
}

// jsonDrawnKey is what drawn lines were drawn for.
type jsonDrawnKey struct {
	width   int
	palette int
	query   string
}

// jsonGeometry is where each row starts among the lines drawn at width, the last entry the number
// of lines, and how the rows taller than a line wrap.
type jsonGeometry struct {
	width  int
	starts []int
	wraps  map[int][]jsonSpan
}

// jsonResult is what a key did to a view that the page acts on: a command of its search, text to
// copy and what to say was copied, or a note for the footer.
type jsonResult struct {
	cmd    tea.Cmd
	copy   string
	copied string
	status string
}

// newJSONView shows a value as json, its refs, secrets and outputs by what they stand for, its
// keys in order. nil shows nothing.
func newJSONView(v any) jsonView {
	if v == nil {
		return jsonView{}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(presentable(v)); err != nil {
		return viewOf(textNodes(err.Error()))
	}
	return viewOf(parseJSON(b.String()))
}

// newJSONViewBytes shows raw json in the order it is written, or, when it is not json, its lines
// as they are. Nothing, or null, shows nothing. c on its top copies the bytes as they are.
func newJSONViewBytes(data []byte) jsonView {
	v := jsonBytesView(bytes.TrimSpace(data))
	if !v.empty() && v.nodes[0].kind != jsonText {
		v.source = string(data)
	}
	return v
}

// jsonBytesView is newJSONViewBytes of data without the space around it.
func jsonBytesView(data []byte) jsonView {
	switch {
	case len(data) == 0 || string(data) == "null":
		return jsonView{}
	case !json.Valid(data):
		return viewOf(textNodes(string(data)))
	case bytes.Contains(data, []byte(`"$koldstart"`)):
		var v any
		if json.Unmarshal(data, &v) == nil {
			return newJSONView(v)
		}
	}
	return viewOf(parseJSON(string(data)))
}

func viewOf(nodes []jsonNode) jsonView {
	return jsonView{nodes: nodes, search: newSearchBox("search the json"), cache: &jsonCache{}}
}

// textNodes are the lines of what is not json, a node each.
func textNodes(s string) []jsonNode {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
	nodes := make([]jsonNode, len(lines))
	for i, l := range lines {
		nodes[i] = jsonNode{kind: jsonText, text: l, parent: -1, index: int32(i), end: int32(i + 1)}
	}
	return nodes
}

// jsonParser reads valid json into nodes, keeping the source's text of every key and scalar.
type jsonParser struct {
	s      string
	i      int
	nodes  []jsonNode
	embeds int
}

// parseJSON reads a document that json.Valid holds valid.
func parseJSON(s string) []jsonNode {
	p := jsonParser{s: s}
	p.value("", -1, 0, 0)
	return p.nodes
}

func (p *jsonParser) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

// value reads a value into a new node and its descendants after it.
//
//nolint:gosec // a document has fewer than 2^31 nodes: its bytes would not fit in memory otherwise
func (p *jsonParser) value(k string, parent, index, depth int32) {
	at := int32(len(p.nodes))
	p.nodes = append(p.nodes, jsonNode{key: k, parent: parent, index: index, depth: depth})
	p.fill(at)
	p.nodes[at].end = int32(len(p.nodes))
}

// fill reads the value at the parser's position into node at.
func (p *jsonParser) fill(at int32) {
	p.space()
	if p.i >= len(p.s) {
		return
	}
	switch c := p.s[p.i]; c {
	case '{', '[':
		p.composite(at, c)
	case '"':
		p.str(at)
	default:
		start := p.i
		for p.i < len(p.s) && strings.IndexByte(" \t\r\n,]}", p.s[p.i]) < 0 {
			p.i++
		}
		kind := jsonNumber
		switch c {
		case 't', 'f':
			kind = jsonBool
		case 'n':
			kind = jsonNull
		}
		p.nodes[at].kind, p.nodes[at].text = kind, p.s[start:p.i]
	}
}

// composite reads an object or array, opened by c, into node at.
func (p *jsonParser) composite(at int32, c byte) {
	kind, closer := jsonObject, byte('}')
	if c == '[' {
		kind, closer = jsonArray, ']'
	}
	p.nodes[at].kind = kind
	depth := p.nodes[at].depth
	p.i++
	var n int32
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] == closer {
			p.i++
			break
		}
		if p.s[p.i] == ',' {
			p.i++
			p.space()
		}
		k := ""
		if kind == jsonObject {
			k = p.quoted()
			p.space()
			p.i++
		}
		p.value(k, at, n, depth+1)
		n++
	}
	p.nodes[at].count = n
}

// quoted reads a string as the source quotes it.
func (p *jsonParser) quoted() string {
	start := p.i
	p.i++
	for p.i < len(p.s) && p.s[p.i] != '"' {
		if p.s[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	p.i = min(p.i+1, len(p.s))
	return p.s[start:p.i]
}

// str reads a string into node at: a placeholder for what it stands for, an object or array when
// it is a string of json, or else a string.
func (p *jsonParser) str(at int32) {
	text := p.quoted()
	p.nodes[at].kind, p.nodes[at].text = jsonString, text
	if inner, ok := placeholderText(text); ok {
		p.nodes[at].kind, p.nodes[at].text = jsonPlaceholder, inner
		return
	}
	if p.embeds >= jsonEmbedDepth || len(text) > jsonEmbedBytes {
		return
	}
	raw, ok := embeddedJSON(text)
	if !ok {
		return
	}
	s, i := p.s, p.i
	p.s, p.i = raw, 0
	p.embeds++
	p.fill(at)
	p.embeds--
	p.s, p.i = s, i
	p.nodes[at].embedded, p.nodes[at].text = true, text
}

// placeholderText is what a quoted placeholder of presentable stands for, in parentheses.
func placeholderText(quoted string) (string, bool) {
	inner, ok := strings.CutPrefix(quoted, `"`+placeholderOpen)
	if !ok {
		return "", false
	}
	if inner, ok = strings.CutSuffix(inner, placeholderClose+`"`); !ok {
		return "", false
	}
	return "(" + placeholders.Replace(jsonUnquote(`"`+inner+`"`)) + ")", true
}

// embeddedJSON is the object or array a quoted string holds as json.
func embeddedJSON(quoted string) (string, bool) {
	body := strings.TrimLeft(quoted[1:], " ")
	if body == "" || body[0] != '{' && body[0] != '[' {
		return "", false
	}
	var raw string
	if json.Unmarshal([]byte(quoted), &raw) != nil {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' && raw[0] != '[' || !json.Valid([]byte(raw)) {
		return "", false
	}
	return raw, true
}

// jsonUnquote is a quoted json string's value, or the text without its quotes when it does not read.
func jsonUnquote(quoted string) string {
	var s string
	if json.Unmarshal([]byte(quoted), &s) != nil {
		return strings.Trim(quoted, `"`)
	}
	return s
}

// empty reports whether there is nothing to show.
func (v jsonView) empty() bool { return len(v.nodes) == 0 }

// folded reports whether node i is drawn folded: as its folds set it, or, by default, an object or
// array of more than jsonFoldItems items or jsonFoldDepth levels down. The root is open.
func (v *jsonView) folded(i int32) bool {
	n := v.nodes[i]
	if !n.composite() || n.count == 0 {
		return false
	}
	if v.folds != nil {
		if shut, ok := v.folds.set[i]; ok {
			return shut
		}
		switch v.folds.all {
		case jsonFoldShut:
			return i != 0
		case jsonFoldOpen:
			return false
		}
	}
	return i != 0 && (n.count > jsonFoldItems || n.depth >= jsonFoldDepth)
}

// rows are the lines of the document as it is folded, as the cache keeps them.
func (v *jsonView) rows() []jsonRow {
	c := v.cache
	if c != nil && c.rowsOK && c.folds == v.folds {
		return c.rows
	}
	rows := v.flatten()
	if c != nil {
		c.folds, c.rowsOK, c.rows, c.geom, c.drawn, c.memo = v.folds, true, rows, nil, nil, nil
	}
	return rows
}

// flatten walks the nodes in order, a row each, skipping what is folded, with a row for each
// closing bracket.
func (v *jsonView) flatten() []jsonRow {
	rows := make([]jsonRow, 0, min(len(v.nodes), 4096))
	var open []int32
	for i := int32(0); int(i) < len(v.nodes); {
		for len(open) > 0 && v.nodes[open[len(open)-1]].end <= i {
			rows = append(rows, jsonRow{node: open[len(open)-1], close: true})
			open = open[:len(open)-1]
		}
		rows = append(rows, jsonRow{node: i})
		if node := v.nodes[i]; node.composite() && node.count > 0 && !v.folded(i) {
			open = append(open, i)
			i++
			continue
		}
		i = v.nodes[i].end
	}
	for _, o := range slices.Backward(open) {
		rows = append(rows, jsonRow{node: o, close: true})
	}
	return rows
}

// last reports whether node i is the last of its parent's, so no comma follows it.
func (v *jsonView) last(i int32) bool {
	p := v.nodes[i].parent
	return p < 0 || v.nodes[i].end == v.nodes[p].end
}

// markOf is what node i, or the marked node it is in, was applied with, whether i is that node,
// and whether it is marked at all.
func (v *jsonView) markOf(i int32) (string, bool, bool) {
	if len(v.marks) == 0 {
		return "", false, false
	}
	for j := i; j >= 0; j = v.nodes[j].parent {
		if was, ok := v.marks[j]; ok {
			return was, j == i, true
		}
	}
	return "", false, false
}

// markKey marks a key of the document's top object as drifted, with what it was applied with, so
// its lines read ▲ and in warning, its first as applied → now.
func (v *jsonView) markKey(name, was string) {
	if v.empty() || v.nodes[0].kind != jsonObject {
		return
	}
	for c := int32(1); c < v.nodes[0].end; c = v.nodes[c].end {
		if jsonUnquote(v.nodes[c].key) == name {
			marks := make(map[int32]string, len(v.marks)+1)
			maps.Copy(marks, v.marks)
			marks[c] = was
			v.marks, v.cache = marks, &jsonCache{}
			return
		}
	}
}

// rowCell is a row's text by role, without its indent, appended to dst.
func (v *jsonView) rowCell(dst cell, r jsonRow) cell {
	n := v.nodes[r.node]
	was, markRoot, marked := v.markOf(r.node)
	tone := func(t styles.Tone) styles.Tone {
		if marked {
			return styles.ToneWarning
		}
		return t
	}
	add := func(s string, t styles.Tone) {
		dst = append(dst, styles.Span{Text: s, Tone: tone(t)})
	}
	comma := func() {
		if !v.last(r.node) {
			add(",", jsonPunctTone)
		}
	}
	open, closer := "{", "}"
	if n.kind == jsonArray {
		open, closer = "[", "]"
	}
	if r.close {
		add(closer, jsonPunctTone)
		comma()
		return dst
	}
	if n.key != "" {
		add(styles.Clean(n.key), jsonKeyTone)
		add(": ", jsonPunctTone)
	}
	if markRoot {
		dst = append(dst, styles.Span{Text: styles.Clean(was), Tone: styles.ToneBody}, styles.Span{Text: " → ", Tone: styles.ToneFaint})
	}
	if n.embedded {
		add(embeddedTag, jsonNoteTone)
	}
	switch {
	case n.kind == jsonText:
		add(styles.Clean(n.text), styles.ToneBody)
	case n.composite() && n.count == 0:
		add(open+closer, jsonPunctTone)
		comma()
	case n.composite() && v.folded(r.node):
		noun := "item"
		if n.kind == jsonObject {
			noun = "key"
		}
		add(open, jsonPunctTone)
		add(" … "+plural(int(n.count), noun)+" ", jsonNoteTone)
		add(closer, jsonPunctTone)
		comma()
	case n.composite():
		add(open, jsonPunctTone)
	default:
		add(styles.Clean(n.text), scalarTone(n.kind))
		comma()
	}
	return dst
}

// scalarTone is the tone of a scalar of a kind.
func scalarTone(k jsonKind) styles.Tone {
	switch k {
	case jsonNumber:
		return jsonNumberTone
	case jsonBool:
		return jsonBoolTone
	case jsonNull:
		return jsonNullTone
	case jsonPlaceholder:
		return jsonRefTone
	}
	return jsonStringTone
}

// jsonRoom is how a row at depth is laid out in width: its indent, the room of its first line,
// and of the lines it wraps onto, which hang two columns further in.
func jsonRoom(width int, depth int32) (lead, first, rest int) {
	avail := max(2, width-jsonGutter)
	lead = min(int(depth)*2, avail/2)
	first = avail - lead
	if first < 2 {
		lead, first = max(0, avail-2), min(2, avail)
	}
	return lead, first, first - min(2, max(0, first-2))
}

// geometry is where the rows start and how they wrap at width, as the cache keeps it.
func (v *jsonView) geometry(width int) *jsonGeometry {
	rows := v.rows()
	c := v.cache
	if c != nil && c.geom != nil && c.geom.width == width {
		return c.geom
	}
	g := &jsonGeometry{width: width, starts: make([]int, len(rows)+1), wraps: map[int][]jsonSpan{}}
	var buf cell
	at := 0
	for k, r := range rows {
		g.starts[k] = at
		_, first, rest := jsonRoom(width, v.nodes[r.node].depth)
		h := 1
		if v.fits(r, first) {
			at++
			continue
		}
		if buf = v.rowCell(buf[:0], r); jsonWidth(buf) > first {
			spans := jsonBreaks(plainText(buf), first, rest)
			g.wraps[k] = spans
			h = len(spans)
		}
		at += h
	}
	g.starts[len(rows)] = at
	if c != nil {
		c.geom, c.drawn = g, nil
	}
	return g
}

// fits reports, without drawing it, that row r is sure to fit in room: a closing bracket, or a
// key and value of printable ascii, unmarked, short enough with all the punctuation they can take.
func (v *jsonView) fits(r jsonRow, room int) bool {
	n := v.nodes[r.node]
	switch {
	case r.close:
		return room >= 2
	case len(v.marks) > 0 || n.embedded || n.kind == jsonText || n.composite() && n.count > 0 && v.folded(r.node):
		return false
	}
	return len(n.key)+len(n.text)+5 <= room && printableASCII(n.key) && printableASCII(n.text)
}

// jsonWidth is how wide a cell's text is, printable ascii measured a byte a column.
func jsonWidth(c cell) int {
	w := 0
	for _, s := range c {
		if printableASCII(s.Text) {
			w += len(s.Text)
		} else {
			w += ansi.StringWidth(s.Text)
		}
	}
	return w
}

// jsonBreaks wraps text into lines, the first first columns wide and the rest rest, as wrapCell
// breaks a cell: at the last space or after the last comma when that fills at least half a line,
// and otherwise where the width runs out, dropping the space it breaks at. Printable ascii is
// measured a byte a column.
func jsonBreaks(text string, first, rest int) []jsonSpan {
	var out []jsonSpan
	width := first
	start, used := 0, 0
	cut, next, cutUsed := -1, -1, 0
	for i := 0; i < len(text); {
		size, w := 1, 1
		if c := text[i]; c < ' ' || c >= utf8.RuneSelf || i+1 < len(text) && text[i+1] >= utf8.RuneSelf {
			g, gw := ansi.FirstGraphemeCluster(text[i:], ansi.GraphemeWidth)
			if g == "" {
				_, size = utf8.DecodeRuneInString(text[i:])
			} else {
				size, w = len(g), gw
			}
		}
		if used+w > width && i > start {
			if cut <= start || cutUsed*2 < width {
				cut, next = i, i
				if text[i] == ' ' {
					next = i + 1
				}
			}
			out = append(out, jsonSpan{start, cut})
			start, i, used, cut, width = next, next, 0, -1, rest
			continue
		}
		switch text[i] {
		case ' ':
			cut, next, cutUsed = i, i+1, used
		case ',':
			cut, next, cutUsed = i+1, i+1, used+1
			if next < len(text) && text[next] == ' ' {
				next++
			}
		}
		used += w
		i += size
	}
	if start < len(text) || len(out) == 0 {
		out = append(out, jsonSpan{start, len(text)})
	}
	return out
}

// lines draws the document at width, the cursor's row on the selection, each line only when it is
// in sight.
func (v jsonView) lines(width int) lazyLines {
	var l lazyLines
	if v.empty() {
		return l
	}
	g := v.geometry(width)
	l.addDrawn(g.starts[len(g.starts)-1], func(i int) string { return v.drawLine(width, i) })
	return l
}

// lineCount is how many lines the document draws at width.
func (v jsonView) lineCount(width int) int {
	if v.empty() {
		return 0
	}
	g := v.geometry(width)
	return g.starts[len(g.starts)-1]
}

// cursorLines are the first and last line the cursor's row draws at width.
func (v jsonView) cursorLines(width int) (int, int) {
	if v.empty() {
		return 0, 0
	}
	g := v.geometry(width)
	k := v.at()
	return g.starts[k], g.starts[k+1] - 1
}

// view draws height lines of the document at width, scrolled so the cursor is in sight.
func (v jsonView) view(width, height int) string {
	top, bottom := v.cursorLines(width)
	return scrolledLines(v.lines(width), keepInSight(v.scroll, top, bottom, height), height)
}

// keepInSight scrolls as little as it takes to bring the lines from top to bottom in sight in
// height, the top first when they do not fit.
func keepInSight(scroll, top, bottom, height int) int {
	scroll = max(scroll, bottom-height+1)
	return max(0, min(scroll, top))
}

// at is the cursor, kept among the rows.
func (v jsonView) at() int {
	return max(0, min(v.cursor, len(v.rows())-1))
}

// drawLine draws line i of the document at width.
func (v *jsonView) drawLine(width, i int) string {
	rows, g := v.rows(), v.geometry(width)
	k, found := slices.BinarySearch(g.starts, i)
	if !found {
		k--
	}
	k = max(0, min(k, len(rows)-1))
	lit := !v.blurred && k == v.at()
	c := v.cache
	if !lit && c != nil {
		want := jsonDrawnKey{width: width, palette: styles.Gen(), query: v.query}
		if c.drawn == nil || c.key != want || len(c.drawn) > 8192 {
			c.drawn, c.key = map[int]string{}, want
		}
		if s, ok := c.drawn[i]; ok {
			return s
		}
	}
	line := v.draw(rows[k], k, i-g.starts[k], g, lit)
	if !lit && c != nil {
		c.drawn[i] = line
	}
	return line
}

// markedCell is the cell of row r, the k-th, with the matches of the search marked, as the cache
// keeps it for the row drawn last.
func (v *jsonView) markedCell(r jsonRow, k int) cell {
	m := v.cache
	if m != nil && m.memo != nil && m.memo.k == k && m.memo.query == v.query {
		return m.memo.c
	}
	c := v.rowCell(nil, r)
	if v.query != "" {
		c = highlight(c, v.query)
	}
	if m != nil {
		m.memo = &jsonMemo{k: k, query: v.query, c: c}
	}
	return c
}

// draw draws part sub of row r, the k-th, lit when the cursor is on it.
func (v *jsonView) draw(r jsonRow, k int, sub int, g *jsonGeometry, lit bool) string {
	lead, first, rest := jsonRoom(g.width, v.nodes[r.node].depth)
	c := v.markedCell(r, k)
	part, room, inset := c, first, lead
	if spans, ok := g.wraps[k]; ok && sub < len(spans) {
		slicer := cellSlicer{c: c}
		part = slicer.slice(spans[sub].from, spans[sub].to)
		if sub > 0 {
			room, inset = rest, lead+first-rest
		}
	}
	_, _, marked := v.markOf(r.node)
	switch {
	case lit && styles.Reverse():
		body := strings.Repeat(" ", inset) + plainText(part)
		body += strings.Repeat(" ", max(0, inset+room-ansi.StringWidth(body)))
		return styles.Accent("▌") + lipgloss.NewStyle().Reverse(true).Render(" "+body)
	case lit:
		bg := styles.P.Selection
		return cursorMark(bg) + pad(inset, bg) + renderCell(lift(part), column{width: room}, bg)
	}
	gutter := "  "
	if marked && sub == 0 {
		gutter = styles.Fg(styles.ToneWarning).Render("▲") + " "
	}
	return gutter + strings.Repeat(" ", inset) + renderCell(part, column{width: room}, nil)
}

// node is the node of row k, and whether the row is its closing bracket.
func (v jsonView) node(k int) (int32, bool) {
	rows := v.rows()
	if len(rows) == 0 {
		return 0, false
	}
	r := rows[max(0, min(k, len(rows)-1))]
	return r.node, r.close
}

// rowOf is the row of node i, or of its closing bracket, or, when it is folded away, of the
// outermost folded node it is in.
func (v jsonView) rowOf(i int32, closing bool) int {
	for j := v.nodes[i].parent; j >= 0; j = v.nodes[j].parent {
		if v.folded(j) {
			i, closing = j, false
		}
	}
	if closing && v.folded(i) {
		closing = false
	}
	for k, r := range v.rows() {
		if r.node == i && r.close == closing {
			return k
		}
	}
	return 0
}

// path is where the line under the cursor is in the document, as .Policies[0].Arn, . for the
// top, or nothing for what is not json.
func (v jsonView) path() string {
	if v.empty() {
		return ""
	}
	i, _ := v.node(v.at())
	return v.pathOf(i)
}

func (v jsonView) pathOf(i int32) string {
	if v.nodes[i].kind == jsonText {
		return ""
	}
	var steps []string
	for j := i; v.nodes[j].parent >= 0; j = v.nodes[j].parent {
		steps = append(steps, v.step(j))
	}
	slices.Reverse(steps)
	p := strings.Join(steps, "")
	if !strings.HasPrefix(p, ".") {
		p = "." + p
	}
	return p
}

// step is how node j is reached from its parent: .Name, ["a key"] or [3].
func (v jsonView) step(j int32) string {
	n := v.nodes[j]
	if v.nodes[n.parent].kind == jsonArray {
		return "[" + strconv.Itoa(int(n.index)) + "]"
	}
	name := jsonUnquote(n.key)
	if identifier(name) {
		return "." + name
	}
	return "[" + n.key + "]"
}

// identifier reports whether a key can follow a dot in a path.
func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// find is the node a path's steps reach, as step writes them.
func (v jsonView) find(steps []string) (int32, bool) {
	if v.empty() || v.nodes[0].kind == jsonText {
		return 0, false
	}
	at := int32(0)
	for _, s := range steps {
		found := false
		for c := at + 1; c < v.nodes[at].end; c = v.nodes[c].end {
			if v.step(c) == s {
				at, found = c, true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	return at, true
}

// steps are the steps from the top to node i.
func (v jsonView) steps(i int32) []string {
	var out []string
	for j := i; j >= 0 && v.nodes[j].parent >= 0; j = v.nodes[j].parent {
		out = append(out, v.step(j))
	}
	slices.Reverse(out)
	return out
}

// valueOf is node i as it is copied: a string's value, a scalar as written, and an object or
// array as indented json.
func (v jsonView) valueOf(i int32) string {
	n := v.nodes[i]
	switch {
	case i == 0 && v.source != "":
		return v.source
	case n.embedded || n.kind == jsonString:
		return jsonUnquote(n.text)
	case n.kind == jsonPlaceholder:
		return strings.TrimSuffix(strings.TrimPrefix(n.text, "("), ")")
	case n.composite():
		var b strings.Builder
		v.encode(&b, i, "")
		return b.String()
	}
	return n.text
}

// encode writes node i as json indented by two spaces, from indent.
func (v jsonView) encode(b *strings.Builder, i int32, indent string) {
	n := v.nodes[i]
	switch {
	case n.kind == jsonPlaceholder:
		data, _ := json.Marshal(n.text)
		b.Write(data)
		return
	case n.embedded || !n.composite():
		b.WriteString(n.text)
		return
	}
	open, closer := "{", "}"
	if n.kind == jsonArray {
		open, closer = "[", "]"
	}
	if n.count == 0 {
		b.WriteString(open + closer)
		return
	}
	inner := indent + "  "
	b.WriteString(open + "\n")
	for c := i + 1; c < n.end; c = v.nodes[c].end {
		if c > i+1 {
			b.WriteString(",\n")
		}
		b.WriteString(inner)
		if k := v.nodes[c].key; k != "" {
			b.WriteString(k + ": ")
		}
		v.encode(b, c, inner)
	}
	b.WriteString("\n" + indent + closer)
}

// matches are the nodes whose key or value holds the query, ignoring case, in order.
func (v jsonView) matches() []int32 {
	c := v.cache
	if c != nil && c.matchesOK && c.query == v.query {
		return c.matches
	}
	var out []int32
	if want := strings.ToLower(v.query); want != "" {
		for i, n := range v.nodes {
			text := n.text
			if n.composite() {
				text = ""
			}
			if strings.Contains(strings.ToLower(styles.Clean(n.key)), want) || strings.Contains(strings.ToLower(styles.Clean(text)), want) {
				out = append(out, int32(i))
			}
		}
	}
	if c != nil {
		c.query, c.matchesOK, c.matches = v.query, true, out
	}
	return out
}

// matchPosition says which match the cursor is on, as 3 of 12, or how many there are.
func (v jsonView) matchPosition() string {
	if v.query == "" {
		return ""
	}
	found := v.matches()
	if len(found) == 0 {
		return "no match"
	}
	i, _ := v.node(v.at())
	if k, ok := slices.BinarySearch(found, i); ok {
		return ofCount(k, len(found))
	}
	return plural(len(found), "match")
}

// jump moves the cursor to the next match after node from in the direction, or at it too when
// inclusive, around the end, unfolding what it is in. It reports whether there was one.
func (v *jsonView) jump(from int32, forward, inclusive bool) bool {
	found := v.matches()
	if len(found) == 0 {
		return false
	}
	k, ok := slices.BinarySearch(found, from)
	switch {
	case forward && ok && !inclusive:
		k++
	case !forward:
		k--
	}
	k = (k%len(found) + len(found)) % len(found)
	v.reveal(found[k])
	return true
}

// reveal unfolds what node i is in and puts the cursor on it.
func (v *jsonView) reveal(i int32) {
	for j := v.nodes[i].parent; j >= 0; j = v.nodes[j].parent {
		if v.folded(j) {
			v.folds = v.folds.with(j, false)
		}
	}
	v.cursor = v.rowOf(i, false)
}

// toggle folds the object or array under the cursor, the one a value under it is in, or unfolds
// it.
func (v *jsonView) toggle() {
	i, _ := v.node(v.at())
	if n := v.nodes[i]; !n.composite() || n.count == 0 {
		if n.parent < 0 {
			return
		}
		i = n.parent
	}
	v.folds = v.folds.with(i, !v.folded(i))
	v.cursor = v.rowOf(i, false)
}

// unfoldAll unfolds every object and array, as z unfolds everything on every page, or, when they
// are, folds them all but the top.
func (v *jsonView) unfoldAll() {
	i, closing := v.node(v.at())
	all := jsonFoldOpen
	if v.folds != nil && v.folds.all == jsonFoldOpen && len(v.folds.set) == 0 {
		all = jsonFoldShut
	}
	v.folds = &jsonFolds{all: all}
	v.cursor = v.rowOf(i, closing)
}

// carry keeps what was done to old, the same document read again: its folds, the cursor and the
// search, wherever their paths are still found.
func (v jsonView) carry(old jsonView) jsonView {
	if old.empty() || v.empty() {
		return v
	}
	v.search, v.query, v.scroll = old.search, old.query, old.scroll
	if old.folds != nil {
		f := &jsonFolds{all: old.folds.all, set: map[int32]bool{}}
		for i, shut := range old.folds.set {
			if j, ok := v.find(old.steps(i)); ok {
				f.set[j] = shut
			}
		}
		v.folds = f
	}
	if i, closing := old.node(old.at()); old.nodes[i].kind != jsonText {
		if j, ok := v.find(old.steps(i)); ok {
			v.cursor = v.rowOf(j, closing)
		}
	}
	return v
}

// update takes a key: the arrows, pages, g and G move the cursor, enter and space fold, z unfolds
// everything or folds it back, c and C copy the value and the path under the cursor, / searches
// and n and N move between the matches, which esc clears. It reports whether the key was one of
// these, and keeps the cursor in sight of height lines drawn at width.
func (v *jsonView) update(msg tea.KeyPressMsg, k keys.KeyMap, width, height int) (jsonResult, bool) {
	if v.empty() {
		return jsonResult{}, false
	}
	if v.search.active {
		res := v.typeSearch(msg, k)
		v.follow(width, height)
		return res, true
	}
	var res jsonResult
	switch {
	case key.Matches(msg, k.Filter):
		v.from, _ = v.node(v.at())
		return jsonResult{cmd: v.search.open()}, true
	case key.Matches(msg, k.Back) && v.query != "":
		v.query = ""
		v.search.clear()
		return res, true
	case key.Matches(msg, k.NextMatch, k.PrevMatch) && v.query != "":
		i, _ := v.node(v.at())
		if !v.jump(i, key.Matches(msg, k.NextMatch), false) {
			res.status = "no match for " + v.query
		}
	case key.Matches(msg, k.View, k.Fold):
		v.toggle()
	case key.Matches(msg, k.Unfold):
		v.unfoldAll()
	case key.Matches(msg, k.Copy):
		i, _ := v.node(v.at())
		res.copy, res.copied = v.valueOf(i), "the value of "+cmpOr(v.pathOf(i), "the line")
		if v.pathOf(i) == "." {
			res.copied = cmpOr(v.sourceName, "all of it")
		}
	case key.Matches(msg, k.CopyPath):
		i, _ := v.node(v.at())
		if res.copy = v.pathOf(i); res.copy != "" {
			res.copied = "the path " + res.copy
		}
	default:
		c, ok := moveCursor(msg, k, v.at(), len(v.rows()), max(1, height-1))
		if !ok {
			return res, false
		}
		v.cursor = c
	}
	v.follow(width, height)
	return res, true
}

// typeSearch takes a key, or a paste, while the search is typed: the cursor goes to the first
// match from where it was as the query changes, enter keeps the query and esc drops it, the
// cursor back where it was.
func (v *jsonView) typeSearch(msg tea.Msg, k keys.KeyMap) jsonResult {
	press, _ := msg.(tea.KeyPressMsg)
	changed, cmd := v.search.update(msg, k)
	v.query = v.search.text
	res := jsonResult{cmd: cmd}
	switch {
	case key.Matches(press, k.Back):
		v.cursor = v.rowOf(v.from, false)
	case changed && v.query != "":
		if !v.jump(v.from, true, true) {
			v.cursor = v.rowOf(v.from, false)
		}
	}
	if !v.search.active && v.query != "" && len(v.matches()) == 0 {
		res.status = "no match for " + v.query
	}
	return res
}

// follow scrolls the view's own scroll so the cursor is in sight.
func (v *jsonView) follow(width, height int) {
	top, bottom := v.cursorLines(width)
	v.scroll = max(0, min(keepInSight(v.scroll, top, bottom, height), v.lineCount(width)-height))
}

// followJSON is the scroll of a page of total lines, its json v drawn at width from line at, after
// msg moved the cursor from row before: as little as keeps the cursor in sight, but for up on the
// json's first line, which scrolls up to what is above it, and g, which goes to the top of the
// page. A page whose json is not all it shows scrolls with it after each key the json takes. While
// its search is typed, a key is a letter of it and only keeps the match in sight.
func (m model) followJSON(v *jsonView, msg tea.KeyPressMsg, before, scroll, at, total, width int) int {
	most := maxOffset(total, contentHeight)
	switch {
	case v == nil || at < 0:
	case !v.search.active && key.Matches(msg, m.keys.Top):
		scroll = 0
	case !v.search.active && before == 0 && v.at() == 0 && key.Matches(msg, m.keys.Up, m.keys.PageUp):
		scroll = scrollBy(msg, m.keys, scroll, contentHeight, most)
	default:
		top, bottom := v.cursorLines(width)
		scroll = keepInSight(scroll, at+top, at+bottom, contentHeight)
	}
	return max(0, min(scroll, most))
}

// jsonHints are the keys of a json view for the footer: n and N in place of / while a search
// holds text.
func (m model) jsonHints(v jsonView) [][2]string {
	k := m.keys
	h := [][2]string{{upDown, "move"}, {k.View.Help().Key + "/" + k.Fold.Help().Key, "fold"}}
	if v.query != "" {
		h = append(h, [2]string{k.NextMatch.Help().Key + "/" + k.PrevMatch.Help().Key, nextPrevious})
	} else {
		h = append(h, hint(k.Filter, "search"))
	}
	return append(h, hint(k.Copy), hint(k.CopyPath))
}

// jsonDone does what a key on a json view asked of the page: copying, with what was copied in the
// footer, a note, or its search's command.
func (m model) jsonDone(res jsonResult) (model, tea.Cmd) {
	var cmds []tea.Cmd
	switch {
	case res.copy != "":
		var status tea.Cmd
		m, status = m.setStatus("copied "+truncate(styles.Clean(res.copied), 60), styles.ToneSuccess)
		cmds = append(cmds, tea.SetClipboard(res.copy), status)
	case res.status != "":
		var status tea.Cmd
		m, status = m.setStatus(res.status, styles.ToneWarning)
		cmds = append(cmds, status)
	}
	return m, tea.Batch(append(cmds, res.cmd)...)
}

// jsonLineCell tints a line of indented json, as jsonLines, docLines and prettyJSON write it, by
// role as a jsonView colours it: strings in the body ink, numbers and booleans warm, keys, null,
// punctuation and the (json) before json written out of a string faint, and a ref, secret or
// output, which jsonLines writes in parentheses, in info. Every span is cleaned.
func jsonLineCell(line string) cell {
	var c cell
	add := func(s string, t styles.Tone) {
		if s == "" {
			return
		}
		if n := len(c); n > 0 && c[n-1].Tone == t {
			c[n-1].Text += styles.Clean(s)
			return
		}
		c = append(c, styles.Span{Text: styles.Clean(s), Tone: t})
	}
	for i := 0; i < len(line); {
		ch := line[i]
		j := i + 1
		switch {
		case ch == ' ':
			for j < len(line) && line[j] == ' ' {
				j++
			}
			add(line[i:j], jsonPunctTone)
		case ch == '"':
			j = skipQuoted(line, i)
			k := j
			for k < len(line) && line[k] == ' ' {
				k++
			}
			if k < len(line) && line[k] == ':' {
				add(line[i:j], jsonKeyTone)
			} else {
				add(line[i:j], jsonStringTone)
			}
		case strings.IndexByte("{}[],:", ch) >= 0:
			add(line[i:j], jsonPunctTone)
		case strings.HasPrefix(line[i:], embeddedTag):
			j = i + len(embeddedTag)
			add(line[i:j], jsonNoteTone)
		case ch == '(' && strings.LastIndexByte(line, ')') > i:
			j = strings.LastIndexByte(line, ')') + 1
			add(line[i:j], jsonRefTone)
		default:
			for j < len(line) && strings.IndexByte(`"{}[],: `, line[j]) < 0 {
				j++
			}
			add(line[i:j], wordTone(line[i:j]))
		}
		i = j
	}
	return c
}

// wordTone is the tone of a bare word of json: a number or boolean warm, null faint, anything else
// in the body ink.
func wordTone(w string) styles.Tone {
	switch {
	case w == "true" || w == "false":
		return jsonBoolTone
	case w == "null":
		return jsonNullTone
	case w[0] == '-' || w[0] >= '0' && w[0] <= '9':
		return jsonNumberTone
	}
	return styles.ToneBody
}
