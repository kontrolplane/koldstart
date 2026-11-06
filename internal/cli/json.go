package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// jsonIndent is the indentation of the json the cli prints, for scripts and for people.
const jsonIndent = "  "

// writeJSON writes v for a script, as -o json prints it: indented, its keys sorted at every depth,
// nothing escaped for html, and a newline after.
func writeJSON(w io.Writer, v any) error {
	return encodeJSON(w, v, jsonIndent)
}

// writeJSONLine writes v as writeJSON does, on one line, for output of a json object a line.
func writeJSONLine(w io.Writer, v any) error {
	return encodeJSON(w, v, "")
}

// encodeJSON writes v with its keys sorted, those of structs too: it is encoded, read back as
// maps, whose keys encoding/json sorts, and encoded again. Numbers are kept as they were written.
func encodeJSON(w io.Writer, v any, indent string) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("writing json: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var sorted any
	if err := dec.Decode(&sorted); err != nil {
		return fmt.Errorf("writing json: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(sorted); err != nil {
		return fmt.Errorf("writing json: %w", err)
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// The colours of json for a person: keys, strings and numbers.
const (
	sgrJSONKey    = sgrCyan
	sgrJSONString = sgrGreen
	sgrJSONNumber = sgrYellow
)

// prettyJSON writes data, which must be json, for a person: indented, its keys, strings and
// numbers coloured with colour, and a newline after. Its keys keep their order and its numbers
// their digits, so it reads as what was returned. A control character in a string, one a
// terminal would act on, is written as its \u escape, so the json stays the same json. It
// returns false, and writes nothing, for data that is not json.
func prettyJSON(w io.Writer, data []byte, colour palette) bool {
	var indented bytes.Buffer
	if err := json.Indent(&indented, bytes.TrimSpace(data), "", jsonIndent); err != nil {
		return false
	}
	src := indented.Bytes()
	var out bytes.Buffer
	for i := 0; i < len(src); {
		switch ch := src[i]; {
		case ch == '"':
			end := stringEnd(src, i)
			code := sgrJSONString
			if next := skipSpace(src, end); next < len(src) && src[next] == ':' {
				code = sgrJSONKey
			}
			out.WriteString(colour.paint(code, escapeControls(src[i:end])))
			i = end
		case ch == '-' || ch >= '0' && ch <= '9':
			end := i + 1
			for end < len(src) && bytes.IndexByte([]byte("+-.eE0123456789"), src[end]) >= 0 {
				end++
			}
			out.WriteString(colour.paint(sgrJSONNumber, string(src[i:end])))
			i = end
		default:
			out.WriteByte(ch)
			i++
		}
	}
	out.WriteByte('\n')
	_, _ = w.Write(out.Bytes())
	return true
}

// stringEnd is the index after the closing quote of the json string that starts at i.
func stringEnd(src []byte, i int) int {
	j := i + 1
	for j < len(src) && src[j] != '"' {
		if src[j] == '\\' {
			j++
		}
		j++
	}
	return min(j+1, len(src))
}

// skipSpace is the index of the first byte from i that is not white space.
func skipSpace(src []byte, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\n' || src[i] == '\t' || src[i] == '\r') {
		i++
	}
	return i
}

// escapeControls writes a json string with DEL and the c1 controls, which json allows as they
// are, as \u escapes.
func escapeControls(s []byte) string {
	var b bytes.Buffer
	for len(s) > 0 {
		r, n := utf8.DecodeRune(s)
		if r == 0x7f || r >= 0x80 && r <= 0x9f {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.Write(s[:n])
		}
		s = s[n:]
	}
	return b.String()
}
