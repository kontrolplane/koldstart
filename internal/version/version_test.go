package version

import (
	"runtime"
	"testing"
)

// TestCurrent checks every field has a value, even in a test binary without stamps or vcs info.
func TestCurrent(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.Date == "" {
		t.Errorf("Current() = %+v, want no empty version, commit or date", info)
	}
	if info.Go != runtime.Version() {
		t.Errorf("Current().Go = %q, want %q", info.Go, runtime.Version())
	}
}

// TestString checks the commit is shortened and a modified tree is marked.
func TestString(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "release",
			info: Info{Version: "v0.1.0", Commit: "0123456789abcdef", Date: "2026-11-04T00:00:00Z", Go: "go1.27.1", Platform: "linux/amd64"},
			want: "v0.1.0 (0123456789ab, 2026-11-04T00:00:00Z, go1.27.1 linux/amd64)",
		},
		{
			name: "modified",
			info: Info{Version: "dev", Commit: "abc", Date: "unknown", Modified: true, Go: "go1.27.1", Platform: "darwin/arm64"},
			want: "dev (abc-dirty, unknown, go1.27.1 darwin/arm64)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
