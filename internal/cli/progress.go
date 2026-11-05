package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// progressNote is how often a node that takes long says it is still going, where no status line
// can be redrawn, as in ci logs.
const progressNote = 30 * time.Second

// The intervals the status line is redrawn at, and long nodes are checked at without one.
const (
	redrawEvery = 100 * time.Millisecond
	noteEvery   = time.Second
)

// spinner are the frames of the status line's spinner.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// participles say what each action is doing, for a node that takes long.
var participles = map[engine.Action]string{
	engine.Create:  "creating",
	engine.Update:  "updating",
	engine.Replace: "replacing",
	engine.Delete:  "deleting",
	engine.Keep:    "leaving in place",
	engine.Adopt:   "taking over",
}

// running is a node being applied.
type running struct {
	id     string
	action engine.Action
	since  time.Time
	// notes is how many times it said it is still going.
	notes int
}

// progress writes a line as each node of a plan finishes. On a terminal, a status line under
// them says what is running and for how long, redrawn in place; elsewhere, as in ci logs, the
// lines are plain and a node that takes long says so every progressNote.
type progress struct {
	w      io.Writer
	colour palette
	// live says the status line is drawn; width returns the terminal's width for it.
	live  bool
	width func() int
	now   func() time.Time
	// idWidth is the column the ids are padded to, and total the number of changes.
	idWidth int
	total   int

	mu      sync.Mutex
	start   time.Time
	frame   int
	running []*running
	done    map[engine.Action]int
	failed  int
	stopped int
	drawn   bool
	// buf holds what is written until it is flushed, in one write.
	buf  strings.Builder
	quit chan struct{}
	wg   sync.WaitGroup
}

// newProgress returns the progress of applying plan, written to w.
func newProgress(w io.Writer, plan *engine.Plan) *progress {
	p := &progress{w: w, colour: paletteFor(w), now: time.Now, width: func() int { return 80 }, done: map[engine.Action]int{}}
	if f, ok := w.(*os.File); ok && isTerminal(f) && os.Getenv("TERM") != "dumb" {
		p.live = true
		p.width = func() int {
			width, _, err := term.GetSize(f.Fd())
			if err != nil || width <= 0 {
				return 80
			}
			return width
		}
	}
	for _, c := range plan.Changes {
		if c.Action != engine.NoOp {
			p.total++
			p.idWidth = max(p.idWidth, min(len(c.ID), maxIDWidth))
		}
	}
	return p
}

// begin starts the clock, and the redraws of the status line or the checks for long nodes.
func (p *progress) begin() {
	p.mu.Lock()
	p.start = p.now()
	p.quit = make(chan struct{})
	p.mu.Unlock()
	every := noteEvery
	if p.live {
		every = redrawEvery
	}
	p.wg.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-p.quit:
				return
			case <-t.C:
				p.tick()
			}
		}
	})
}

// end stops the redraws and clears the status line, and returns how long it took.
func (p *progress) end() time.Duration {
	if p.quit != nil {
		close(p.quit)
		p.wg.Wait()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	p.flush()
	return p.now().Sub(p.start)
}

// event is the engine's OnEvent.
func (p *progress) event(ev engine.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.flush()
	if !ev.Done {
		p.running = append(p.running, &running{id: ev.ID, action: ev.Action, since: p.now()})
		p.draw()
		return
	}
	for i, r := range p.running {
		if r.id == ev.ID {
			p.running = append(p.running[:i], p.running[i+1:]...)
			break
		}
	}
	p.clear()
	id := padRight(ev.ID, p.idWidth)
	took := elapsed(ev.Duration)
	switch {
	case ev.Err != nil && errors.Is(ev.Err, context.Canceled):
		p.stopped++
		_, _ = fmt.Fprintf(&p.buf, "  %s %s  %s %s\n", p.colour.paint(sgrYellow, "✗"), id, verbs[ev.Action], p.colour.paint(sgrFaint, "stopped after "+took))
	case ev.Err != nil:
		p.failed++
		_, _ = fmt.Fprintf(&p.buf, "  %s %s  %s %s\n", p.colour.paint(sgrRed, "✗"), id, verbs[ev.Action], p.colour.paint(sgrRed, "failed after "+took))
	default:
		p.done[ev.Action]++
		_, _ = fmt.Fprintf(&p.buf, "  %s %s  %s %s\n", p.colour.paint(sgrGreen, "✓"), id, pastTenses[ev.Action], p.colour.paint(sgrFaint, "in "+took))
	}
	p.draw()
}

// tick redraws the status line, or says which nodes are taking long.
func (p *progress) tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.flush()
	if p.live {
		p.frame++
		p.draw()
		return
	}
	now := p.now()
	for _, r := range p.running {
		if n := int(now.Sub(r.since) / progressNote); n > r.notes {
			r.notes = n
			_, _ = fmt.Fprintf(&p.buf, "  %s %s  %s\n", p.colour.paint(sgrFaint, "…"), padRight(r.id, p.idWidth),
				p.colour.paint(sgrFaint, "still "+participles[r.action]+" after "+elapsed(now.Sub(r.since))))
		}
	}
}

// flush writes what was drawn since the last flush in one write, so the terminal never shows
// the status line cleared and not yet drawn again.
func (p *progress) flush() {
	if p.buf.Len() == 0 {
		return
	}
	_, _ = io.WriteString(p.w, p.buf.String())
	p.buf.Reset()
}

// draw writes the status line in place of the one before, cut to the terminal's width so it
// never wraps.
func (p *progress) draw() {
	if !p.live || len(p.running) == 0 {
		p.clear()
		return
	}
	finished := p.failed + p.stopped
	for _, n := range p.done {
		finished += n
	}
	ids := make([]string, 0, len(p.running))
	for _, r := range p.running {
		ids = append(ids, r.id)
	}
	line := fmt.Sprintf("  %s %d of %d  %s  %s", p.colour.paint(sgrCyan, spinner[p.frame%len(spinner)]), finished, p.total,
		strings.Join(ids, ", "), p.colour.paint(sgrFaint, elapsed(p.now().Sub(p.start))))
	p.buf.WriteString("\r" + ansi.Truncate(line, p.width()-1, "…") + "\x1b[K")
	p.drawn = true
}

// clear removes the status line, so a line can be written in its place.
func (p *progress) clear() {
	if !p.drawn {
		return
	}
	p.buf.WriteString("\r\x1b[K")
	p.drawn = false
}

// summary counts what was done, as 8 created, 1 updated, with what failed or was stopped.
func (p *progress) summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var parts []string
	for _, a := range actionOrder {
		if p.done[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.done[a], pastTenses[a]))
		}
	}
	if p.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", p.failed))
	}
	if p.stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped", p.stopped))
	}
	if len(parts) == 0 {
		return "nothing changed"
	}
	return strings.Join(parts, ", ")
}

// elapsed writes a duration as precisely as is useful: 12ms, 4.2s, 3m5s.
func elapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// padRight pads s with spaces to width runes.
func padRight(s string, width int) string {
	if n := len([]rune(s)); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
