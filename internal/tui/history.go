package tui

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// deployGap is the longest pause between two writes of the state that one deploy makes.
const deployGap = time.Minute

// deploys groups the versions of a state, newest first, into the deploys that wrote them: a
// deploy writes the state before and after every step, so its versions follow each other closely.
func deploys(history []state.ObjectVersion) [][]state.ObjectVersion {
	var out [][]state.ObjectVersion
	var prev time.Time
	for _, v := range history {
		if len(out) == 0 || prev.Sub(v.LastModified) > deployGap {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], v)
		prev = v.LastModified
	}
	return out
}

// deploySummary is what a deploy of the history left: who made it, how many resources it added,
// changed and removed from the deploy before, and whether it stopped with one unfinished. err is
// why its versions could not be read.
type deploySummary struct {
	who                     string
	added, changed, removed int
	failed                  bool
	err                     string
}

// deployPair is a deploy to summarise: the last version it wrote, the last the deploy before it
// wrote, none for the first, and when its first write was.
type deployPair struct {
	key      string
	id       string
	previous string
	started  time.Time
}

// pairOf is the deploy at i of all, newest first, to summarise.
func pairOf(all [][]state.ObjectVersion, i int) deployPair {
	d := all[i]
	p := deployPair{id: d[0].ID, started: d[len(d)-1].LastModified}
	if i+1 < len(all) {
		p.previous = all[i+1][0].ID
	}
	p.key = p.id + " " + p.previous
	return p
}

type summariesLoadedMsg struct {
	gen       int
	summaries map[string]deploySummary
}

// summaryReads is how many versions of the state a summary of the history reads at once.
const summaryReads = 4

// loadSummaries reads what the deploys of the history in sight changed, and those a page below
// them, unless a read of them is in flight already. A deploy summarised once is not read again,
// as a version of the state never changes.
func (m model) loadSummaries() (model, tea.Cmd) {
	s := &m.state.stage
	if !s.historyLoaded || s.summariesGen != s.summariesAnswered {
		return m, nil
	}
	all := deploys(s.history)
	t := s.tables[historyTab]
	var want []deployPair
	for _, i := range s.shownDeploys[:min(len(s.shownDeploys), t.offset+2*t.height)] {
		p := pairOf(all, i)
		if _, ok := s.summaries[p.key]; !ok {
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		return m, nil
	}
	gen := m.next()
	s.summariesGen = gen
	ref := s.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		return summariesLoadedMsg{gen: gen, summaries: summarise(ctx, m.b, ref, want)}
	}
}

// summarise reads the versions the deploys wrote, each once, a few at a time, and says what each
// deploy changed.
func summarise(ctx context.Context, b backend, ref cli.StageRef, pairs []deployPair) map[string]deploySummary {
	type read struct {
		st  *state.State
		err error
	}
	reads := map[string]*read{}
	for _, p := range pairs {
		reads[p.id] = &read{}
		if p.previous != "" {
			reads[p.previous] = &read{}
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, summaryReads)
	for id, r := range reads {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			r.st, r.err = b.Version(ctx, ref, id)
		})
	}
	wg.Wait()
	out := make(map[string]deploySummary, len(pairs))
	for _, p := range pairs {
		cur, before := reads[p.id], &read{}
		if p.previous != "" {
			before = reads[p.previous]
		}
		switch {
		case cur.err != nil:
			out[p.key] = deploySummary{err: cur.err.Error()}
		case before.err != nil:
			out[p.key] = deploySummary{err: before.err.Error()}
		case cur.st == nil:
			out[p.key] = deploySummary{err: "the version holds no state"}
		default:
			out[p.key] = summary(before.st, cur.st, p.started)
		}
	}
	return out
}

// summary is what a deploy changed from the state before it to the one it left, and whether it
// left a resource pending that it started on: it stopped before it finished.
func summary(before, after *state.State, started time.Time) deploySummary {
	sum := deploySummary{who: after.DeployedBy}
	sum.added, sum.changed, sum.removed = countChanges(before, after)
	for _, r := range after.Resources {
		if r.Pending != nil && !r.Pending.StartedAt.Before(started.Add(-deployGap)) {
			sum.failed = true
		}
	}
	return sum
}

// countChanges counts the resources added, changed and removed between two versions of a state,
// as stateChanges lists them.
func countChanges(before, after *state.State) (added, changed, removed int) {
	var was map[string]*state.Resource
	if before != nil {
		was = before.Resources
	}
	for id, a := range after.Resources {
		b, ok := was[id]
		switch {
		case !ok:
			added++
		case !bytes.Equal(b.Inputs, a.Inputs) || b.SecretsHash != a.SecretsHash:
			changed++
		}
	}
	for id := range was {
		if _, ok := after.Resources[id]; !ok {
			removed++
		}
	}
	return added, changed, removed
}

