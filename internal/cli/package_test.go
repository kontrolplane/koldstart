package cli

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
)

// TestPrintArchives checks the zips are a table with their sizes lined up on the right, and a
// container image says it is built on deploy.
func TestPrintArchives(t *testing.T) {
	root := filepath.FromSlash("/src/shop")
	zip := func(fn string) string { return filepath.Join(root, ".koldstart", "build", "dev", fn+".zip") }
	tests := []struct {
		name     string
		archives []build.Archive
		want     string
	}{
		{name: "none", want: "no functions to build\n"},
		{
			name: "zips and an image",
			archives: []build.Archive{
				{Function: "api", Path: zip("api"), Size: 264908, SHA256: "d8d6b993285e0123"},
				{Function: "image", SHA256: "aaaaaaaaaaaa0123", Image: true},
				{Function: "worker", Path: zip("worker"), Size: 3984588, SHA256: "f95b8ec1a70a0123"},
				{Function: "x", Path: zip("x"), Size: 12, SHA256: "0123456789ab0123"},
			},
			want: "FUNCTION    SIZE  SHA256        ZIP\n" +
				"api       259 kb  d8d6b993285e  " + filepath.FromSlash(".koldstart/build/dev/api.zip") + "\n" +
				"image          -  aaaaaaaaaaaa  container image, built on deploy\n" +
				"worker    3.8 mb  f95b8ec1a70a  " + filepath.FromSlash(".koldstart/build/dev/worker.zip") + "\n" +
				"x           12 b  0123456789ab  " + filepath.FromSlash(".koldstart/build/dev/x.zip") + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := printArchives(&out, root, tt.archives); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tt.want)
			}
		})
	}
}
