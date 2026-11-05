package cli

import (
	"bytes"
	"context"
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
	root := Root(name, nil)
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
			root := Root(nameLong, nil)
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

// TestUsageErrors checks a command line that is refused says what was wrong, suggests what may
// have been meant, names the help to read, and exits with 2; a command that fails exits with 1.
func TestUsageErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("KOLDSTART_CONFIG", "")
	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "typo of a command", args: []string{"plon"}, want: `unknown command "plon" for kdst: did you mean plan? (see kdst --help)`, code: exitUsage},
		{name: "unknown command", args: []string{"xyzzy"}, want: `unknown command "xyzzy" for kdst (see kdst --help)`, code: exitUsage},
		{name: "typo of a subcommand", args: []string{"deploy", "functon"}, want: `unknown command "functon" for kdst deploy: did you mean function? (see kdst deploy --help)`, code: exitUsage},
		{name: "typo of a flag", args: []string{"plan", "--stge", "prod"}, want: "unknown flag: --stge: did you mean --stage? (see kdst plan --help)", code: exitUsage},
		{name: "unknown shorthand", args: []string{"plan", "-x"}, want: "unknown shorthand flag: 'x' in -x (see kdst plan --help)", code: exitUsage},
		{name: "no arguments", args: []string{"plan", "prod"}, want: `kdst plan takes no arguments, got "prod" (see kdst plan --help)`, code: exitUsage},
		{name: "wrong arguments", args: []string{"import", "orders"}, want: "kdst import takes <resource> <identifier>, got 1 argument (see kdst import --help)", code: exitUsage},
		{name: "required flag", args: []string{"logs"}, want: "kdst logs needs -f/--function (see kdst logs --help)", code: exitUsage},
		{name: "help of an unknown command", args: []string{"help", "deplo"}, want: `unknown command "deplo" for kdst: did you mean deploy? (see kdst --help)`, code: exitUsage},
		{name: "unknown output", args: []string{"version", "-o", "yaml"}, want: `-o "yaml": want text or json (see kdst version --help)`, code: exitUsage},
		{name: "no configuration", args: []string{"plan"}, want: "no koldstart.pkl here: run kdst init to start a project, or pass --config", code: exitFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := run(t, nameShort, tt.args...)
			if err == nil {
				t.Fatalf("%v: want an error", tt.args)
			}
			if err.Error() != tt.want {
				t.Errorf("%v = %q, want %q", tt.args, err, tt.want)
			}
			if code := ExitCode(err); code != tt.code {
				t.Errorf("ExitCode = %d, want %d", code, tt.code)
			}
			if out != "" {
				t.Errorf("%v printed %q, want nothing: main prints the error once", tt.args, out)
			}
		})
	}
}

// TestHelpGroups checks the root's help lists the commands in their groups, in order, and leaves
// out the line for running the root itself, which only prints the help.
func TestHelpGroups(t *testing.T) {
	out, err := run(t, nameShort)
	if err != nil {
		t.Fatalf("no arguments: %v", err)
	}
	if strings.Contains(out, "kdst [flags]") {
		t.Errorf("the help shows how to run the root:\n%s", out)
	}
	want := []string{"Start a project:", "  init ", "Deploy a stage:", "  package ", "  plan ", "  deploy ", "Work with a stage:", "  invoke ", "  outputs ", "Other commands:", "  version ", "  completion ", "  help "}
	rest := out
	for _, w := range want {
		i := strings.Index(rest, w)
		if i < 0 {
			t.Fatalf("the help does not show %q after what came before:\n%s", w, out)
		}
		rest = rest[i+len(w):]
	}
}

// TestCompletion checks the values of flags and the commands of help complete, and that a
// command without arguments completes no file names.
func TestCompletion(t *testing.T) {
	ui := func(context.Context, *Console, string) error { return nil }
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "commands", args: []string{"pl"}, want: []string{"plan\t"}},
		{name: "theme", args: []string{"ui", "--theme", ""}, want: []string{"auto", "dark", "light", ":4"}},
		{name: "output", args: []string{"version", "-o", ""}, want: []string{"text", "json", ":4"}},
		{name: "help", args: []string{"help", "deploy", ""}, want: []string{"function\t", ":4"}},
		{name: "no arguments", args: []string{"plan", ""}, want: []string{":4"}},
		{name: "configuration", args: []string{"--config", ""}, want: []string{"pkl", ":8"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := Root(nameShort, ui)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(append([]string{"__complete"}, tt.args...))
			if err := root.Execute(); err != nil {
				t.Fatalf("__complete %v: %v", tt.args, err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("__complete %v does not give %q:\n%s", tt.args, w, out.String())
				}
			}
		})
	}
}

// TestDistance checks the edits counted between a typo and what was meant.
func TestDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{a: "stage", b: "stage", want: 0},
		{a: "stge", b: "stage", want: 1},
		{a: "regoin", b: "region", want: 2},
		{a: "", b: "debug", want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.a+"-"+tt.b, func(t *testing.T) {
			if got := distance(tt.a, tt.b); got != tt.want {
				t.Errorf("distance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestOrList checks choices are joined with commas, and the last with or.
func TestOrList(t *testing.T) {
	tests := []struct {
		choices []string
		want    string
	}{
		{choices: []string{"plan"}, want: "plan"},
		{choices: []string{"plan", "deploy"}, want: "plan or deploy"},
		{choices: []string{"--stage", "--stack", "--start"}, want: "--stage, --stack or --start"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := orList(tt.choices); got != tt.want {
				t.Errorf("orList(%q) = %q, want %q", tt.choices, got, tt.want)
			}
		})
	}
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
