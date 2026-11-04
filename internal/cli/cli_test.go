package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/version"
)

// run executes the command named name with args, and returns what it printed.
func run(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	root := Root(name)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestName checks the binary answers to kdst through the link and to koldstart otherwise.
func TestName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "koldstart", want: "koldstart"},
		{path: "/opt/homebrew/bin/kdst", want: "kdst"},
		{path: "./kdst", want: "kdst"},
		{path: "/usr/local/bin/koldstart", want: "koldstart"},
		{path: "/tmp/go-build123/exe/koldstart", want: "koldstart"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := Name(tt.path); got != tt.want {
				t.Errorf("Name(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestHelpUsesName checks the help names the binary the way it was run.
func TestHelpUsesName(t *testing.T) {
	for _, name := range []string{nameLong, nameShort} {
		t.Run(name, func(t *testing.T) {
			out, err := run(t, name, "--help")
			if err != nil {
				t.Fatalf("--help: %v", err)
			}
			if !strings.Contains(out, name+" [command]") {
				t.Errorf("--help does not show %q:\n%s", name+" [command]", out)
			}
		})
	}
}

// TestFlagDefaults checks the global flags take their defaults from the environment, and that a
// flag wins over it.
func TestFlagDefaults(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want Options
	}{
		{
			name: "defaults",
			want: Options{Stage: "dev", Config: "koldstart.pkl"},
		},
		{
			name: "environment",
			env: map[string]string{
				"KOLDSTART_STAGE":  "prod",
				"KOLDSTART_CONFIG": "koldstart.prod.pkl",
				"AWS_PROFILE":      "sandbox",
				"AWS_REGION":       "eu-west-1",
			},
			want: Options{Stage: "prod", Config: "koldstart.prod.pkl", Profile: "sandbox", Region: "eu-west-1"},
		},
		{
			name: "flags over environment",
			env:  map[string]string{"KOLDSTART_STAGE": "prod", "AWS_REGION": "eu-west-1"},
			args: []string{"-s", "pr-12", "-c", "other.pkl", "--profile", "ci", "--region", "us-east-1"},
			want: Options{Stage: "pr-12", Config: "other.pkl", Profile: "ci", Region: "us-east-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"KOLDSTART_STAGE", "KOLDSTART_CONFIG", "AWS_PROFILE", "AWS_REGION"} {
				t.Setenv(key, tt.env[key])
			}
			root := Root(nameLong)
			if err := root.PersistentFlags().Parse(tt.args); err != nil {
				t.Fatalf("parsing %v: %v", tt.args, err)
			}
			flags := root.PersistentFlags()
			got := Options{}
			got.Stage, _ = flags.GetString("stage")
			got.Config, _ = flags.GetString("config")
			got.Profile, _ = flags.GetString("profile")
			got.Region, _ = flags.GetString("region")
			if got != tt.want {
				t.Errorf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestVersion checks both forms of the version, and that an unknown -o is refused.
func TestVersion(t *testing.T) {
	want := version.Current()

	t.Run("text", func(t *testing.T) {
		out, err := run(t, nameShort, "version")
		if err != nil {
			t.Fatalf("version: %v", err)
		}
		if out != want.String()+"\n" {
			t.Errorf("version = %q, want %q", out, want.String()+"\n")
		}
	})

	t.Run("flag", func(t *testing.T) {
		out, err := run(t, nameShort, "--version")
		if err != nil {
			t.Fatalf("--version: %v", err)
		}
		if out != nameShort+" "+want.String()+"\n" {
			t.Errorf("--version = %q, want %q", out, nameShort+" "+want.String()+"\n")
		}
	})

	t.Run("json", func(t *testing.T) {
		out, err := run(t, nameLong, "version", "-o", "json")
		if err != nil {
			t.Fatalf("version -o json: %v", err)
		}
		var got version.Info
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("version -o json is not json: %v\n%s", err, out)
		}
		if got != want {
			t.Errorf("version -o json = %+v, want %+v", got, want)
		}
	})

	t.Run("unknown output", func(t *testing.T) {
		if _, err := run(t, nameLong, "version", "-o", "yaml"); err == nil {
			t.Error("version -o yaml: want an error")
		}
	})
}

// TestDebugLog checks --debug creates debug.log in the working directory, and that without it
// nothing is written.
func TestDebugLog(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := run(t, nameLong, "version"); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := os.Stat(debugLog); !os.IsNotExist(err) {
		t.Fatalf("%s exists without --debug: %v", debugLog, err)
	}

	if _, err := run(t, nameLong, "version", "--debug"); err != nil {
		t.Fatalf("version --debug: %v", err)
	}
	info, err := os.Stat(filepath.Join(".", debugLog))
	if err != nil {
		t.Fatalf("--debug did not create %s: %v", debugLog, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s has mode %o, want 600", debugLog, mode)
	}
}
