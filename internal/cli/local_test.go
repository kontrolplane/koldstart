package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEventFrom checks the event comes from --data, --path or is {}, and one that is not json is
// refused with where it came from.
func TestEventFrom(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(good, []byte(`{"id": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{id: 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		data     string
		path     string
		want     string
		failWith string
	}{
		{name: "neither", want: "{}"},
		{name: "data", data: `{"job": 1}`, want: `{"job": 1}`},
		{name: "a string", data: `"hello"`, want: `"hello"`},
		{name: "path", path: good, want: `{"id": 1}`},
		{name: "both", data: "{}", path: good, failWith: "not both"},
		{name: "data not json", data: "{bad", failWith: "the event in --data is not json"},
		{name: "file not json", path: bad, failWith: "the event in " + bad + " is not json"},
		{name: "no file", path: filepath.Join(dir, "nope.json"), failWith: "reading the event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eventFrom(tt.data, tt.path)
			if tt.failWith != "" {
				if err == nil || !strings.Contains(err.Error(), tt.failWith) {
					t.Fatalf("eventFrom = %v, want it to say %q", err, tt.failWith)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("event = %s, want %s", got, tt.want)
			}
		})
	}
}
