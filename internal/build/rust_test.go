package build

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

// fakeCargo is a cargo that logs where it ran and with what to $FAKE_LOG, and fails with
// $FAKE_FAIL on stderr when it is set. cargo lambda build copies $FAKE_BINARY to
// <lambda dir>/<name>/bootstrap for each name in $FAKE_BINS; cargo build prints a json message
// of each, as cargo does.
const fakeCargo = `#!/bin/sh
echo "$(pwd -P) $*" >> "$FAKE_LOG"
if [ -n "$FAKE_FAIL" ]; then echo "$FAKE_FAIL" >&2; exit 101; fi
if [ "$1" = lambda ]; then
  while [ $# -gt 0 ]; do
    if [ "$1" = --lambda-dir ]; then out=$2; fi
    shift
  done
  for b in $FAKE_BINS; do mkdir -p "$out/$b"; cp "$FAKE_BINARY" "$out/$b/bootstrap"; done
  exit 0
fi
echo '{"reason":"compiler-artifact","target":{"name":"build-script-build","kind":["custom-build"]},"executable":null}'
for b in $FAKE_BINS; do
  printf '{"reason":"compiler-artifact","target":{"name":"%s","kind":["bin"]},"executable":"%s"}\n' "$b" "$FAKE_BINARY"
done
echo '{"reason":"build-finished","success":true}'
`

// fakeRust puts a fake cargo, and cargo-lambda with lambda, first on the PATH, and returns the
// file cargo logs to.
func fakeRust(t *testing.T, lambda bool) string {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{"cargo": fakeCargo}
	if lambda {
		scripts["cargo-lambda"] = "#!/bin/sh\nexit 0\n"
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	logFile := filepath.Join(t.TempDir(), "cargo.log")
	t.Setenv("FAKE_LOG", logFile)
	t.Setenv("FAKE_FAIL", "")
	return logFile
}

// elfBinary is the header of a linux binary for machine, which is all platform reads.
func elfBinary(t *testing.T, machine elf.Machine) []byte {
	t.Helper()
	h := elf.Header64{Type: uint16(elf.ET_EXEC), Machine: uint16(machine), Version: uint32(elf.EV_CURRENT), Ehsize: 64, Phentsize: 56, Shentsize: 64}
	copy(h.Ident[:], elf.ELFMAG)
	h.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	h.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	h.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, h); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// machoBinary is the header of a macos binary for cpu.
func machoBinary(t *testing.T, cpu macho.Cpu) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, macho.FileHeader{Magic: macho.Magic64, Cpu: cpu, Type: macho.TypeExec}); err != nil {
		t.Fatal(err)
	}
	b.Write(make([]byte, 4))
	return b.Bytes()
}

