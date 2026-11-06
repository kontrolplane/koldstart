package schema

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseVersion checks a release's version names its schema package, and a dev build's,
// which has no release, none.
func TestReleaseVersion(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{version: "v0.1.0", want: "0.1.0"},
		{version: "0.2.0", want: "0.2.0"},
		{version: "v0.2.0-rc.1", want: "0.2.0-rc.1"},
		{version: "dev"},
		{version: "dfa9603"},
		{version: "0.1.1-next"},
		{version: "v0.1.0-3-gabc1234-dirty"},
		{version: "v0.0.0-20261106101500-abcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, ok := ReleaseVersion(tt.version)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("ReleaseVersion = %q, %v, want %q", got, ok, tt.want)
			}
		})
	}
}

// TestCurrentDevBuild checks the tests' build, a dev build, depends on the schema it embeds,
// extracted into the cache under its hash.
func TestCurrentDevBuild(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cache, "koldstart", "schema", hash); !got.Local() || got.Dir != want {
		t.Errorf("Current = %+v, want the local dependency in %s", got, want)
	}
}

// TestCompare checks release versions order as semver does, a prerelease before its release.
func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{a: "0.1.0", b: "0.2.0", want: -1},
		{a: "0.10.0", b: "0.9.0", want: 1},
		{a: "1.0.0", b: "1.0.0", want: 0},
		{a: "0.2.0-rc.1", b: "0.2.0", want: -1},
		{a: "0.2.0-rc.2", b: "0.2.0-rc.10", want: -1},
		{a: "0.2.0-beta.1", b: "0.2.0-alpha.3", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+" "+tt.b, func(t *testing.T) {
			if got := Compare(tt.a, tt.b); got != tt.want {
				t.Errorf("Compare = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestPklProject checks the PklProject of a release names its package uri, and of a dev build
// imports the PklProject of the extracted schema.
func TestPklProject(t *testing.T) {
	tests := []struct {
		name string
		dep  Dependency
		want string
	}{
		{name: "release", dep: Release("0.1.0"), want: `amends "pkl:Project"

dependencies {
  ["koldstart"] { uri = "package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0" }
}
`},
		{name: "dev", dep: Dependency{Dir: "/home/me/.cache/koldstart/schema/0123456789abcdef"}, want: `amends "pkl:Project"

dependencies {
  ["koldstart"] = import("/home/me/.cache/koldstart/schema/0123456789abcdef/PklProject")
}
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dep.PklProject(); got != tt.want {
				t.Errorf("PklProject =\n%s\nwant\n%s", got, tt.want)
			}
			got, err := ReadDependency(tt.dep.PklProject())
			if err != nil || got != tt.dep {
				t.Errorf("ReadDependency = %+v, %v, want %+v", got, err, tt.dep)
			}
		})
	}
}

// TestSetDependency checks the koldstart dependency is replaced where it is, added to the
// dependencies there are, or given dependencies of its own, leaving the rest as it was, and one
// it cannot read is refused.
func TestSetDependency(t *testing.T) {
	to := Release("0.2.0")
	tests := []struct {
		name    string
		text    string
		want    string
		wantErr string
	}{
		{
			name: "replaced",
			text: "amends \"pkl:Project\"\n\ndependencies {\n  [\"koldstart\"] { uri = \"package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0\" }\n  [\"toml\"] { uri = \"package://pkg.pkl-lang.org/pkl-pantry/pkl.toml@1.0.0\" }\n}\n",
			want: "amends \"pkl:Project\"\n\ndependencies {\n  " + to.Entry() + "\n  [\"toml\"] { uri = \"package://pkg.pkl-lang.org/pkl-pantry/pkl.toml@1.0.0\" }\n}\n",
		},
		{
			name: "local replaced",
			text: "amends \"pkl:Project\"\n\ndependencies {\n  [\"koldstart\"] = import(\"/cache/schema/abc/PklProject\")\n}\n",
			want: "amends \"pkl:Project\"\n\ndependencies {\n  " + to.Entry() + "\n}\n",
		},
		{
			name: "added",
			text: "amends \"pkl:Project\"\n\ndependencies {\n  [\"toml\"] { uri = \"package://pkg.pkl-lang.org/pkl-pantry/pkl.toml@1.0.0\" }\n}\n",
			want: "amends \"pkl:Project\"\n\ndependencies {\n  " + to.Entry() + "\n  [\"toml\"] { uri = \"package://pkg.pkl-lang.org/pkl-pantry/pkl.toml@1.0.0\" }\n}\n",
		},
		{
			name: "no dependencies",
			text: "amends \"pkl:Project\"",
			want: to.PklProject(),
		},
		{
			name:    "unreadable",
			text:    "amends \"pkl:Project\"\n\ndependencies {\n  [\"koldstart\"] = koldstartPackage\n}\n",
			wantErr: "the koldstart dependency is neither",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SetDependency(tt.text, to)
			switch {
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("SetDependency = %v, want an error with %q", err, tt.wantErr)
			case tt.wantErr == "" && err != nil:
				t.Fatal(err)
			case got != tt.want:
				t.Errorf("SetDependency =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
	if _, err := ReadDependency("amends \"pkl:Project\"\n"); !errors.Is(err, ErrNoDependency) {
		t.Errorf("ReadDependency of none = %v, want ErrNoDependency", err)
	}
}

// TestExtract checks the schema is extracted once into a directory named by its hash, with the
// files a local dependency needs.
func TestExtract(t *testing.T) {
	parent := t.TempDir()
	first, err := Extract(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{Project, Kdst, Package} {
		want, _ := Files.ReadFile(name)
		if got, err := os.ReadFile(filepath.Join(first.Dir, name)); err != nil || string(got) != string(want) {
			t.Errorf("%s in %s: %v", name, first.Dir, err)
		}
	}
	again, err := Extract(parent)
	if err != nil || again != first {
		t.Errorf("a second Extract = %+v, %v, want %+v", again, err, first)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Errorf("%d entries in %s, want the one directory", len(entries), parent)
	}
}

// TestWriteProject checks a PklProject is written when there is none, gets the dependency when
// it has none, and is kept when it has one.
func TestWriteProject(t *testing.T) {
	dep := Release("0.1.0")
	tests := []struct {
		name        string
		existing    string
		wantWritten bool
		want        string
	}{
		{name: "none", wantWritten: true, want: dep.PklProject()},
		{name: "without", existing: "amends \"pkl:Project\"\n", wantWritten: true, want: dep.PklProject()},
		{name: "with", existing: Release("0.0.9").PklProject(), want: Release("0.0.9").PklProject()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.existing != "" {
				if err := os.WriteFile(filepath.Join(dir, Package), []byte(tt.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			written, err := WriteProject(dir, dep)
			if err != nil || written != tt.wantWritten {
				t.Fatalf("WriteProject = %v, %v, want %v", written, err, tt.wantWritten)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, Package)); string(got) != tt.want {
				t.Errorf("PklProject =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestLocal checks Local writes the source into a project whose koldstart dependency is the
// embedded schema beside it, with the full package uris of a file from before PklProject pointed
// at it.
func TestLocal(t *testing.T) {
	source := "amends \"" + PackageURI("0.1.0", Project) + "\"\n\nimport \"" + PackageURI("0.1.0", Kdst) + "\"\n"
	path, cleanup, err := Local([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ := os.ReadFile(path)
	if want := "amends \"@koldstart/Project.pkl\"\n\nimport \"@koldstart/kdst.pkl\"\n"; string(got) != want {
		t.Errorf("the source =\n%s\nwant\n%s", got, want)
	}
	project, _ := os.ReadFile(filepath.Join(filepath.Dir(path), Package))
	if dep, err := ReadDependency(string(project)); err != nil || dep.Dir != "../schema" {
		t.Errorf("the PklProject depends on %+v, %v", dep, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "..", "schema", Project)); err != nil {
		t.Errorf("no schema beside the project: %v", err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cleanup left %s: %v", filepath.Dir(path), err)
	}
}
