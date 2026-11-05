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
		Long: fmt.Sprintf(`init writes a koldstart.pkl with one function behind an http route, GET /hello, and its
handler in typescript or go, into the directory, this one without it. It adds .koldstart/build/ and
.koldstart/local/ to .gitignore. Files that are there already are kept; koldstart.pkl is refused
unless --force is passed.

The app is named after the directory, and deploys to --region, or the region of the aws profile,
or %s.`, defaultRegion),
		Example: fmt.Sprintf(`  %[1]s init
  %[1]s init orders --template go --region eu-west-1`, name),
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
			res, err := scaffold.Write(scaffold.Options{
				Dir: abs, App: app, Region: initRegion(cmd.Context(), opts), Template: template, Version: schemaVersion(), Force: force,
			})
			if err != nil {
				return err
			}
			printInit(cmd.OutOrStdout(), name, dir, template, res)
			return nil
		},
	}
	cmd.Flags().StringVar(&app, "app", "", "the app's name, the directory's by default")
	cmd.Flags().StringVarP(&template, "template", "t", scaffold.Node, "the handler's language: "+strings.Join(scaffold.Templates, " or "))
	cmd.Flags().BoolVar(&force, "force", false, "write over a koldstart.pkl that is there")
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

func printInit(w io.Writer, name, dir, template string, res scaffold.Result) {
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
	_, _ = fmt.Fprintf(w, "  %[1]s plan\n  %[1]s deploy\n", name)
}
