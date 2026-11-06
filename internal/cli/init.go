package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/scaffold"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// defaultRegion is the region a new project deploys to when neither --region nor the aws profile
// names one.
const defaultRegion = "us-east-1"

func initCommand(name string, opts *Options) *cobra.Command {
	var app, template string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "start a project: a koldstart.pkl, its PklProject, a handler and the .gitignore entries",
		Long: fmt.Sprintf(`init writes a koldstart.pkl with one function behind GET /hello, and its handler in typescript,
go or python, into the directory, this one without it, and adds .koldstart/build/ and
.koldstart/local/ to .gitignore. It keeps any other file that is there already, and writes over a
koldstart.pkl only with --force.

Beside it goes a PklProject that names the schema of this release of %[1]s, or, for a dev build,
the schema it embeds, which init resolves into PklProject.deps.json; commit both. When it cannot
resolve it, the next command that reads the koldstart.pkl does.

The app is named after the directory, or --app, and deploys to --region, the region of the aws
profile, or %[2]s.`, name, defaultRegion),
		Example: fmt.Sprintf(`  %[1]s init
  %[1]s init orders --template go --region eu-west-1
  %[1]s init --app orders --template python`, name),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains(scaffold.Templates, template) {
				return usage(cmd, fmt.Errorf("-t %q: want %s", template, orList(scaffold.Templates)))
			}
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			if app == "" {
				if app = scaffold.AppName(abs); app == "" {
					return fmt.Errorf("%s does not make an app name: pass --app", filepath.Base(abs))
				}
			}
			region := initRegion(cmd.Context(), opts)
			dep, err := schema.Current()
			if err != nil {
				return err
			}
			res, err := scaffold.Write(scaffold.Options{
				Dir: abs, App: app, Region: region, Template: template, Dependency: dep, Force: force,
			})
			if err != nil && dir != "." {
				return fmt.Errorf("starting a project in %s: %w", dir, err)
			}
			if err != nil {
				return err
			}
			resolved, note := resolveWritten(cmd.Context(), pklResolver(name), name, abs)
			if resolved {
				res.Written = append(res.Written, pkl.DepsFile)
			}
			printInit(cmd.OutOrStdout(), name, dir, app, region, template, dep, res, note)
			return nil
		},
	}
	cmd.Flags().StringVar(&app, "app", "", "the app's name, the directory's by default")
	cmd.Flags().StringVarP(&template, "template", "t", scaffold.Node, "the language: "+orList(scaffold.Templates))
	cmd.Flags().BoolVar(&force, "force", false, "write over a koldstart.pkl that is there")
	_ = cmd.RegisterFlagCompletionFunc("template", cobra.FixedCompletions(scaffold.Templates, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// initRegion is --region, or the region of the aws profile, or defaultRegion. It reads only the
// shared config, and calls nothing.
func initRegion(ctx context.Context, opts *Options) string {
	if opts.Region != "" {
		return opts.Region
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if opts.Profile != "" {
		loaders = append(loaders, awsconfig.WithSharedConfigProfile(opts.Profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return defaultRegion
	}
	return cmp.Or(cfg.Region, defaultRegion)
}

// printInit says what init wrote and kept, the app and its region, the schema it amends, why
// its PklProject is not resolved when it is not, and what to run next.
func printInit(w io.Writer, name, dir, app, region, template string, dep schema.Dependency, res scaffold.Result, note string) {
	_, _ = fmt.Fprintf(w, "started %s, which deploys to %s, with the schema of koldstart %s\n\n", app, region, dep.Name())
	for _, f := range res.Written {
		_, _ = fmt.Fprintf(w, "  wrote %s\n", f)
	}
	for _, f := range res.Kept {
		_, _ = fmt.Fprintf(w, "  kept  %s, which was there already\n", f)
	}
	if len(res.Gitignore) > 0 {
		_, _ = fmt.Fprintf(w, "  added %s to .gitignore\n", andList(res.Gitignore))
	}
	if note != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", note)
	}
	_, _ = fmt.Fprintln(w, "\nnext:")
	for _, step := range nextSteps(name, dir, template, res) {
		_, _ = fmt.Fprintln(w, "  "+step)
	}
}

// nextSteps are the commands to run after init, each with what it does in a column of its own.
func nextSteps(name, dir, template string, res scaffold.Result) []string {
	type step struct{ run, does string }
	var steps []step
	if dir != "." {
		steps = append(steps, step{"cd " + dir, ""})
	}
	if template == scaffold.Go {
		if slices.Contains(res.Written, "go.mod") {
			steps = append(steps, step{"go mod tidy", "fetch aws-lambda-go, which the handler imports"})
		} else {
			steps = append(steps, step{"go get github.com/aws/aws-lambda-go", "add it to go.mod, as the handler imports it"})
		}
	}
	steps = append(steps, step{name + " plan", "see what a deploy creates"}, step{name + " deploy", "deploy it, and print the url of GET /hello"})
	width := 0
	for _, s := range steps {
		if s.does != "" {
			width = max(width, len(s.run))
		}
	}
	lines := make([]string, 0, len(steps))
	for _, s := range steps {
		if s.does == "" {
			lines = append(lines, s.run)
			continue
		}
		lines = append(lines, padRight(s.run, width)+"    "+s.does)
	}
	return lines
}
