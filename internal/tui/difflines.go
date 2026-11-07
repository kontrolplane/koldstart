package tui

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// diffLine is a line of a diff: kept, removed or added, with where it is in its document and,
// when it pairs with a line on the other side as the same field changed, the parts that changed.
type diffLine struct {
	op   byte
	text string
	// path is where the line is in its document, as ."Statement"[0]."Effect".
	path string
	// marks are the byte ranges of text that changed from the line it pairs with.
	marks [][2]int
	// paired is set on a removed and an added line that are one field changed.
	paired bool
	// counted is set on a line added or removed that is a field of its own, rather than a part
	// of one added or removed whole, or a bracket that closes one.
	counted bool
}

// maxDiffCells bounds the table diffOps fills; past it the lines are shown removed and added
// whole.
const maxDiffCells = 4_000_000

// diffDocs is the diff of two documents as the diff page shows it: their lines, json in a string
// written out, each field that changed paired with what it was and its changed part marked.
func diffDocs(before, after any) []diffLine {
	a, b := docLines(before), docLines(after)
	lines := lineDiff(a, b)
	annotate(lines, a, b)
	return lines
}

// docLines are the lines of a document as a diff compares them: jsonLines, with json held in a
// string, as an iam policy often is, written out as json of its own after (json).
func docLines(v any) []string {
	return expandEmbedded(jsonLines(v))
}

// embeddedTag is what stands before json written out of a string, so it does not read as an
// object.
const embeddedTag = "(json) "

// expandEmbedded writes out each string of lines that holds a json object or array as the json it
// holds, at the string's indent, so a change to one statement of a policy is one line of the diff
// rather than the whole policy.
func expandEmbedded(lines []string) []string {
	var out []string
	for i, line := range lines {
		_, from, to := splitLine(line)
		inner := embeddedLines(line[from:to])
		if inner == nil {
			if out != nil {
				out = append(out, line)
			}
			continue
		}
		if out == nil {
			out = append(make([]string, 0, len(lines)+len(inner)), lines[:i]...)
		}
		lead := strings.Repeat(" ", indentOf(line))
		comma := line[to:]
		if len(inner) == 1 {
			out = append(out, line[:from]+embeddedTag+inner[0]+comma)
			continue
		}
		out = append(out, line[:from]+embeddedTag+inner[0])
		for _, l := range inner[1 : len(inner)-1] {
			out = append(out, lead+l)
		}
		out = append(out, lead+inner[len(inner)-1]+comma)
	}
	if out == nil {
		return lines
	}
	return out
}

