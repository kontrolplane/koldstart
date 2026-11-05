package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/state"
)

// openNewestDeploy opens the newest deploy on the history tab, with its state and the one
// before it as given.
func openNewestDeploy(t *testing.T, before, current *state.State) model {
	t.Helper()
	m := pressKeys(t, started(t, newFake(t)), "shift+tab", "enter")
	if m.page != versionPage {
		t.Fatalf("page %v, want the version", m.page)
	}
	m.state.version.before, m.state.version.current = before, current
	return m
}

// TestVersionAlignsIDs checks the ids in aws of the resources a deploy created start in one
// column, as names from permission/api/httpapi to httpapi left them ragged.
func TestVersionAlignsIDs(t *testing.T) {
	current := testState(ordersDev)
	m := openNewestDeploy(t, nil, current)
	columns := map[int]bool{}
	for line := range strings.SplitSeq(screen(m), "\n") {
		for id, r := range current.Resources {
			if strings.Contains(line, "+ "+id+" ") {
				columns[ansi.StringWidth(line[:strings.Index(line, r.PhysicalID)])] = true
			}
		}
	}
	if len(columns) != 1 {
		t.Errorf("the ids start in columns %v, want one:\n%s", columns, screen(m))
	}
}

// TestVersionChanges checks the version page says what a deploy changed, and says so when it
// changed no resource.
func TestVersionChanges(t *testing.T) {
	tests := []struct {
		name    string
		change  func(before, after *state.State)
		first   bool
		want    []string
		notWant string
	}{
		{
			name:   "a resource added",
			change: func(_, after *state.State) { after.Resources["resource/queue"] = &state.Resource{PhysicalID: "jobs"} },
			want:   []string{"changed from the deploy before · 1 resource", "+ resource/queue  jobs"},
		},
		{
			name:    "nothing",
			change:  func(_, _ *state.State) {},
			want:    []string{"no resource or output changed"},
			notWant: "outputs ·",
		},
		{
			name: "outputs alone",
			change: func(before, after *state.State) {
				before.Outputs["old"] = "gone"
				after.Outputs["httpApiUrl"] = "https://new.example.com"
				after.Outputs["queueUrl"] = "https://sqs/jobs"
			},
			want: []string{"no resource changed", "outputs · 3 changes", "+ queueUrl    https://sqs/jobs", "- old         gone",
				"~ httpApiUrl  https://api.example.com  →  https://new.example.com"},
		},
		{
			name:   "the first deploy",
			change: func(_, _ *state.State) {},
			first:  true,
			want:   []string{"the first deploy · 4 resources", "+ function/api", "+ httpApiUrl  https://api.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := testState(ordersDev), testState(ordersDev)
			tt.change(before, after)
			if tt.first {
				before = nil
			}
			v := screen(openNewestDeploy(t, before, after))
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q in:\n%s", want, v)
				}
			}
			if tt.notWant != "" && strings.Contains(v, tt.notWant) {
				t.Errorf("%q in:\n%s", tt.notWant, v)
			}
		})
	}
}

// TestVersionDiffSaysWhat checks the diff of a change says whether the deploy added, removed or
// changed the resource.
func TestVersionDiffSaysWhat(t *testing.T) {
	tests := []struct {
		name   string
		change func(before, after *state.State)
		want   string
	}{
		{name: "added", change: func(_, after *state.State) { after.Resources["resource/a"] = &state.Resource{} }, want: "added in this deploy"},
		{name: "removed", change: func(before, _ *state.State) { before.Resources["resource/a"] = &state.Resource{} }, want: "removed in this deploy"},
		{
			name:   "changed",
			change: func(_, after *state.State) { after.Resources["function/api"].Inputs = []byte(`{"memory":256}`) },
			want:   "changed in this deploy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := testState(ordersDev), testState(ordersDev)
			tt.change(before, after)
			m := pressKeys(t, openNewestDeploy(t, before, after), "enter")
			if v := screen(m); m.page != diffPage || !strings.Contains(v, tt.want) {
				t.Errorf("page %v, want %q in:\n%s", m.page, tt.want, v)
			}
		})
	}
}

// TestStateChangesWithoutAfter checks a version not read yet has no changes, rather than a
// panic.
func TestStateChangesWithoutAfter(t *testing.T) {
	if got := stateChanges(testState(ordersDev), nil); got != nil {
		t.Errorf("stateChanges = %v, want none", got)
	}
}
