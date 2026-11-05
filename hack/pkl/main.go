// Command pkl downloads the pkl koldstart pins, for a platform, checked against its pinned
// checksum. The image build puts it in the image, so koldstart does not download it on first use.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/kontrolplane/koldstart/internal/pkl"
)

func main() {
	goos := flag.String("os", runtime.GOOS, "the os to download pkl for")
	goarch := flag.String("arch", runtime.GOARCH, "the architecture to download pkl for")
	out := flag.String("out", "pkl", "where to write the binary")
	flag.Parse()
	if err := run(*goos, *goarch, *out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(goos, goarch, out string) error {
	dir, err := os.MkdirTemp("", "koldstart-pkl-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, err := pkl.Download(context.Background(), dir, goos, goarch)
	if err != nil {
		return err
	}
	return os.Rename(path, out)
}
