package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/scaffold"
)

// defaultRegion is the region a new project deploys to when neither --region nor the aws profile
// names one.
const defaultRegion = "us-east-1"

func initCommand(name string, opts *Options) *cobra.Command {
	var app, template string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "start a project: a koldstart.pkl, a handler and the .gitignore entries",
		Long: fmt.Sprintf(`init writes a koldstart.pkl with one function behind GET /hello, and its handler in typescript,
go or python, into the directory, this one without it, and adds .koldstart/build/ and
.koldstart/local/ to .gitignore. It keeps any other file that is there already, and writes over a
koldstart.pkl only with --force.

The app is named after the directory, or --app, and deploys to --region, the region of the aws
profile, or %s.`, defaultRegion),
		Example: fmt.Sprintf(`  %[1]s init
  %[1]s init orders --template go --region eu-west-1
  %[1]s init --app orders --template python`, name),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			res, err := scaffold.Write(scaffold.Options{
				Dir: abs, App: app, Region: region, Template: template, Version: schemaVersion(), Force: force,
			})
			if err != nil && dir != "." {
				return fmt.Errorf("starting a project in %s: %w", dir, err)
			}
			if err != nil {
				return err
			}
			printInit(cmd.OutOrStdout(), name, dir, app, region, template, res)
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

// printInit says what init wrote and kept, the app and its region, and what to run next.
func printInit(w io.Writer, name, dir, app, region, template string, res scaffold.Result) {
	_, _ = fmt.Fprintf(w, "started %s, which deploys to %s\n\n", app, region)
	for _, f := range res.Written {
		_, _ = fmt.Fprintf(w, "  wrote %s\n", f)
	}
	for _, f := range res.Kept {
		_, _ = fmt.Fprintf(w, "  kept  %s, which was there already\n", f)
	}
	if len(res.Gitignore) > 0 {
		_, _ = fmt.Fprintf(w, "  added %s to .gitignore\n", strings.Join(res.Gitignore, " and "))
	}
	_, _ = fmt.Fprintln(w, "\nnext:")
	if dir != "." {
		_, _ = fmt.Fprintf(w, "  cd %s\n", dir)
	}
	if template == scaffold.Go {
		if slices.Contains(res.Written, "go.mod") {
			_, _ = fmt.Fprintln(w, "  go mod tidy")
		} else {
			_, _ = fmt.Fprintln(w, "  go get github.com/aws/aws-lambda-go")
		}
	}
	_, _ = fmt.Fprintf(w, "  %[1]s plan      see what a deploy creates\n  %[1]s deploy    deploy it, and print the url of GET /hello\n", name)
}