// TestRust checks a rust crate is built with cargo lambda in its directory, with the flag of the
// function's architecture, for lambda, and with cargo for this machine on another os; that the
// bootstrap is the crate's one binary or the one named after the function; and that what goes
// wrong says what to do.
func TestRust(t *testing.T) {
	arm := elfBinary(t, elf.EM_AARCH64)
	tests := []struct {
		name         string
		lambda       bool
		architecture string
		target       *Target
		binary       []byte
		bins         string
		fail         string
		wantArgs     string
		err          string
	}{
		{name: "arm64", lambda: true, architecture: config.ArchARM64, binary: arm, bins: "api", wantArgs: "lambda build --release --arm64 --lambda-dir "},
		{name: "x86_64", lambda: true, architecture: config.ArchX8664, binary: elfBinary(t, elf.EM_X86_64), bins: "api", wantArgs: "lambda build --release --x86-64 --lambda-dir "},
		{name: "one binary of another name", lambda: true, architecture: config.ArchARM64, binary: arm, bins: "orders-api"},
		{name: "the binary named after the function", lambda: true, architecture: config.ArchARM64, binary: arm, bins: "seed api migrate"},
		{name: "no binary named after the function", lambda: true, architecture: config.ArchARM64, binary: arm, bins: "seed migrate", err: "the crate in ./cmd/api builds migrate and seed: name the function's binary api, or give each function a crate of its own"},
		{name: "no binary", lambda: true, architecture: config.ArchARM64, binary: arm, err: "the crate in ./cmd/api builds no binary: give it a src/main.rs"},
		{name: "no cargo-lambda", architecture: config.ArchARM64, binary: arm, bins: "api", err: "rust functions need cargo-lambda on the PATH to build for lambda: brew install cargo-lambda/tap/cargo-lambda"},
		{name: "cargo fails", lambda: true, architecture: config.ArchARM64, binary: arm, bins: "api", fail: "error[E0425]: cannot find value `x`", err: "building the crate in ./cmd/api: cargo lambda build:\nerror[E0425]: cannot find value `x`"},
		{name: "built for another architecture", lambda: true, architecture: config.ArchARM64, binary: elfBinary(t, elf.EM_X86_64), bins: "api", err: "the binary api cargo built is built for linux/amd64, and the function's architecture is arm64"},
		{name: "this machine", target: &Target{GOOS: Darwin, GOARCH: "arm64"}, architecture: config.ArchARM64, binary: machoBinary(t, macho.CpuArm64), bins: "api", wantArgs: "build --release --message-format=json-render-diagnostics"},
		{name: "this machine, failing", target: &Target{GOOS: Darwin, GOARCH: "arm64"}, architecture: config.ArchARM64, binary: arm, bins: "api", fail: "error: could not compile `api`", err: "cargo build:\nerror: could not compile `api`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeRust(t, tt.lambda)
			root := t.TempDir()
			write(t, root, map[string]string{"cmd/api/Cargo.toml": "[package]\nname = \"api\"\n", "cmd/api/src/main.rs": "fn main() {}\n"})
			binaryFile := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(binaryFile, tt.binary, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_BINARY", binaryFile)
			t.Setenv("FAKE_BINS", tt.bins)
			t.Setenv("FAKE_FAIL", tt.fail)
			fn := config.Function{Runtime: config.RuntimeProvided, Handler: "./cmd/api", Architecture: tt.architecture}
			target := LambdaTarget(tt.architecture)
			if tt.target != nil {
				target = *tt.target
			}
			files, err := Builder{Root: root, Stage: "dev"}.Files(context.Background(), "api", fn, target)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("Files = %v, want an error with %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Files: %v", err)
			}
			if len(files) != 1 || files[0].Name != "bootstrap" || !files[0].Executable || !bytes.Equal(files[0].Data, tt.binary) {
				t.Errorf("files %+v, want the binary as an executable bootstrap", files)
			}
			logged, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatal(err)
			}
			crate, err := filepath.EvalSymlinks(filepath.Join(root, "cmd", "api"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(logged), crate+" "+tt.wantArgs) {
				t.Errorf("cargo ran as %q, want in %s with %q", logged, crate, tt.wantArgs)
			}
		})
	}
}

