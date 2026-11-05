package build

import (
	"archive/zip"
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"time"
)

// File is one entry of a function's zip.
type File struct {
	// Name is slash separated, relative to the root of the zip.
	Name       string
	Data       []byte
	Executable bool
}

// epoch is the time every entry carries, the earliest a zip can hold, so the same files always
// give the same bytes.
var epoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// Zip packs files into a zip that is the same, byte for byte, for the same files: entries sorted
// by name, one fixed time, and modes set here rather than taken from the disk, 0755 for
// executables and 0644 for the rest.
func Zip(files []File) ([]byte, error) {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b File) int { return cmp.Compare(a.Name, b.Name) })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Name == sorted[i-1].Name {
			return nil, fmt.Errorf("%s is in the zip twice", sorted[i].Name)
		}
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range sorted {
		header := &zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: epoch}
		header.SetMode(0o644)
		if f.Executable {
			header.SetMode(0o755)
		}
		entry, err := w.CreateHeader(header)
		if err != nil {
			return nil, fmt.Errorf("adding %s to the zip: %w", f.Name, err)
		}
		if _, err := entry.Write(f.Data); err != nil {
			return nil, fmt.Errorf("adding %s to the zip: %w", f.Name, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
