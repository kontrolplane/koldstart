// Command schema packages the pkl schema in pkl/ into a directory, as a release attaches it. It
// runs the pkl koldstart pins, downloaded and checked against its checksum when none is installed.
//
//	go run ./hack/schema -version 0.1.0 -out .pkl-out
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/kontrolplane/koldstart/internal/pkl"
)

//nolint:gosec // runs the pinned pkl on the schema
func main() {
	version := flag.String("version", "0.0.0", "the version of the package")
	out := flag.String("out", ".pkl-out", "the directory to package into")
	flag.Parse()

	binary, err := pkl.Find(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	cmd := exec.Command(binary, "project", "package", "--output-path", *out, "pkl/")
	cmd.Env = append(os.Environ(), "KOLDSTART_VERSION="+*version)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error: packaging the schema:", err)
		os.Exit(1)
	}
}
