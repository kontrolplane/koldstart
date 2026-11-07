package build

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
)

// ImageRepository is the ecr repository the images of an app go in.
func ImageRepository(app string) string {
	return "koldstart/" + app
}

// ImageTag tags a function's image by the hash of what it is built from, so the same build is
// pushed once, and a deploy that changes nothing in it leaves the function as it is.
func ImageTag(stage, function, sha string) string {
	return stage + "-" + function + "-" + sha[:min(32, len(sha))]
}

// imageHash hashes what an image is built from: every file of its context, but what
// .dockerignore leaves out, its dockerfile, its build arguments and the platform.
//
//nolint:gosec // reads the project's own build context
func (b Builder) imageHash(fn config.Function, target Target) (string, error) {
	img := fn.Image
	contextDir := filepath.Join(b.Root, filepath.FromSlash(img.Context))
	ignore := dockerIgnore(contextDir)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "platform %s/%s\n", target.GOOS, target.GOARCH)
	for _, k := range slices.Sorted(maps.Keys(img.BuildArgs)) {
		_, _ = fmt.Fprintf(h, "arg %s=%s\n", k, img.BuildArgs[k])
	}
	dockerfile, err := os.ReadFile(filepath.Join(b.Root, filepath.FromSlash(img.Dockerfile)))
	if err != nil {
		return "", fmt.Errorf("reading the dockerfile: %w", err)
	}
	_, _ = fmt.Fprintf(h, "dockerfile %d\n", len(dockerfile))
	h.Write(dockerfile)
	err = filepath.WalkDir(contextDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(contextDir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ignore(rel) || rel == ".git" || rel == ".koldstart" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "file %s %o %d\n", rel, info.Mode().Perm()&0o111, info.Size())
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("hashing the build context %s: %w", contextDir, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dockerIgnore reads the .dockerignore of a context into a test of a slash separated path: a
// pattern matches the path or a directory it is in. A pattern starting with ! is not supported,
// and reads as no pattern, so it can only make more of the context count.
//
//nolint:gosec // reads the project's own .dockerignore
func dockerIgnore(contextDir string) func(string) bool {
	f, err := os.Open(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		return func(string) bool { return false }
	}
	defer func() { _ = f.Close() }()
	var patterns []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		patterns = append(patterns, strings.Trim(path.Clean(line), "/"))
	}
	return func(rel string) bool {
		for _, p := range patterns {
			for dir := rel; dir != "."; dir = path.Dir(dir) {
				if ok, _ := path.Match(p, dir); ok {
					return true
				}
				if ok, _ := path.Match(strings.TrimPrefix(p, "**/"), path.Base(dir)); ok && strings.HasPrefix(p, "**/") {
					return true
				}
			}
		}
		return false
	}
}

// BuildImage builds a function's image with docker for the platform, tagged ref. With push, it
// goes to its registry, as lambda takes it: a single image, without the attestations buildx adds,
// which lambda refuses. Without, it is loaded into docker on this machine.
//
//nolint:gosec // runs docker on the project's own dockerfile
func (b Builder) BuildImage(ctx context.Context, fn config.Function, target Target, ref string, push bool) error {
	img := fn.Image
	docker, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("container image functions need docker on the PATH: %w", err)
	}
	args := []string{"buildx", "build", "--platform", target.GOOS + "/" + target.GOARCH, "--provenance=false", "--sbom=false",
		"-f", filepath.Join(b.Root, filepath.FromSlash(img.Dockerfile)), "-t", ref}
	for _, k := range slices.Sorted(maps.Keys(img.BuildArgs)) {
		args = append(args, "--build-arg", k+"="+img.BuildArgs[k])
	}
	if push {
		args = append(args, "--push")
	} else {
		args = append(args, "--load")
	}
	args = append(args, filepath.Join(b.Root, filepath.FromSlash(img.Context)))
	cmd := exec.CommandContext(ctx, docker, args...)
	cmd.Dir = b.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker buildx build of %s: %w\n%s", ref, err, lastLines(string(out), 20))
	}
	return nil
}

// lastLines is the end of a command's output, where its error is.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
