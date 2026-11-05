package build

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

// write creates the files in dir, keyed by their slash separated path.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// entries returns the names, modes and contents in a zip.
func entries(t *testing.T, data []byte) map[string]*zip.File {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("reading the zip: %v", err)
	}
	out := map[string]*zip.File{}
	for _, f := range r.File {
		out[f.Name] = f
	}
	return out
}

// TestZipDeterministic checks the same files give the same bytes whatever their order, with
// fixed modes and times.
func TestZipDeterministic(t *testing.T) {
	files := []File{
		{Name: "src/api.js", Data: []byte("exports.handler = async () => 1")},
		{Name: "bootstrap", Data: []byte("binary"), Executable: true},
		{Name: "node_modules/a/index.js", Data: []byte("module.exports = 1")},
	}
	first, err := Zip(files)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []File{files[2], files[1], files[0]}
	second, err := Zip(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the same files in another order give another zip")
	}

	got := entries(t, first)
	if mode := got["bootstrap"].Mode().Perm(); mode != 0o755 {
		t.Errorf("bootstrap has mode %o, want 755", mode)
	}
	if mode := got["src/api.js"].Mode().Perm(); mode != 0o644 {
		t.Errorf("src/api.js has mode %o, want 644", mode)
	}
	if !got["src/api.js"].Modified.Equal(epoch) {
		t.Errorf("src/api.js has time %v, want %v", got["src/api.js"].Modified, epoch)
	}

	if _, err := Zip([]File{{Name: "a"}, {Name: "a"}}); err == nil {
		t.Error("a name twice: want an error")
	}
}

// TestSplitHandler checks a handler splits into its module and export.
func TestSplitHandler(t *testing.T) {
	module, export, err := SplitHandler("src/handlers/api.main")
	if err != nil || module != "src/handlers/api" || export != "main" {
		t.Errorf("SplitHandler = %q, %q, %v", module, export, err)
	}
	for _, bad := range []string{"handler", ".handler", "src/api."} {
		if _, _, err := SplitHandler(bad); err == nil {
			t.Errorf("SplitHandler(%q): want an error", bad)
		}
	}
}

// TestNode checks a typescript handler is bundled with its imports into the file its handler
// names, with an external package copied with what it depends on.
func TestNode(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"src/api.ts":                       "import { greet } from './greet'\nimport ext from 'ext'\nexport const handler = async (): Promise<string> => greet(ext)\n",
		"src/greet.ts":                     "export const greet = (name: string): string => `hello ${name}`\n",
		"node_modules/ext/package.json":    `{"name": "ext", "main": "index.js", "dependencies": {"dep": "1.0.0"}}`,
		"node_modules/ext/index.js":        "module.exports = require('dep')\n",
		"node_modules/dep/package.json":    `{"name": "dep", "main": "index.js"}`,
		"node_modules/dep/index.js":        "module.exports = 'world'\n",
		"node_modules/unused/package.json": `{"name": "unused"}`,
	})
	fn := config.Function{
		Runtime: config.RuntimeNode22,
		Handler: "src/api.handler",
		Bundle:  config.Bundle{External: []string{"ext"}, Sourcemap: true},
	}
	b := Builder{Root: root, Stage: "dev"}
	arc, err := b.Build(context.Background(), "api", fn)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(arc.Path)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, data)
	for _, name := range []string{"src/api.js", "src/api.js.map", "node_modules/ext/index.js", "node_modules/dep/index.js"} {
		if got[name] == nil {
			t.Errorf("%s is not in the zip", name)
		}
	}
	if got["node_modules/unused/package.json"] != nil {
		t.Error("a package nothing uses is in the zip")
	}
	rc, _ := got["src/api.js"].Open()
	var bundle bytes.Buffer
	_, _ = bundle.ReadFrom(rc)
	_ = rc.Close()
	if !strings.Contains(bundle.String(), "hello ${name}") || !strings.Contains(bundle.String(), `require("ext")`) {
		t.Errorf("the bundle lacks the import or keeps the external:\n%s", bundle.String())
	}
	if arc.Path != filepath.Join(root, Dir, "dev", "api.zip") || len(arc.SHA256) != 64 {
		t.Errorf("Archive = %+v", arc)
	}

	again, err := b.Build(context.Background(), "api", fn)
	if err != nil {
		t.Fatal(err)
	}
	if again.SHA256 != arc.SHA256 {
		t.Error("building twice gives another hash")
	}
}

// TestNodeErrors checks a missing file, a syntax error and a missing external are reported with
// what to fix.
func TestNodeErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"src/broken.ts": "export const handler = (\n"})
	b := Builder{Root: root, Stage: "dev"}
	tests := []struct {
		name string
		fn   config.Function
		want string
	}{
		{name: "missing", fn: config.Function{Runtime: config.RuntimeNode22, Handler: "src/missing.handler"}, want: "no src/missing with any of"},
		{name: "syntax", fn: config.Function{Runtime: config.RuntimeNode22, Handler: "src/broken.handler"}, want: "src/broken.ts:2:"},
		{name: "external", fn: config.Function{Runtime: config.RuntimeNode24, Handler: "src/broken.handler", Bundle: config.Bundle{External: []string{"sharp"}}}, want: "src/broken.ts:2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := b.Build(context.Background(), tt.name, tt.fn)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Build = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestGoReproducible checks the same go code built in two directories gives the same zip, as on
// a laptop and in ci, and that the binary is for lambda: an elf binary for aarch64, with the
// magic and machine 0xb7 at offset 18.
func TestGoReproducible(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on the PATH")
	}
	source := map[string]string{
		"go.mod":             "module example.com/orders\n\ngo 1.26\n",
		"cmd/worker/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"worker\") }\n",
	}
	fn := config.Function{Runtime: config.RuntimeGo, Handler: "./cmd/worker", Architecture: config.ArchARM64}

	hashes := make([]string, 0, 2)
	for range 2 {
		root := t.TempDir()
		write(t, root, source)
		arc, err := Builder{Root: root, Stage: "dev"}.Build(context.Background(), "worker", fn)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		hashes = append(hashes, arc.SHA256)

		data, _ := os.ReadFile(arc.Path)
		got := entries(t, data)
		if len(got) != 1 || got["bootstrap"] == nil {
			t.Fatalf("zip holds %v, want bootstrap alone", got)
		}
		if got["bootstrap"].Mode().Perm() != 0o755 {
			t.Errorf("bootstrap has mode %o, want 755", got["bootstrap"].Mode().Perm())
		}
		rc, _ := got["bootstrap"].Open()
		head := make([]byte, 20)
		_, _ = rc.Read(head)
		_ = rc.Close()
		if !bytes.HasPrefix(head, []byte("\x7fELF")) || head[18] != 0xb7 {
			t.Errorf("bootstrap is not an arm64 linux binary: % x", head)
		}
	}
	if hashes[0] != hashes[1] {
		t.Errorf("the same code in two directories gives %s and %s", hashes[0], hashes[1])
	}
}