func (m model) applySummaries(msg summariesLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.summariesGen {
		return m, nil
	}
	s.summariesAnswered = msg.gen
	s.summaries = cloned(s.summaries)
	maps.Copy(s.summaries, msg.summaries)
	return m.updateStageTables().loadSummaries()
}

// forgetFailedSummaries drops the summaries that could not be read, so they are read again.
func (s *stageState) forgetFailedSummaries() {
	s.summaries = cloned(s.summaries)
	maps.DeleteFunc(s.summaries, func(_ string, sum deploySummary) bool { return sum.err != "" })
}

// historyRows are the deploys the history tab lists, and the key of each, its first version,
// which stays as the deploy writes more: when, by whom, what it changed, whether it failed, how
// long it took and how many versions it wrote. The filter matches when, who and the version.
func (s *stageState) historyRows() ([]string, []tableRow) {
	s.shownDeploys = nil
	var keys []string
	var rows []tableRow
	all := deploys(s.history)
	var listed []deploySummary
	for i, d := range all {
		if sum, ok := s.summaries[pairOf(all, i).key]; ok && s.filter.matches(formatTime(d[0].LastModified), d[0].ID, sum.who) {
			listed = append(listed, sum)
		}
	}
	widths := countWidths(listed)
	for i, d := range all {
		v, at := d[0], formatTime(d[0].LastModified)
		sum, read := s.summaries[pairOf(all, i).key]
		if !s.filter.matches(at, v.ID, sum.who) {
			continue
		}
		s.shownDeploys = append(s.shownDeploys, i)
		keys = append(keys, d[len(d)-1].ID)
		by := text("…", styles.ToneFaint)
		switch {
		case sum.err != "":
			by = text("-", styles.ToneFaint)
		case read:
			by = text(orDash(sum.who), styles.ToneMuted)
		}
		took := text("-", styles.ToneFaint)
		if len(d) > 1 {
			took = text(cli.Duration(d[0].LastModified.Sub(d[len(d)-1].LastModified)), styles.ToneBody)
		}
		rows = append(rows, tableRow{
			text(formatAgo(v.LastModified), styles.ToneBody),
			text(at, styles.ToneMuted),
			by,
			changesCell(sum, read, widths),
			deployNote(sum, v.Latest),
			took,
			text(formatInt(len(d)), styles.ToneBody),
			text(v.ID, styles.ToneFaint),
		})
	}
	return keys, rows
}

// countWidths is how wide the added, changed and removed counts of the deploys listed are, at
// most, so each lines up under the same count of the other deploys.
func countWidths(summaries []deploySummary) [3]int {
	var w [3]int
	for _, sum := range summaries {
		for i, n := range []int{sum.added, sum.changed, sum.removed} {
			w[i] = max(w[i], len(formatInt(n)))
		}
	}
	return w
}

// changesCell is what a deploy changed in counts, +3 ~1 -0, a count of none faint, each count
// padded to widths so the counts of the deploys line up: an ellipsis while it is read, and a dash
// where it cannot be.
func changesCell(sum deploySummary, read bool, widths [3]int) cell {
	switch {
	case !read:
		return text("…", styles.ToneFaint)
	case sum.err != "":
		return text("-", styles.ToneFaint)
	}
	count := func(mark string, n, width int, tone styles.Tone) styles.Span {
		if n == 0 {
			tone = styles.ToneFaint
		}
		return styles.S(mark+fmt.Sprintf("%-*s", width, formatInt(n)), tone)
	}
	return cell{
		count("+", sum.added, widths[0], styles.ToneSuccess), styles.S(" ", styles.ToneFaint),
		count("~", sum.changed, widths[1], styles.ToneWarning), styles.S(" ", styles.ToneFaint),
		count("-", sum.removed, widths[2], styles.ToneDanger),
	}
}

// deployNote marks the deploy that wrote the current state, one that stopped before it
// finished, and one whose versions could not be read, whose who and changes are dashes; r reads
// it again. Beside current it says not read, so both fit the column.
func deployNote(sum deploySummary, latest bool) cell {
	switch {
	case sum.err != "" && latest:
		return cell{styles.S("current", styles.ToneAccent), sep(), styles.S("not read", styles.ToneFaint)}
	case sum.err != "":
		return cell{styles.S("could not read it", styles.ToneFaint)}
	}
	var note cell
	if sum.failed {
		note = cell{styles.S("✗ failed", styles.ToneDanger)}
	}
	if latest {
		if len(note) > 0 {
			note = append(note, sep())
		}
		note = append(note, styles.S("current", styles.ToneAccent))
	}
	return note
}