// TestRustZip checks a rust function's zip holds its binary alone as an executable bootstrap,
// and is the same when built again.
func TestRustZip(t *testing.T) {
	fakeRust(t, true)
	root := t.TempDir()
	write(t, root, map[string]string{"cmd/api/Cargo.toml": "[package]\nname = \"api\"\n"})
	binaryFile := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(binaryFile, elfBinary(t, elf.EM_AARCH64), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_BINARY", binaryFile)
	t.Setenv("FAKE_BINS", "api")
	fn := config.Function{Runtime: config.RuntimeProvided, Handler: "./cmd/api", Architecture: config.ArchARM64}
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
	if len(got) != 1 || got["bootstrap"] == nil || got["bootstrap"].Mode().Perm() != 0o755 {
		t.Errorf("zip holds %v, want an executable bootstrap alone", got)
	}
	again, err := b.Build(context.Background(), "api", fn)
	if err != nil || again.SHA256 != arc.SHA256 {
		t.Errorf("built again: %s, %v; want %s", again.SHA256, err, arc.SHA256)
	}
}

// TestRustCargoLambda builds a crate with the cargo lambda on the PATH, for arm64: an elf binary
// for aarch64.
func TestRustCargoLambda(t *testing.T) {
	if _, err := exec.LookPath("cargo-lambda"); err != nil {
		t.Skip("no cargo-lambda on the PATH")
	}
	root := t.TempDir()
	write(t, root, map[string]string{
		"cmd/api/Cargo.toml":  "[package]\nname = \"api\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"cmd/api/src/main.rs": "fn main() {}\n",
	})
	fn := config.Function{Runtime: config.RuntimeProvided, Handler: "./cmd/api", Architecture: config.ArchARM64}
	files, err := Builder{Root: root, Stage: "dev"}.Files(context.Background(), "api", fn, LambdaTarget(fn.Architecture))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, ok := platform(files[0].Data, Target{}); !ok || got != LambdaTarget(config.ArchARM64) {
		t.Errorf("the bootstrap is built for %v, want linux/arm64", got)
	}
}

// TestBootstrap checks an executable built elsewhere goes in the zip as bootstrap when it is
// there, executable and built for where it runs, or is a script; and that otherwise the error
// says what to do.
func TestBootstrap(t *testing.T) {
	arm := elfBinary(t, elf.EM_AARCH64)
	here := &Target{GOOS: Darwin, GOARCH: "arm64"}
	tests := []struct {
		name         string
		data         []byte
		mode         os.FileMode
		dir          bool
		missing      bool
		architecture string
		target       *Target
		err          string
	}{
		{name: "arm64", data: arm, architecture: config.ArchARM64},
		{name: "x86_64", data: elfBinary(t, elf.EM_X86_64), architecture: config.ArchX8664},
		{name: "a script", data: []byte("#!/bin/sh\necho hello\n"), architecture: config.ArchARM64},
		{name: "a macos binary here", data: machoBinary(t, macho.CpuArm64), architecture: config.ArchARM64, target: here},
		{name: "missing", missing: true, architecture: config.ArchARM64, err: "bootstrap dist/api is not there: build it first, as koldstart zips it as it is"},
		{name: "a directory", dir: true, architecture: config.ArchARM64, err: "bootstrap dist/api is a directory: set bootstrap to the executable in it"},
		{name: "not executable", data: arm, mode: 0o644, architecture: config.ArchARM64, err: "bootstrap dist/api is not executable: run chmod +x dist/api"},
		{name: "another architecture", data: elfBinary(t, elf.EM_X86_64), architecture: config.ArchARM64, err: `bootstrap dist/api is built for linux/amd64, and the function's architecture is arm64: build it for linux/arm64, or set architecture = "x86_64"`},
		{name: "a macos binary for lambda", data: machoBinary(t, macho.CpuArm64), architecture: config.ArchARM64, err: "bootstrap dist/api is built for darwin/arm64, and lambda runs it on linux/arm64: build it for linux/arm64"},
		{name: "another machine's architecture", data: elfBinary(t, elf.EM_RISCV), architecture: config.ArchX8664, err: "bootstrap dist/api is built for linux/riscv, and lambda runs it on linux/amd64"},
		{name: "a linux binary here", data: arm, architecture: config.ArchARM64, target: here, err: "bootstrap dist/api is built for linux/arm64, which does not run on this machine, darwin/arm64: build it for darwin/arm64 to run it here, or invoke the function deployed"},
		{name: "neither", data: []byte("hello\n"), architecture: config.ArchARM64, err: "bootstrap dist/api is neither a binary nor a script that starts with #!: build it for linux/arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "dist", "api")
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o755)
			if tt.mode != 0 {
				mode = tt.mode
			}
			switch {
			case tt.dir:
				if err := os.Mkdir(file, 0o750); err != nil {
					t.Fatal(err)
				}
			case !tt.missing:
				if err := os.WriteFile(file, tt.data, mode); err != nil {
					t.Fatal(err)
				}
			}
			fn := config.Function{Runtime: config.RuntimeProvided, Bootstrap: "dist/api", Architecture: tt.architecture}
			target := LambdaTarget(tt.architecture)
			if tt.target != nil {
				target = *tt.target
			}
			files, err := Builder{Root: root, Stage: "dev"}.Files(context.Background(), "api", fn, target)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("Files = %v, want an error with %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Files: %v", err)
			}
			if len(files) != 1 || files[0].Name != "bootstrap" || !files[0].Executable || !bytes.Equal(files[0].Data, tt.data) {
				t.Errorf("files %+v, want the file as an executable bootstrap", files)
			}
		})
	}
}