// embeddedLines are the lines of the json a quoted value holds, when it is an object or an array,
// or nil.
func embeddedLines(value string) []string {
	if len(value) < 4 || value[0] != '"' || value[len(value)-1] != '"' || !strings.ContainsRune(`{[ \`, rune(value[1])) {
		return nil
	}
	var s string
	if json.Unmarshal([]byte(value), &s) != nil {
		return nil
	}
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil || dec.More() {
		return nil
	}
	return expandEmbedded(jsonLines(doc))
}

// indentOf is how many spaces a line starts with.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// splitLine finds the parts of a line of indented json: where its key ends, 0 when it has none,
// and the bytes of its value, without the comma that ends the line.
func splitLine(line string) (keyEnd, from, to int) {
	lead := indentOf(line)
	from, to = lead, len(line)
	if strings.HasSuffix(line, ",") {
		to--
	}
	if lead < len(line) && line[lead] == '"' {
		if j := skipQuoted(line, lead); strings.HasPrefix(line[j:], ": ") {
			keyEnd, from = j, j+2
		}
	}
	return keyEnd, from, max(to, from)
}

// diffKey is what lineDiff compares a line by: the line without the comma that ends it, so an
// entry that gains one, as the entry before one added after it does, is not a change.
func diffKey(line string) string { return strings.TrimSuffix(line, ",") }

// lineDiff is the shortest edit from a to b, line by line, as the longest common subsequence
// gives it. A line kept is written as b has it. Each run of lines only added or only removed is
// slid to where it begins and ends shallowest, so an object added to an array reads as the
// object, rather than the end of the one before it and the start of the next.
func lineDiff(a, b []string) []diffLine {
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i, l := range a {
		ka[i] = diffKey(l)
	}
	for i, l := range b {
		kb[i] = diffKey(l)
	}
	ops := diffOps(ka, kb)
	slideRuns(ops, '+', b, kb)
	slideRuns(ops, '-', a, ka)
	out := make([]diffLine, 0, len(ops))
	i, j := 0, 0
	for _, op := range ops {
		switch op {
		case ' ':
			out = append(out, diffLine{op: ' ', text: b[j]})
			i++
			j++
		case '-':
			out = append(out, diffLine{op: '-', text: a[i]})
			i++
		default:
			out = append(out, diffLine{op: '+', text: b[j]})
			j++
		}
	}
	return out
}

// diffOps is the shortest edit from a to b: a byte for each element, ' ' kept, '-' removed and
// '+' added. What a and b start and end with alike is kept before the table is filled, so a long
// document with one change costs little.
func diffOps[T comparable](a, b []T) []byte {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]byte, 0, len(a)+len(b))
	for range pre {
		ops = append(ops, ' ')
	}
	ops = append(ops, lcsOps(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for range suf {
		ops = append(ops, ' ')
	}
	return ops
}

// lcsOps is diffOps by the table of the longest common subsequence, or a removed whole and b
// added whole when the table would pass maxDiffCells.
func lcsOps[T comparable](a, b []T) []byte {
	ops := make([]byte, 0, len(a)+len(b))
	if len(a)*len(b) > maxDiffCells {
		for range a {
			ops = append(ops, '-')
		}
		for range b {
			ops = append(ops, '+')
		}
		return ops
	}
	cols := len(b) + 1
	lcs := make([]int32, (len(a)+1)*cols)
	for i := range slices.Backward(a) {
		for j := range slices.Backward(b) {
			if a[i] == b[j] {
				lcs[i*cols+j] = lcs[(i+1)*cols+j+1] + 1
			} else {
				lcs[i*cols+j] = max(lcs[(i+1)*cols+j], lcs[i*cols+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, ' ')
			i++
			j++
		case lcs[(i+1)*cols+j] >= lcs[i*cols+j+1]:
			ops = append(ops, '-')
			i++
		default:
			ops = append(ops, '+')
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, '-')
	}
	for ; j < len(b); j++ {
		ops = append(ops, '+')
	}
	return ops
}

// slideRuns moves each run of ops op, '+' or '-', up or down past kept lines equal to its own
// ends, to where runScore puts it, the lowest when two score alike. lines and keys are the side
// the run's lines are on.
func slideRuns(ops []byte, op byte, lines, keys []string) {
	at := 0
	for p := 0; p < len(ops); {
		switch ops[p] {
		case ' ':
			at++
			p++
			continue
		case op:
		default:
			p++
			continue
		}
		e := p
		for e < len(ops) && ops[e] == op {
			e++
		}
		n := e - p
		s, base := p, at
		for s > 0 && ops[s-1] == ' ' && keys[base-1] == keys[base-1+n] {
			s--
			base--
		}
		best, bestScore := s, runScore(lines[base:base+n])
		end := e
		for t, tb := s, base; t+n < len(ops) && (t+n < e || ops[t+n] == ' ') && keys[tb] == keys[tb+n]; {
			t++
			tb++
			end = max(end, t+n)
			if sc := runScore(lines[tb : tb+n]); sc <= bestScore {
				best, bestScore = t, sc
			}
		}
		for q := s; q < end; q++ {
			ops[q] = ' '
		}
		for q := best; q < best+n; q++ {
			ops[q] = op
		}
		at = base + (end - s)
		p = end
	}
}

// runScore is how badly a run of lines added or removed reads: how deep it starts and ends, and
// far worse, when it starts by closing something or ends by opening something.
func runScore(lines []string) int {
	first, last := lines[0], lines[len(lines)-1]
	score := indentOf(first) + indentOf(last)
	if t := strings.TrimLeft(first, " "); strings.HasPrefix(t, "}") || strings.HasPrefix(t, "]") {
		score += 1000
	}
	if t := diffKey(last); strings.HasSuffix(t, "{") || strings.HasSuffix(t, "[") {
		score += 1000
	}
	return score
}

// lineShape is where a line of indented json is in its document: its path, the line that opens
// what it is in, -1 at the top, and whether it opens or closes an object or array.
type lineShape struct {
	path          string
	parent        int
	opens, closes bool
}

// shapes finds the shape of each line of an indented json document.
func shapes(lines []string) []lineShape {
	type frame struct {
		path  string
		line  int
		array bool
		next  int
	}
	var stack []frame
	out := make([]lineShape, len(lines))
	for i, line := range lines {
		keyEnd, from, to := splitLine(line)
		value := line[from:to]
		s := lineShape{parent: -1}
		if len(stack) > 0 {
			s.parent = stack[len(stack)-1].line
		}
		switch {
		case strings.HasPrefix(value, "}") || strings.HasPrefix(value, "]"):
			s.closes = true
			if len(stack) > 0 {
				s.path = stack[len(stack)-1].path
				stack = stack[:len(stack)-1]
				s.parent = -1
				if len(stack) > 0 {
					s.parent = stack[len(stack)-1].line
				}
			}
		case len(stack) == 0:
		case stack[len(stack)-1].array:
			top := &stack[len(stack)-1]
			s.path = top.path + "[" + strconv.Itoa(top.next) + "]"
			top.next++
		case keyEnd > 0:
			s.path = stack[len(stack)-1].path + "." + line[indentOf(line):keyEnd]
		}
		if !s.closes && (strings.HasSuffix(value, "{") || strings.HasSuffix(value, "[")) {
			s.opens = true
			stack = append(stack, frame{path: s.path, line: i, array: strings.HasSuffix(value, "[")})
		}
		out[i] = s
	}
	return out
}

// annotate gives each line of a diff of a and b its path, pairs each field removed with the same
// field added in the same change and marks what changed in them, and counts the fields added and
// removed.
func annotate(lines []diffLine, a, b []string) {
	sa, sb := shapes(a), shapes(b)
	opA, opB := make([]byte, len(a)), make([]byte, len(b))
	side := make([]int, len(lines))
	i, j := 0, 0
	for n, l := range lines {
		switch l.op {
		case '-':
			opA[i], side[n] = '-', i
			lines[n].path = sa[i].path
			i++
		default:
			opB[j], side[n] = l.op, j
			lines[n].path = sb[j].path
			if l.op == ' ' {
				opA[i] = ' '
				i++
			}
			j++
		}
	}
	shape := func(n int) (lineShape, []lineShape, []byte) {
		if lines[n].op == '-' {
			return sa[side[n]], sa, opA
		}
		return sb[side[n]], sb, opB
	}
	for n, l := range lines {
		if l.op == ' ' {
			continue
		}
		s, all, ops := shape(n)
		switch {
		case s.closes:
		case s.parent < 0:
			lines[n].counted = !s.opens
		default:
			lines[n].counted = all[s.parent].parent < 0 || ops[s.parent] != l.op
		}
	}
	for start := 0; start < len(lines); {
		if lines[start].op == ' ' {
			start++
			continue
		}
		end := start
		for end < len(lines) && lines[end].op != ' ' {
			end++
		}
		pairChange(lines, start, end, shape)
		start = end
	}
}

// pairChange pairs the fields removed in lines start to end, one change, with the fields added at
// the same paths, marks what changed in each pair, and puts each added field of a pair right
// after the removed one, as a review reads them.
func pairChange(lines []diffLine, start, end int, shape func(int) (lineShape, []lineShape, []byte)) {
	leaf := func(n int) bool {
		s, _, _ := shape(n)
		return !s.opens && !s.closes
	}
	partner := map[int]int{}
	last := -1
	monotonic := true
	for r := start; r < end; r++ {
		if lines[r].op != '-' || !leaf(r) {
			continue
		}
		for a := start; a < end; a++ {
			if lines[a].op != '+' || lines[a].paired || lines[a].path != lines[r].path || !leaf(a) {
				continue
			}
			_, fromR, toR := splitLine(lines[r].text)
			_, fromA, toA := splitLine(lines[a].text)
			mr, ma := markChanged(lines[r].text[fromR:toR], lines[a].text[fromA:toA])
			lines[r].marks, lines[a].marks = shift(mr, fromR), shift(ma, fromA)
			lines[r].paired, lines[a].paired = true, true
			lines[r].counted, lines[a].counted = false, false
			partner[r] = a
			monotonic = monotonic && a > last
			last = a
			break
		}
	}
	if len(partner) == 0 || !monotonic {
		return
	}
	var added []int
	for n := start; n < end; n++ {
		if lines[n].op == '+' {
			added = append(added, n)
		}
	}
	out := make([]diffLine, 0, end-start)
	next := 0
	for n := start; n < end; n++ {
		if lines[n].op != '-' {
			continue
		}
		a, ok := partner[n]
		if !ok {
			out = append(out, lines[n])
			continue
		}
		for ; added[next] != a; next++ {
			out = append(out, lines[added[next]])
		}
		out = append(out, lines[n], lines[a])
		next++
	}
	for _, a := range added[next:] {
		out = append(out, lines[a])
	}
	copy(lines[start:end], out)
}

// shift moves byte ranges by n.
func shift(ranges [][2]int, n int) [][2]int {
	for i := range ranges {
		ranges[i][0] += n
		ranges[i][1] += n
	}
	return ranges
}

// maxMarkCells bounds the table markChanged fills; past it the values are marked whole.
const maxMarkCells = 250_000

// markChanged is what changed from one value to another: the byte ranges of each, by word, that
// the other does not have, so 512 to 1024 marks the numbers, and an arn the part of it that
// differs. Marks with a single space or punctuation mark between them are joined.
func markChanged(a, b string) (ma, mb [][2]int) {
	ta, tb := wordsOf(a), wordsOf(b)
	wa, wb := make([]string, len(ta)), make([]string, len(tb))
	for i, t := range ta {
		wa[i] = a[t[0]:t[1]]
	}
	for i, t := range tb {
		wb[i] = b[t[0]:t[1]]
	}
	var ops []byte
	if len(wa)*len(wb) > maxMarkCells {
		ops = make([]byte, 0, len(wa)+len(wb))
		for range wa {
			ops = append(ops, '-')
		}
		for range wb {
			ops = append(ops, '+')
		}
	} else {
		ops = diffOps(wa, wb)
	}
	marked, added := make([]bool, len(ta)), make([]bool, len(tb))
	i, j := 0, 0
	for _, op := range ops {
		switch op {
		case ' ':
			i++
			j++
		case '-':
			marked[i] = true
			i++
		default:
			added[j] = true
			j++
		}
	}
	return joinMarks(a, ta, marked), joinMarks(b, tb, added)
}

// wordsOf cuts s into words, runs of letters and digits, and every other rune on its own, as
// byte ranges.
func wordsOf(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		j := i + size
		if wordRune(r) {
			for j < len(s) {
				r, n := utf8.DecodeRuneInString(s[j:])
				if !wordRune(r) {
					break
				}
				j += n
			}
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

// wordRune reports whether r is part of a word: a letter or a digit.
func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// joinMarks turns the words of s marked into byte ranges, a run of them one range, and two runs
// with one word between that is not a letter or digit one range.
func joinMarks(s string, words [][2]int, marked []bool) [][2]int {
	var out [][2]int
	for i, w := range words {
		if !marked[i] {
			continue
		}
		if n := len(out); n > 0 && out[n-1][1] == w[0] {
			out[n-1][1] = w[1]
			continue
		}
		if n := len(out); n > 0 && i > 0 && out[n-1][1] == words[i-1][0] {
			if r, _ := utf8.DecodeRuneInString(s[words[i-1][0]:]); !wordRune(r) {
				out[n-1][1] = w[1]
				continue
			}
		}
		out = append(out, w)
	}
	return out
}
