package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// tint splits a line of the logs into spans so the structure of json and logfmt recedes behind
// the values: keys and punctuation are faint, values keep the body ink, and the message, what a
// line is read for, the text ink. Lambda's own lines, START, END and REPORT, are faint, and a
// line a node function wrote, with its time, request and level in front, keeps them faint and
// its level in a tone. Anything else reads as it is, with a leading timestamp faint.
func tint(line string) cell {
	if platformLine(line) {
		tone := styles.ToneFaint
		if strings.Contains(line, "Status: timeout") || strings.Contains(line, "Status: error") {
			tone = styles.ToneDanger
		}
		return cell{{Text: line, Tone: tone}}
	}
	if strings.Contains(line, "Task timed out after") {
		return cell{{Text: line, Tone: styles.ToneDanger}}
	}
	if c, ok := tintNode(line); ok {
		return c
	}
	if t := strings.TrimLeft(line, " "); strings.HasPrefix(t, "{") {
		return tintJSON(line)
	}
	if c, ok := tintLogfmt(line); ok {
		return c
	}
	if n := leadingTimestamp(line); n > 0 {
		return cell{{Text: line[:n], Tone: styles.ToneFaint}, {Text: line[n:], Tone: styles.ToneBody}}
	}
	return cell{{Text: line, Tone: styles.ToneBody}}
}

// messageKeys are the fields loggers write the message in.
var messageKeys = []string{"msg", "message", "error", "err"}

func isMessageKey(key string) bool {
	for _, k := range messageKeys {
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}

// isLevelKey reports whether a field holds the level a line was logged at.
func isLevelKey(key string) bool {
	switch strings.ToLower(key) {
	case "level", "lvl", "severity":
		return true
	}
	return false
}

// valueLevelTone is the tone of a level written as a value of json or logfmt: error and worse in
// danger, a warning in warning, the rest in the body ink.
func valueLevelTone(level string) styles.Tone {
	switch strings.ToUpper(level) {
	case "ERROR", "ERR", "FATAL", "CRITICAL", "PANIC":
		return styles.ToneDanger
	case "WARN", "WARNING":
		return styles.ToneWarning
	}
	return styles.ToneBody
}

// tintJSON tints a JSON line. It does not check the line is valid, a broken one is tinted as far
// as it reads.
func tintJSON(line string) cell {
	var c cell
	add := func(text string, tone styles.Tone) {
		if text == "" {
			return
		}
		if n := len(c); n > 0 && c[n-1].Tone == tone {
			c[n-1].Text += text
			return
		}
		c = append(c, styles.Span{Text: text, Tone: tone})
	}
	depth := 0
	key := ""
	for i := 0; i < len(line); {
		switch ch := line[i]; {
		case ch == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(line))
			k := j
			for k < len(line) && line[k] == ' ' {
				k++
			}
			switch {
			case k < len(line) && line[k] == ':':
				key = line[i+1 : max(i+1, j-1)]
				add(line[i:j], styles.ToneFaint)
			case depth == 1 && isMessageKey(key):
				add(line[i:j], styles.ToneText)
			case depth == 1 && isLevelKey(key):
				add(line[i:j], valueLevelTone(strings.Trim(line[i:j], `"`)))
			default:
				add(line[i:j], styles.ToneBody)
			}
			i = j
		case strings.IndexByte("{}[],:", ch) >= 0:
			switch ch {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			add(string(ch), styles.ToneFaint)
			i++
		default:
			j := i + 1
			for j < len(line) && strings.IndexByte(`"{}[],:`, line[j]) < 0 {
				j++
			}
			add(line[i:j], styles.ToneBody)
			i = j
		}
	}
	return c
}

// tintLogfmt tints a line of key=value pairs. Like loki.Fields, it takes a line for logfmt when at
// least two words are pairs and they make up most of it.
func tintLogfmt(line string) (cell, bool) {
	var c cell
	words, pairs := 0, 0
	last := 0
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		words++
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' && line[i] != '"' {
			i++
		}
		if i >= len(line) || line[i] != '=' || i == start {
			for i < len(line) && line[i] != ' ' {
				if line[i] == '"' {
					i = skipQuoted(line, i)
					continue
				}
				i++
			}
			continue
		}
		pairs++
		key := line[start:i]
		if start > last {
			c = append(c, styles.Span{Text: line[last:start], Tone: styles.ToneBody})
		}
		c = append(c, styles.Span{Text: line[start : i+1], Tone: styles.ToneFaint})
		i++
		vstart := i
		if i < len(line) && line[i] == '"' {
			i = skipQuoted(line, i)
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
		}
		tone := styles.ToneBody
		switch {
		case isMessageKey(key):
			tone = styles.ToneText
		case isLevelKey(key):
			tone = valueLevelTone(strings.Trim(line[vstart:i], `"`))
		}
		if i > vstart {
			c = append(c, styles.Span{Text: line[vstart:i], Tone: tone})
		}
		last = i
	}
	if pairs < 2 || pairs*3 < words*2 {
		return nil, false
	}
	if last < len(line) {
		c = append(c, styles.Span{Text: line[last:], Tone: styles.ToneBody})
	}
	return c, true
}

