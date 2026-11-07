package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/pkl"
)

// complete runs __complete with args on a root whose completions read through c, and returns
// the lines it printed: the completions, then the directive.
func complete(t *testing.T, c completer, args ...string) []string {
	t.Helper()
	ui := func(context.Context, *Console, string) error { return nil }
	root := newRoot(nameShort, ui, c)
	var out bytes.Buffer
	var errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	return strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
}

// errOffline is what the fakes fail with, as aws or pkl would without a network or a project.
var errOffline = errors.New("offline")

// checkoutProject is the configuration the fakes read: two functions, one of them an image, and
// two resources.
func checkoutProject() *config.Project {
	return &config.Project{
		App:    "checkout",
		Region: "eu-west-1",
		Functions: map[string]config.Function{
			"storefront": {Runtime: "nodejs22.x"},
			"payments":   {Runtime: "provided.al2023"},
			"reconcile":  {Image: &config.Image{}},
		},
		Resources: map[string]config.Resource{
			"orders":   {Type: "AWS::DynamoDB::Table"},
			"receipts": {Type: "AWS::S3::Bucket"},
		},
	}
}

// TestStageCompletions checks the stages offered are those of the app, or of every app with
// the apps they are of, always with the stage there is without --stage, and only those that
// start with what is typed.
func TestStageCompletions(t *testing.T) {
	names := []string{"checkout/dev", "checkout/prod", "shop/dev", "shop/pr-128", "broken"}
	tests := []struct {
		name       string
		names      []string
		app        string
		fallback   string
		toComplete string
		want       []cobra.Completion
	}{
		{name: "of the app", names: names, app: "checkout", fallback: "dev", want: []cobra.Completion{"dev", "prod"}},
		{name: "fallback not deployed", names: names, app: "checkout", fallback: "test", want: []cobra.Completion{"dev", "prod", "test"}},
		{
			name: "every app", names: names, fallback: "dev",
			want: []cobra.Completion{"dev\tcheckout and shop", "pr-128\tshop", "prod\tcheckout"},
		},
		{name: "typed", names: names, app: "checkout", fallback: "dev", toComplete: "p", want: []cobra.Completion{"prod"}},
		{name: "fallback not typed", names: names, fallback: "dev", toComplete: "pr-", want: []cobra.Completion{"pr-128\tshop"}},
		{name: "nothing listed", fallback: "dev", want: []cobra.Completion{"dev"}},
		{name: "app not deployed", names: names, app: "billing", fallback: "dev", want: []cobra.Completion{"dev"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stageCompletions(tt.names, tt.app, tt.fallback, tt.toComplete)
			if !slices.Equal(got, tt.want) {
				t.Errorf("stageCompletions() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCompleteStage checks --stage completes from the state bucket of the project's region, or
// of --region, as --profile names it, falling back to the default stage alone, quietly, when the
// bucket cannot be listed.
func TestCompleteStage(t *testing.T) {
	listed := []string{"checkout/dev", "checkout/staging", "shop/prod"}
	tests := []struct {
		name        string
		project     error
		stages      error
		env         string
		args        []string
		want        []string
		wantRegion  string
		wantProfile string
	}{
		{
			name: "project", args: []string{"deploy", "--stage", ""},
			want: []string{"dev", "staging", ":4"}, wantRegion: "eu-west-1",
		},
		{
			name: "flags", args: []string{"--profile", "prod", "--region", "us-east-1", "deploy", "--stage", ""},
			want: []string{"dev", "staging", ":4"}, wantRegion: "us-east-1", wantProfile: "prod",
		},
		{
			name: "no project", project: errOffline, args: []string{"ui", "-s", ""},
			want: []string{"dev\tcheckout", "prod\tshop", "staging\tcheckout", ":4"},
		},
		{
			name: "offline", stages: errOffline, args: []string{"deploy", "--stage", ""},
			want: []string{"dev", ":4"}, wantRegion: "eu-west-1",
		},
		{
			name: "environment", env: "test", args: []string{"plan", "--stage", ""},
			want: []string{"dev", "staging", "test", ":4"}, wantRegion: "eu-west-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KOLDSTART_STAGE", tt.env)
			t.Setenv("AWS_PROFILE", "")
			t.Setenv("AWS_REGION", "")
			var region, profile string
			c := completer{
				project: func(context.Context, *Options) (*config.Project, error) {
					if tt.project != nil {
						return nil, tt.project
					}
					return checkoutProject(), nil
				},
				stages: func(_ context.Context, opts *Options, r string) ([]string, error) {
					region, profile = r, opts.Profile
					return listed, tt.stages
				},
			}
			got := complete(t, c, tt.args...)
			if !slices.Equal(got, tt.want) {
				t.Errorf("__complete %v = %q, want %q", tt.args, got, tt.want)
			}
			if region != tt.wantRegion || profile != tt.wantProfile {
				t.Errorf("listed the stages in %q as %q, want %q as %q", region, profile, tt.wantRegion, tt.wantProfile)
			}
		})
	}
}

// TestCompleteNames checks -f completes the functions of the configuration --config names, for
// --stage, import its resources and nothing after them, and that a configuration that does not
// load completes nothing.
func TestCompleteNames(t *testing.T) {
	functions := []string{"payments\tprovided.al2023", "reconcile\timage", "storefront\tnodejs22.x", ":4"}
	tests := []struct {
		name       string
		project    error
		args       []string
		want       []string
		wantConfig string
		wantStage  string
	}{
		{name: "invoke", args: []string{"invoke", "-f", ""}, want: functions, wantConfig: "koldstart.pkl", wantStage: "dev"},
		{name: "logs", args: []string{"logs", "--function", "s"}, want: []string{"storefront\tnodejs22.x", ":4"}, wantConfig: "koldstart.pkl", wantStage: "dev"},
		{
			name: "deploy function", args: []string{"-c", "koldstart.prod.pkl", "deploy", "function", "--stage", "prod", "-f", ""},
			want: functions, wantConfig: "koldstart.prod.pkl", wantStage: "prod",
		},
		{
			name: "import", args: []string{"import", ""},
			want: []string{"orders\tAWS::DynamoDB::Table", "receipts\tAWS::S3::Bucket", ":4"}, wantConfig: "koldstart.pkl", wantStage: "dev",
		},
		{name: "identifier", args: []string{"import", "orders", ""}, want: []string{":4"}},
		{name: "no project", project: errOffline, args: []string{"invoke", "-f", ""}, want: []string{":4"}, wantConfig: "koldstart.pkl", wantStage: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KOLDSTART_STAGE", "")
			t.Setenv("KOLDSTART_CONFIG", "")
			var configPath, stage string
			c := completer{
				project: func(_ context.Context, opts *Options) (*config.Project, error) {
					configPath, stage = opts.Config, opts.Stage
					if tt.project != nil {
						return nil, tt.project
					}
					return checkoutProject(), nil
				},
				stages: func(context.Context, *Options, string) ([]string, error) {
					t.Error("listed the stages to complete a name")
					return nil, nil
				},
			}
			got := complete(t, c, tt.args...)
			if !slices.Equal(got, tt.want) {
				t.Errorf("__complete %v = %q, want %q", tt.args, got, tt.want)
			}
			if configPath != tt.wantConfig || stage != tt.wantStage {
				t.Errorf("read %q for stage %q, want %q for %q", configPath, stage, tt.wantConfig, tt.wantStage)
			}
		})
	}
}

// TestCompleteFiles checks the flags and arguments that take a file or directory complete
// those, filtered to the extensions they take.
func TestCompleteFiles(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "event", args: []string{"invoke", "--path", ""}, want: []string{"json", ":8"}},
		{name: "serverless.yml", args: []string{"migrate", ""}, want: []string{"yml", "yaml", ":8"}},
		{name: "after serverless.yml", args: []string{"migrate", "serverless.yml", ""}, want: []string{":4"}},
		{name: "migrate out", args: []string{"migrate", "--out", ""}, want: []string{"pkl", ":8"}},
		{name: "directory", args: []string{"init", ""}, want: []string{":16"}},
		{name: "after directory", args: []string{"init", "orders", ""}, want: []string{":4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := complete(t, completer{}, tt.args...)
			if !slices.Equal(got, tt.want) {
				t.Errorf("__complete %v = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestFlagsComplete checks every flag that takes a value completes values or files of its own,
// so none falls back to cobra's file names by accident.
func TestFlagsComplete(t *testing.T) {
	ui := func(context.Context, *Console, string) error { return nil }
	root := newRoot(nameShort, ui, completer{})
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.NoOptDefVal != "" || completesFiles(f) {
				return
			}
			if _, ok := cmd.GetFlagCompletionFunc(f.Name); !ok {
				t.Errorf("%s --%s completes nothing of its own", cmd.CommandPath(), f.Name)
			}
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// TestCompletionProject checks the configuration is not evaluated for a completion when it
// does not exist, or when its PklProject must be resolved first, which would download.
func TestCompletionProject(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "no configuration", want: "reading "},
		{
			name:  "unresolved",
			files: map[string]string{"koldstart.pkl": "app = \"orders\"\n", pkl.ProjectFile: "amends \"pkl:Project\"\n"},
			want:  "is not resolved yet",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(t.TempDir(), "pkl")
			if err := os.WriteFile(binary, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(pkl.EnvBinary, binary)
			opts := &Options{Config: filepath.Join(dir, "koldstart.pkl"), Stage: "dev"}
			if _, err := completionProject(t.Context(), opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("completionProject() = %v, want an error with %q", err, tt.want)
			}
		})
	}
}
