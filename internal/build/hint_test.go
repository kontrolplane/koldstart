package build

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

// TestNodeInstallHint checks a bundle that imports a package that is not installed says how to
// install it: npm install or npm ci without node_modules or for a package in package.json, npm
// install <package> for one that is not; and a missing relative import says nothing of npm.
func TestNodeInstallHint(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    string
		wantNot string
	}{
		{
			name:  "no node_modules",
			files: map[string]string{"src/api.ts": `import _ from "lodash"; export const handler = () => _.noop();`},
			want:  "there is no node_modules in ",
		},
		{
			name: "declared",
			files: map[string]string{
				"package.json":       `{"dependencies": {"lodash": "4.17.21"}}`,
				"node_modules/.keep": "",
				"src/api.ts":         `import _ from "lodash/fp"; export const handler = () => _.noop();`,
			},
			want: "lodash is in package.json but not installed: run npm install, or npm ci, in ",
		},
		{
			name: "undeclared",
			files: map[string]string{
				"package.json":       `{"dependencies": {}}`,
				"node_modules/.keep": "",
				"src/api.ts":         `import { z } from "zod"; import { S3 } from "@aws-sdk/client-s3"; export const handler = () => [z, S3];`,
			},
			want: "@aws-sdk/client-s3 and zod are not in package.json: add them with npm install @aws-sdk/client-s3 zod in ",
		},
		{
			name:    "relative",
			files:   map[string]string{"src/api.ts": `import { x } from "./missing"; export const handler = () => x;`},
			want:    `Could not resolve "./missing"`,
			wantNot: "npm",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			fn := config.Function{Runtime: config.RuntimeNode22, Handler: "src/api.handler"}
			_, err := Builder{Root: root, Stage: "dev"}.Build(context.Background(), "api", fn)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build = %v, want an error with %q", err, tt.want)
			}
			if tt.wantNot != "" && strings.Contains(err.Error(), tt.wantNot) {
				t.Errorf("Build = %v, want nothing of %q", err, tt.wantNot)
			}
		})
	}
}

// TestNodeExternalMissing checks an external that is not installed says the npm command that
// installs it for the function's architecture.
func TestNodeExternalMissing(t *testing.T) {
	tests := []struct {
		architecture string
		want         string
	}{
		{architecture: config.ArchARM64, want: "npm install --os=linux --cpu=arm64 sharp"},
		{architecture: config.ArchX8664, want: "npm install --os=linux --cpu=x64 sharp"},
	}
	for _, tt := range tests {
		t.Run(tt.architecture, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"src/api.ts": `export const handler = () => 1;`})
			fn := config.Function{
				Runtime: config.RuntimeNode22, Handler: "src/api.handler", Architecture: tt.architecture,
				Bundle: config.Bundle{External: []string{"sharp"}},
			}
			_, err := Builder{Root: root, Stage: "dev"}.Build(context.Background(), "api", fn)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Build = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestGoNoModule checks a go function outside a go module says to run go mod init.
func TestGoNoModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on the PATH")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"cmd/worker/main.go": "package main\n\nfunc main() {}\n"})
	t.Setenv("GOFLAGS", "")
	t.Setenv("GO111MODULE", "")
	fn := config.Function{Runtime: config.RuntimeProvided, Handler: "./cmd/worker", Architecture: config.ArchARM64}
	_, err := Builder{Root: root, Stage: "dev"}.Build(context.Background(), "worker", fn)
	if err == nil || !strings.Contains(err.Error(), "run go mod init <module path> in ") {
		t.Errorf("Build = %v, want an error that says to run go mod init", err)
	}
}
