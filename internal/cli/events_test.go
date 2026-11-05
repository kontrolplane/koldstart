package cli

import (
	"path/filepath"
	"testing"
)

// TestEventsDir checks the events of a function are kept in the project, and nowhere without a
// project here, so editing one never creates .koldstart in another directory.
func TestEventsDir(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		project *project
		want    string
	}{
		{name: "in the project", project: &project{Root: root}, want: filepath.Join(root, ".koldstart", "events", "api")},
		{name: "no project here", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Console{opts: &Options{}, project: tt.project}
			if got := c.EventsDir("api"); got != tt.want {
				t.Errorf("events dir %q, want %q", got, tt.want)
			}
		})
	}
}
