package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// sdHistoryTab opens the history tab of the fixture of a stage at w by h.
func sdHistoryTab(t *testing.T, f *fakeBackend, w, h int) model {
	t.Helper()
	m := newModel(t.Context(), f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = settle(t, m, m.Init())
	return pressKeys(t, m, "shift+tab")
}

// sdRowText is the text of a row of the tab on screen, its cells joined by a space.
func sdRowText(m model, i int) string {
	s := m.state.stage
	var parts []string
	for _, c := range s.tables[s.tab].rows[i] {
		if p := plainText(c); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

// TestHistoryRows checks each deploy says when it was, by whom, what it changed from the one
// before, its counts padded to those of the first deploy, which added everything, whether it
// stopped before it finished, and how long it took.
func TestHistoryRows(t *testing.T) {
	m := sdHistoryTab(t, sdStage(t, 3), 120, 40)
	tests := []struct {
		name string
		row  int
		want []string
	}{
		{name: "the current deploy, unfinished", row: 0, want: []string{"3h ago", "levi", "+3   ~1 -1", "✗ failed · current", " - 1 d00-0"}},
		{name: "one of two writes", row: 1, want: []string{"8h ago", "ana", "+1   ~1 -0", "20s 2 d01-1"}},
		{name: "a failed deploy", row: 2, want: []string{"13h ago", "ci@github-actions", "+3   ~1 -0", "✗ failed", "40s 3"}},
		{name: "nothing added", row: 3, want: []string{"levi", "+0   ~1 -0", "1m 4"}},
		{name: "the first deploy", row: 49, want: []string{"+", "~0 -0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.row >= 9 {
				m = pressKeys(t, m, "G")
			}
			got := sdRowText(m, tt.row)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("row %d %q has no %q", tt.row, got, want)
				}
			}
		})
	}
}

// TestHistoryReadsInSight checks the history reads what the deploys in sight changed, and a
// page below them, rather than every version, and reads the rest as they come in sight, each
// deploy once.
func TestHistoryReadsInSight(t *testing.T) {
	f := sdStage(t, 3)
	m := sdHistoryTab(t, f, 80, 24)
	height := m.state.stage.tables[historyTab].height
	if got, want := f.versionReads, 2*height+1; got != want {
		t.Errorf("%d versions read for %d rows in sight, want %d", got, height, want)
	}
	if got := sdRowText(m, 2*height); !strings.Contains(got, "…") {
		t.Errorf("a deploy out of sight is summarised: %q", got)
	}
	m = pressKeys(t, m, "G")
	if got := len(m.state.stage.summaries); got != 50 {
		t.Errorf("%d deploys summarised once the last is in sight, want 50", got)
	}
	reads := f.versionReads
	m = pressKeys(t, m, "g", "G", "r")
	if f.versionReads != reads {
		t.Errorf("moving and reading the history again read %d versions more, want none", f.versionReads-reads)
	}
	if got := sdRowText(m, 49); strings.Contains(got, "…") {
		t.Errorf("the first deploy is not summarised: %q", got)
	}
}

// TestHistoryUnreadable checks a deploy whose versions cannot be read shows a dash where its
// author and changes go, and r reads them again.
func TestHistoryUnreadable(t *testing.T) {
	f := sdStage(t, 3)
	f.versionErr = errAWS
	m := sdHistoryTab(t, f, 120, 40)
	if got := sdRowText(m, 0); !strings.HasPrefix(got, "3h ago") || strings.Contains(got, "levi") || !strings.Contains(got, " - - ") {
		t.Errorf("a deploy that cannot be read: %q", got)
	}
	f.versionErr = nil
	m = pressKeys(t, m, "r")
	if got := sdRowText(m, 0); !strings.Contains(got, "levi") || !strings.Contains(got, "+3   ~1 -1") {
		t.Errorf("after r: %q", got)
	}
}

// TestHistoryFilterByAuthor checks the filter finds the deploys of whoever made them, once
// what they changed is read.
func TestHistoryFilterByAuthor(t *testing.T) {
	m := pressKeys(t, sdHistoryTab(t, sdStage(t, 3), 200, 60), "G", "g", "/")
	m = pressKeys(t, typeText(t, m, "github"), "enter")
	if n := len(m.state.stage.shownDeploys); n != 16 {
		t.Errorf("%d deploys by github, want 16", n)
	}
}