// skipQuoted returns where the quoted string starting at i ends.
func skipQuoted(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

// leadingTimestamp is the length of a date and time a line starts with, like
// "2026-10-01 12:00:00.123" or "2026-10-01T12:00:00Z", with the space after it, or 0.
func leadingTimestamp(line string) int {
	const layout = "0000-00-00?00:00:00"
	if len(line) < len(layout) {
		return 0
	}
	for i := range len(layout) {
		c := line[i]
		switch layout[i] {
		case '0':
			if c < '0' || c > '9' {
				return 0
			}
		case '?':
			if c != ' ' && c != 'T' {
				return 0
			}
		default:
			if c != layout[i] {
				return 0
			}
		}
	}
	n := len(layout)
	for n < len(line) && strings.IndexByte("0123456789.,:+-Z", line[n]) >= 0 {
		n++
	}
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// platformLines are the starts of the lines lambda writes itself: those of a request, of the start
// of an instance, and of its extensions.
var platformLines = []string{
	"START RequestId:", "END RequestId:", "REPORT RequestId:",
	"INIT_START ", "INIT_REPORT ", "RESTORE_START ", "RESTORE_REPORT ", "EXTENSION ", "TELEMETRY ",
}

// platformLine reports whether lambda wrote the line. Its tabs are spaces by now, as Clean makes them.
func platformLine(line string) bool {
	for _, p := range platformLines {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// levelTones are the tones of the levels a node function logs at.
var levelTones = map[string]styles.Tone{
	"ERROR": styles.ToneDanger, "WARN": styles.ToneWarning, "INFO": styles.ToneMuted, "DEBUG": styles.ToneFaint, "TRACE": styles.ToneFaint,
}

// tintNode tints a line of node's console in lambda: its time, request id and level, tab
// separated, before the message, which is tinted as json or logfmt when it is either.
func tintNode(line string) (cell, bool) {
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) != 4 || leadingTimestamp(parts[0]+" ") == 0 {
		return nil, false
	}
	tone, ok := levelTones[parts[2]]
	if !ok {
		return nil, false
	}
	return append(cell{
		{Text: parts[0] + " ", Tone: styles.ToneFaint},
		{Text: parts[1] + " ", Tone: styles.ToneFaint},
		{Text: parts[2] + " ", Tone: tone, Bold: tone == styles.ToneDanger},
	}, tint(parts[3])...), true
}

// colours are the sgr sequences a logger colours its lines with. They are dropped; any other
// escape sequence is shown escaped, by Clean.
var colours = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// tintLine tints a raw line of a log for drawing: a node line by its tab separated fields, anything
// else as tint does, its colours dropped and every span cleaned of control characters.
func tintLine(raw string) cell {
	raw = colours.ReplaceAllString(raw, "")
	var c cell
	if parts := strings.SplitN(raw, "\t", 4); len(parts) == 4 {
		if node, ok := tintNode(strings.Join([]string{styles.Clean(parts[0]), styles.Clean(parts[1]), styles.Clean(parts[2]), styles.Clean(parts[3])}, "\t")); ok {
			c = node
		}
	}
	if c == nil {
		c = tint(styles.Clean(raw))
	}
	return c
}

// wrapCell breaks a cell into lines of at most width columns, keeping each span's tone. A line
// breaks at its last space or after its last comma, when that fills at least half of it, and
// otherwise where the width runs out. The space it breaks at, or after the comma, is dropped.
func wrapCell(c cell, width int) []cell {
	text := plainText(c)
	if width <= 0 || ansi.StringWidth(text) <= width {
		return []cell{c}
	}
	var lines []cell
	start, used := 0, 0
	cut, next, cutUsed := -1, -1, 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		w := ansi.StringWidth(string(r))
		if used+w > width && i > start {
			if cut <= start || cutUsed*2 < width {
				cut, next = i, i
				if r == ' ' {
					next = i + size
				}
			}
			lines = append(lines, sliceCell(c, start, cut))
			start, i, used, cut = next, next, 0, -1
			continue
		}
		switch r {
		case ' ':
			cut, next, cutUsed = i, i+size, used
		case ',':
			cut, next, cutUsed = i+size, i+size, used+w
			if next < len(text) && text[next] == ' ' {
				next++
			}
		}
		used += w
		i += size
	}
	if start < len(text) {
		lines = append(lines, sliceCell(c, start, len(text)))
	}
	return lines
}

// plainText is the text of a cell, without its tones.
func plainText(c cell) string {
	var b strings.Builder
	for _, s := range c {
		b.WriteString(s.Text)
	}
	return b.String()
}

// sliceCell is the part of a cell from byte from to byte to of its text.
func sliceCell(c cell, from, to int) cell {
	var out cell
	at := 0
	for _, s := range c {
		end := at + len(s.Text)
		if lo, hi := max(from, at), min(to, end); lo < hi {
			out = append(out, styles.Span{Text: s.Text[lo-at : hi-at], Tone: s.Tone, Bold: s.Bold})
		}
		at = end
	}
	return out
}

// highlight marks where a cell holds want, ignoring case, in bold accent, so the lines a filter
// keeps show why.
func highlight(c cell, want string) cell {
	text := plainText(c)
	lower, want := strings.ToLower(text), strings.ToLower(want)
	if want == "" || len(lower) != len(text) || !strings.Contains(lower, want) {
		return c
	}
	var out cell
	at := 0
	for {
		i := strings.Index(lower[at:], want)
		if i < 0 {
			break
		}
		out = append(out, sliceCell(c, at, at+i)...)
		out = append(out, styles.Span{Text: text[at+i : at+i+len(want)], Tone: styles.ToneAccent, Bold: true})
		at += i + len(want)
	}
	return append(out, sliceCell(c, at, len(text))...)
}

// logCells are the drawn lines of one log message: each of its lines tinted and wrapped to width.
func logCells(message string, width int) []cell { return markedLogCells(message, "", width) }

// markedLogCells are logCells with where the message holds mark marked. The newlines a message
// ends in draw no empty lines.
func markedLogCells(message, mark string, width int) []cell {
	message = strings.TrimRight(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	lines := strings.Split(message, "\n")
	out := make([]cell, 0, len(lines))
	for _, raw := range lines {
		out = append(out, wrapCell(highlight(tintLine(raw), mark), width)...)
	}
	return out
}
