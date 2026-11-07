package build

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
)

// Darwin is the os of a mac, which a function runs on here.
const Darwin = "darwin"

// String is the platform as go names it, linux/arm64.
func (t Target) String() string {
	return t.GOOS + "/" + t.GOARCH
}

// provided builds a function on provided.al2023 into its bootstrap: the executable in
// fn.Bootstrap as it is, a rust crate with cargo, or a go package.
func (b Builder) provided(ctx context.Context, name string, fn config.Function, target Target) (File, error) {
	switch {
	case fn.Bootstrap != "":
		return b.bootstrap(fn, target)
	case fn.IsRust(b.Root):
		return b.rust(ctx, name, fn, target)
	}
	return b.golang(ctx, fn, target)
}

// bootstrap reads fn.Bootstrap, an executable built elsewhere, as the bootstrap of the zip. It
// must be there, executable, and built for target, or a script that starts with #!.
//
//nolint:gosec // reads the function's own bootstrap
func (b Builder) bootstrap(fn config.Function, target Target) (File, error) {
	path := fn.Bootstrap
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.Root, filepath.FromSlash(path))
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return File{}, fmt.Errorf("bootstrap %s is not there: build it first, as koldstart zips it as it is", fn.Bootstrap)
	case err != nil:
		return File{}, fmt.Errorf("reading bootstrap %s: %w", fn.Bootstrap, err)
	case info.IsDir():
		return File{}, fmt.Errorf("bootstrap %s is a directory: set bootstrap to the executable in it", fn.Bootstrap)
	case info.Mode().Perm()&0o111 == 0:
		return File{}, fmt.Errorf("bootstrap %s is not executable: run chmod +x %s", fn.Bootstrap, fn.Bootstrap)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("reading bootstrap %s: %w", fn.Bootstrap, err)
	}
	if err := runs("bootstrap "+fn.Bootstrap, data, fn.Architecture, target); err != nil {
		return File{}, err
	}
	return File{Name: bootstrapFile, Data: data, Executable: true}, nil
}

// runs says what to do when the executable what, data, does not run on target: a binary for
// another os or architecture, or neither a binary nor a script. A script that starts with #!
// runs anywhere. A target that is not the one lambda runs a function of the architecture on is
// this machine, to run the function here.
func runs(what string, data []byte, architecture string, target Target) error {
	if bytes.HasPrefix(data, []byte("#!")) {
		return nil
	}
	got, ok := platform(data, target)
	lambda := LambdaTarget(architecture)
	switch {
	case !ok:
		return fmt.Errorf("%s is neither a binary nor a script that starts with #!: build it for %s", what, target)
	case got == target:
		return nil
	case target != lambda:
		return fmt.Errorf("%s is built for %s, which does not run on this machine, %s: build it for %s to run it here, or invoke the function deployed", what, got, target, target)
	case got.GOOS == Linux && got == LambdaTarget(otherArchitecture(architecture)):
		return fmt.Errorf("%s is built for %s, and the function's architecture is %s: build it for %s, or set architecture = %q", what, got, architecture, target, otherArchitecture(architecture))
	}
	return fmt.Errorf("%s is built for %s, and lambda runs it on %s: build it for %s", what, got, target, target)
}

// otherArchitecture is the architecture of lambda that is not architecture.
func otherArchitecture(architecture string) string {
	if architecture == config.ArchX8664 {
		return config.ArchARM64
	}
	return config.ArchX8664
}

// platform is the os and architecture a binary is built for, from its header: linux for elf,
// darwin for mach-o, and for a universal mach-o binary want's architecture when it holds it,
// otherwise its first. It is false for anything else.
func platform(data []byte, want Target) (Target, bool) {
	r := bytes.NewReader(data)
	if f, err := elf.NewFile(r); err == nil {
		return Target{GOOS: Linux, GOARCH: elfArch(f.Machine)}, true
	}
	if f, err := macho.NewFile(r); err == nil {
		return Target{GOOS: Darwin, GOARCH: machoArch(f.Cpu)}, true
	}
	if f, err := macho.NewFatFile(r); err == nil && len(f.Arches) > 0 {
		got := Target{GOOS: Darwin, GOARCH: machoArch(f.Arches[0].Cpu)}
		for _, a := range f.Arches {
			if machoArch(a.Cpu) == want.GOARCH {
				got.GOARCH = want.GOARCH
			}
		}
		return got, true
	}
	return Target{}, false
}

// elfArch is the architecture of an elf machine as go names it.
func elfArch(m elf.Machine) string {
	switch m {
	case elf.EM_AARCH64:
		return goARM64
	case elf.EM_X86_64:
		return goAMD64
	}
	return strings.ToLower(strings.TrimPrefix(m.String(), "EM_"))
}

// machoArch is the architecture of a mach-o cpu as go names it.
func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuArm64:
		return goARM64
	case macho.CpuAmd64:
		return goAMD64
	}
	return strings.ToLower(strings.TrimPrefix(c.String(), "Cpu"))
}