// TestDeploySummary checks a deploy counts what it added, changed and removed, and failed only
// when it left pending a resource it started on, not one an earlier deploy left.
func TestDeploySummary(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	tests := []struct {
		name    string
		pending time.Time
		failed  bool
	}{
		{name: "finished"},
		{name: "stopped part way", pending: started.Add(30 * time.Second), failed: true},
		{name: "stopped at its first step", pending: started.Add(-time.Second), failed: true},
		{name: "a step an earlier deploy left", pending: started.Add(-time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := testState(ordersDev), testState(ordersDev)
			delete(after.Resources, "role/api")
			after.Resources["resource/queue"] = &state.Resource{PhysicalID: "jobs"}
			after.Resources["resource/table"].Inputs = []byte(`{"properties":{"BillingMode":"PROVISIONED"}}`)
			after.Resources["loggroup/api"].SecretsHash = "new"
			if !tt.pending.IsZero() {
				after.Resources["resource/table"].Pending = &state.Pending{Operation: "update", StartedAt: tt.pending}
			}
			got := summary(before, after, started)
			if got.added != 1 || got.changed != 2 || got.removed != 1 || got.who != "levi" || got.failed != tt.failed {
				t.Errorf("summary %+v, want +1 ~2 -1 by levi, failed %v", got, tt.failed)
			}
			if n := len(stateChanges(before, after)); n != got.added+got.changed+got.removed {
				t.Errorf("the version lists %d changes, the summary counts %d", n, got.added+got.changed+got.removed)
			}
		})
	}
}

// TestChangesAlign checks the counts of the deploys line up, each padded to the widest of its
// kind, so a deploy of 19 additions does not push the changes and removals of the others.
func TestChangesAlign(t *testing.T) {
	sums := map[string]deploySummary{"a": {added: 1, changed: 1}, "b": {added: 19, removed: 120}}
	widths := countWidths([]deploySummary{sums["a"], sums["b"]})
	if widths != [3]int{2, 1, 3} {
		t.Fatalf("widths %v, want [2 1 3]", widths)
	}
	tests := []struct {
		sum  deploySummary
		want string
	}{
		{sum: sums["a"], want: "+1  ~1 -0  "},
		{sum: sums["b"], want: "+19 ~0 -120"},
	}
	for _, tt := range tests {
		if got := plainText(changesCell(tt.sum, true, widths)); got != tt.want {
			t.Errorf("changes %q, want %q", got, tt.want)
		}
	}
}

// TestChangesCell checks the counts of a deploy's changes are in the tones of what they did,
// and a count of none is faint.
func TestChangesCell(t *testing.T) {
	c := changesCell(deploySummary{added: 3, changed: 0, removed: 2}, true, [3]int{1, 1, 1})
	tests := []struct {
		part string
		want styles.Tone
	}{
		{part: "+3", want: styles.ToneSuccess},
		{part: "~0", want: styles.ToneFaint},
		{part: "-2", want: styles.ToneDanger},
	}
	for _, tt := range tests {
		t.Run(tt.part, func(t *testing.T) {
			if tone, ok := toneOf(c, tt.part); !ok || tone != tt.want {
				t.Errorf("%q in tone %d (found %v), want %d", tt.part, tone, ok, tt.want)
			}
		})
	}
	if got := plainText(changesCell(deploySummary{}, false, [3]int{1, 1, 1})); got != "…" {
		t.Errorf("unread changes %q, want …", got)
	}
}

// TestDeployNote checks the note of a deploy: current, failed, both, and that its versions
// could not be read, which beside current is short enough that both fit the column.
func TestDeployNote(t *testing.T) {
	tests := []struct {
		name   string
		sum    deploySummary
		latest bool
		want   string
	}{
		{name: "nothing to say", want: ""},
		{name: "current", latest: true, want: "current"},
		{name: "failed", sum: deploySummary{failed: true}, want: "✗ failed"},
		{name: "failed and current", sum: deploySummary{failed: true}, latest: true, want: "✗ failed · current"},
		{name: "not read", sum: deploySummary{err: "aws said no"}, want: "could not read it"},
		{name: "not read and current", sum: deploySummary{err: "aws said no"}, latest: true, want: "current · not read"},
	}
	width := historyColumns[4].width
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note := deployNote(tt.sum, tt.latest)
			if got := plainText(note); got != tt.want {
				t.Errorf("deployNote = %q, want %q", got, tt.want)
			}
			if w := cellWidth(note); w > width {
				t.Errorf("%q is %d wide, wider than its column of %d", plainText(note), w, width)
			}
		})
	}
}
