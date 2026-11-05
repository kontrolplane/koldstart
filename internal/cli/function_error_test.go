package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

// TestFunctionMissing checks a function the configuration does not have is refused with the
// configuration's path relative to the working directory, and the functions it does have.
func TestFunctionMissing(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		path      string
		functions []string
		want      string
	}{
		{name: "here", path: filepath.Join(wd, "koldstart.pkl"), functions: []string{"worker", "api"},
			want: `no function "jobs" in koldstart.pkl; it has api and worker`},
		{name: "below", path: filepath.Join(wd, "orders", "koldstart.pkl"), functions: []string{"api"},
			want: `no function "jobs" in ` + filepath.Join("orders", "koldstart.pkl") + "; it has api"},
		{name: "none", path: filepath.Join(wd, "koldstart.pkl"),
			want: `no function "jobs" in koldstart.pkl; it has none`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			functions := map[string]config.Function{}
			for _, name := range tt.functions {
				functions[name] = config.Function{}
			}
			s := &stage{project: &project{Project: &config.Project{Functions: functions}, Path: tt.path}}
			if err := s.function("api"); len(tt.functions) > 0 && err != nil {
				t.Errorf("function(api) = %v, want none", err)
			}
			if err := s.function("jobs"); err == nil || err.Error() != tt.want {
				t.Errorf("function(jobs) = %v, want %q", err, tt.want)
			}
		})
	}
}
