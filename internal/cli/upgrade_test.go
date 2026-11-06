package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// TestUpgrade checks upgrade sets the koldstart dependency to the one wanted and resolves it,
// says what changed, leaves one that is wanted already, refuses to go back to an older release
// without --force, writes a PklProject where there is none, and with --check changes nothing and
// fails only when an upgrade is wanted.
func TestUpgrade(t *testing.T) {
	v := func(version string) string { return schema.Release(version).PklProject() }
	local := schema.Dependency{Dir: "/cache/koldstart/schema/0123456789abcdef"}
	resolveFailed := errors.New("resolving PklProject: the packages it depends on did not download")
	tests := []struct {
		name       string
		existing   string
		above      bool
		want       schema.Dependency
		check      bool
		force      bool
		resolveErr error
		wantFile   string
		wantOut    string
		wantErr    string
		wantRuns   int
	}{
		{name: "up", existing: v("0.1.0"), want: schema.Release("0.2.0"), wantFile: v("0.2.0"), wantOut: "koldstart 0.1.0 → 0.2.0\nresolved PklProject into PklProject.deps.json\n", wantRuns: 1},
		{name: "same", existing: v("0.2.0"), want: schema.Release("0.2.0"), wantFile: v("0.2.0"), wantOut: "PklProject has koldstart 0.2.0, this koldstart's: nothing to upgrade\n"},
		{name: "down", existing: v("0.3.0"), want: schema.Release("0.2.0"), wantFile: v("0.3.0"), wantErr: "PklProject has koldstart 0.3.0, newer than this koldstart's 0.2.0: upgrade kdst to 0.3.0, or pass --force to go back to 0.2.0"},
		{name: "down with force", existing: v("0.3.0"), want: schema.Release("0.2.0"), force: true, wantFile: v("0.2.0"), wantOut: "koldstart 0.3.0 → 0.2.0\n", wantRuns: 1},
		{name: "none", want: schema.Release("0.2.0"), wantFile: v("0.2.0"), wantOut: "koldstart none → 0.2.0\n", wantRuns: 1},
		{name: "above", above: true, existing: v("0.1.0"), want: schema.Release("0.2.0"), wantFile: v("0.2.0"), wantOut: "koldstart 0.1.0 → 0.2.0\n", wantRuns: 1},
		{name: "to a dev build", existing: v("0.1.0"), want: local, wantFile: local.PklProject(), wantOut: "koldstart 0.1.0 → dev (/cache/koldstart/schema/0123456789abcdef)\n", wantRuns: 1},
		{name: "not resolved", existing: v("0.1.0"), want: schema.Release("0.2.0"), resolveErr: resolveFailed, wantFile: v("0.2.0"), wantErr: "PklProject is set to koldstart 0.2.0, but not resolved: resolving PklProject: the packages it depends on did not download", wantRuns: 1},
		{name: "check wanted", existing: v("0.1.0"), want: schema.Release("0.2.0"), check: true, wantFile: v("0.1.0"), wantErr: "PklProject wants an upgrade, koldstart 0.1.0 → 0.2.0: run kdst upgrade"},
		{name: "check same", existing: v("0.2.0"), want: schema.Release("0.2.0"), check: true, wantFile: v("0.2.0"), wantOut: "nothing to upgrade"},
		{name: "check newer", existing: v("0.3.0"), want: schema.Release("0.2.0"), check: true, wantFile: v("0.3.0"), wantOut: "PklProject has koldstart 0.3.0, newer than this koldstart's 0.2.0: upgrade kdst to 0.3.0\n"},
		{name: "check none", want: schema.Release("0.2.0"), check: true, wantErr: "there is no PklProject for koldstart.pkl: run kdst upgrade to write one, with koldstart 0.2.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			config := "koldstart.pkl"
			if tt.above {
				config = filepath.Join("services", "orders", "koldstart.pkl")
			}
			if err := os.MkdirAll(filepath.Dir(config), 0o750); err != nil {
				t.Fatal(err)
			}
			if tt.existing != "" {
				if err := os.WriteFile(pkl.ProjectFile, []byte(tt.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runs := 0
			resolve := func(_ context.Context, got string) error {
				runs++
				if want, _ := filepath.Abs("."); got != want {
					t.Errorf("resolved %s, want %s", got, want)
				}
				return tt.resolveErr
			}
			var out bytes.Buffer
			err := upgrade(context.Background(), &out, resolve, upgradeOptions{name: nameShort, config: config, want: tt.want, check: tt.check, force: tt.force})
			switch {
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("upgrade = %v, want an error with %q", err, tt.wantErr)
			case tt.wantErr != "" && ExitCode(err) != exitFailed:
				t.Errorf("exit code %d, want %d", ExitCode(err), exitFailed)
			case tt.wantErr == "" && err != nil:
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("printed %q, want it to hold %q", out.String(), tt.wantOut)
			}
			got, _ := os.ReadFile(pkl.ProjectFile)
			if string(got) != tt.wantFile {
				t.Errorf("PklProject =\n%s\nwant\n%s", got, tt.wantFile)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(config), pkl.ProjectFile)); tt.above && err == nil {
				t.Error("wrote a PklProject beside the configuration, rather than upgrading the one above it")
			}
			if runs != tt.wantRuns {
				t.Errorf("resolved %d times, want %d", runs, tt.wantRuns)
			}
		})
	}
}

// TestUpgradeCommand runs upgrade as a dev build, which moves a project from a release to the
// schema it embeds, resolved for real and offline, a second time finding nothing to do, and the
// project it leaves evaluating.
func TestUpgradeCommand(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil && os.Getenv(pkl.EnvBinary) == "" && os.Getenv("CI") == "" {
		t.Skip("no pkl on the PATH")
	}
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.WriteFile(pkl.ProjectFile, []byte(schema.Release("0.1.0").PklProject()), 0o600); err != nil {
		t.Fatal(err)
	}
	module := "amends \"@koldstart/Project.pkl\"\n\napp = \"shop\"\n"
	if err := os.WriteFile("koldstart.pkl", []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		root := Root(nameShort, nil)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}
	out, err := run("upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "koldstart 0.1.0 → dev (") || !strings.Contains(out, "resolved PklProject into PklProject.deps.json") {
		t.Errorf("printed:\n%s", out)
	}
	if out, err := run("upgrade", "--check"); err != nil || !strings.Contains(out, "nothing to upgrade") {
		t.Errorf("upgrade --check after upgrade = %v, printed:\n%s", err, out)
	}
	binary, err := pkl.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (pkl.Evaluator{Binary: binary}).EvalJSON(context.Background(), "koldstart.pkl", nil); err != nil {
		t.Errorf("the upgraded project does not evaluate: %v", err)
	}
}
