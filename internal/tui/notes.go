package tui

import (
	"time"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// readingNote says what is being read, as every page does: reading the metrics…
func readingNote(what string) string {
	return "reading " + what + "…"
}

// couldNotRead says what could not be read and why, and the key that reads it again, the reason
// in tone: could not read the metrics: AccessDenied · r tries again.
func (m model) couldNotRead(what, err string, tone styles.Tone) cell {
	return cell{styles.S("could not read "+what+": "+err, tone), styles.S(" · "+m.keys.Refresh.Help().Key+" tries again", styles.ToneFaint)}
}

// staleNote says what is on screen is from the read at at, as the last one failed with err: as of
// 10:04:05, could not read again: AccessDenied. what names it where nothing around it does.
func staleNote(what string, at time.Time, err string) cell {
	note := "as of " + at.Local().Format(time.TimeOnly) + ", could not read again: " + err
	if what != "" {
		note = what + " " + note
	}
	return text(note, styles.ToneFaint)
}

// noMatch is what a list says when its filter hides every row: no stage matches orders: esc
// clears the filter.
func noMatch(row, filter string) string {
	return "no " + row + " matches " + filter + ": esc clears the filter"
}

// cellLines wraps a cell to width and draws each of its lines.
func cellLines(c cell, width int) []string {
	parts := wrapCell(c, width)
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, renderCell(part, column{width: width}, nil))
	}
	return lines
}
