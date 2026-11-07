package pkl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAsset checks each platform maps to the release binary built for it.
func TestAsset(t *testing.T) {
	tests := []struct {
		goos, goarch string
		musl         bool
		want         string
		wantErr      bool
	}{
		{goos: "darwin", goarch: "arm64", want: "pkl-macos-aarch64"},
		{goos: "darwin", goarch: "amd64", want: "pkl-macos-amd64"},
		{goos: "linux", goarch: "arm64", want: "pkl-linux-aarch64"},
		{goos: "linux", goarch: "amd64", want: "pkl-linux-amd64"},
		{goos: "linux", goarch: "amd64", musl: true, want: "pkl-alpine-linux-amd64"},
		{goos: "linux", goarch: "arm64", musl: true, wantErr: true},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "linux", goarch: "386", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			got, err := asset(tt.goos, tt.goarch, tt.musl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("asset() error = %v, want error %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("asset() = %q, want %q", got, tt.want)
			}
			if got != "" && checksums[got] == "" {
				t.Errorf("no checksum pinned for %s", got)
			}
		})
	}
}

// TestParseVersion checks the version is read from pkl --version, and compared by number.
func TestParseVersion(t *testing.T) {
	v, err := parseVersion("Pkl 0.32.2 (macOS 15.0, native)\n")
	if err != nil {
		t.Fatalf("parseVersion: %v", err)
	}
	if v != [3]int{0, 32, 2} {
		t.Errorf("parseVersion = %v, want [0 32 2]", v)
	}
	if _, err := parseVersion("something else"); err == nil {
		t.Error("parseVersion of no version: want an error")
	}
	tests := []struct {
		v, than [3]int
		want    bool
	}{
		{v: [3]int{0, 29, 9}, than: [3]int{0, 30, 0}, want: true},
		{v: [3]int{0, 30, 0}, than: [3]int{0, 30, 0}, want: false},
		{v: [3]int{0, 30, 10}, than: [3]int{0, 30, 9}, want: false},
		{v: [3]int{1, 0, 0}, than: [3]int{0, 30, 0}, want: false},
	}
	for _, tt := range tests {
		if got := older(tt.v, tt.than); got != tt.want {
			t.Errorf("older(%v, %v) = %v, want %v", tt.v, tt.than, got, tt.want)
		}
	}
}

// TestDownload checks a download is kept only when it matches its checksum, and is not
// downloaded again once it is in the cache.
func TestDownload(t *testing.T) {
	payload := []byte("#!/bin/sh\necho Pkl 0.32.2\n")
	sum := sha256.Sum256(payload)
	checksums["pkl-test"] = hex.EncodeToString(sum[:])
	checksums["pkl-tampered"] = strings.Repeat("0", 64)
	t.Cleanup(func() {
		delete(checksums, "pkl-test")
		delete(checksums, "pkl-tampered")
	})

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	ctx := context.Background()

	t.Run("tampered", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := download(ctx, server.Client(), server.URL+"/", dir, "pkl-tampered"); err == nil {
			t.Fatal("download with a wrong checksum: want an error")
		}
		if _, err := os.Stat(filepath.Join(dir, "pkl", Version, "pkl")); !os.IsNotExist(err) {
			t.Errorf("a binary with a wrong checksum was kept: %v", err)
		}
	})

	t.Run("cached", func(t *testing.T) {
		dir := t.TempDir()
		requests = 0
		path, err := download(ctx, server.Client(), server.URL+"/", dir, "pkl-test")
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s is not executable: %v", path, info.Mode())
		}
		if _, err := download(ctx, server.Client(), server.URL+"/", dir, "pkl-test"); err != nil {
			t.Fatalf("second download: %v", err)
		}
		if requests != 1 {
			t.Errorf("downloaded %d times, want 1", requests)
		}
	})

	t.Run("not found", func(t *testing.T) {
		missing := httptest.NewServer(http.NotFoundHandler())
		defer missing.Close()
		if _, err := download(ctx, missing.Client(), missing.URL+"/", t.TempDir(), "pkl-test"); err == nil {
			t.Fatal("download of a missing binary: want an error")
		}
	})
}

// TestInstalled checks the pkl a completion may use is found without a download: the one
// $KOLDSTART_PKL names, else the pinned one in the cache, else none.
func TestInstalled(t *testing.T) {
	cached := filepath.Join("cache", "koldstart", "pkl", Version, "pkl")
	tests := []struct {
		name string
		// env is what $KOLDSTART_PKL names, relative to the test's directory.
		env string
		// files are the files there, relative to it.
		files   []string
		want    string
		wantErr bool
	}{
		{name: "environment", env: "pkl-here", files: []string{"pkl-here", cached}, want: "pkl-here"},
		{name: "environment missing", env: "pkl-gone", files: []string{cached}, wantErr: true},
		{name: "cache", files: []string{cached}, want: cached},
		{name: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
			t.Setenv(EnvBinary, "")
			if tt.env != "" {
				t.Setenv(EnvBinary, filepath.Join(dir, tt.env))
			}
			for _, file := range tt.files {
				path := filepath.Join(dir, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Installed(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Installed() error = %v, want an error: %t", err, tt.wantErr)
			}
			want := tt.want
			if want != "" {
				want = filepath.Join(dir, want)
			}
			if got != want {
				t.Errorf("Installed() = %q, want %q", got, want)
			}
		})
	}
}
