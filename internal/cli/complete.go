package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/logging"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/state"
)

// How long a completion waits: for pkl to evaluate the configuration here, and for aws to list
// the stages. A shell waits on both, so aws gets a couple of seconds and no more.
const (
	projectTimeout = 10 * time.Second
	stagesTimeout  = 2 * time.Second
)

// completer reads what the completions offer: the configuration here, offline, and the stages
// in the state bucket of the account. Tests replace both. A completion prints no errors: what
// it cannot read, it offers nothing of.
type completer struct {
	// project evaluates the configuration the global flags name, with no download.
	project func(ctx context.Context, opts *Options) (*config.Project, error)
	// stages lists the stages in the state bucket of the account of the global flags in region,
	// as app/stage.
	stages func(ctx context.Context, opts *Options, region string) ([]string, error)
}

// liveCompleter reads the configuration with pkl and the stages from aws.
var liveCompleter = completer{project: completionProject, stages: bucketStages}

// completionProject evaluates the configuration the global flags name for their stage, without
// the checks of a deploy, so one being written still completes, and infers the runtimes it can,
// for the descriptions; a function whose runtime cannot be inferred is left without one. It
// downloads nothing: without a pkl here, or with a PklProject to resolve first, there is nothing
// to complete.
func completionProject(ctx context.Context, opts *Options) (*config.Project, error) {
	path, err := filepath.Abs(opts.Config)
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", opts.Config, err)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("reading %s: %w", opts.Config, err)
	}
	dir, err := pkl.FindProject(path)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		stale, err := pkl.Stale(dir)
		if err != nil {
			return nil, err
		}
		if stale {
			return nil, fmt.Errorf("the %s of %s is not resolved yet", pkl.ProjectFile, opts.Config)
		}
	}
	binary, err := pkl.Installed(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding pkl: %w", err)
	}
	if binary == "" {
		return nil, errors.New("no pkl here, and none downloaded yet")
	}
	ctx, cancel := context.WithTimeout(ctx, projectTimeout)
	defer cancel()
	out, err := pkl.Evaluator{Binary: binary}.EvalJSON(ctx, path, map[string]string{"stage": opts.Stage})
	if err != nil {
		return nil, err
	}
	p, err := config.Decode(out)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", opts.Config, err)
	}
	_ = p.Infer(filepath.Dir(path))
	return p, nil
}

// bucketStages lists the stages in the state bucket of the account of the global flags in
// region, or in the profile's region when region is empty. The sdk logs nothing, so the shell
// shows nothing of it.
func bucketStages(ctx context.Context, opts *Options, region string) ([]string, error) {
	cfg, err := awsConfig(ctx, opts, region)
	if err != nil {
		return nil, err
	}
	cfg.Logger = logging.Nop{}
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("finding the aws account: %w", err)
	}
	sess := newSession(cfg, aws.ToString(ident.Account))
	b := &state.S3{Client: sess.S3, Bucket: state.BucketName(sess.Account, sess.Region), Account: sess.Account}
	return b.Stages(ctx)
}

// completionContext is the context a completion runs in, which cobra gives the command it
// completes for.
func completionContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// stageNames completes --stage: the stages of the app of the configuration here in its region's
// state bucket, those of every app without one, and the stage there is without --stage. It waits
// for aws stagesTimeout at most.
func (c completer) stageNames(opts *Options) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		ctx := completionContext(cmd)
		var app, region string
		if p, err := c.project(ctx, opts); err == nil {
			app, region = p.App, p.Region
		}
		ctx, cancel := context.WithTimeout(ctx, stagesTimeout)
		defer cancel()
		names, err := c.stages(ctx, opts, cmp.Or(opts.Region, region))
		if err != nil {
			names = nil
		}
		return stageCompletions(names, app, envOr("KOLDSTART_STAGE", "dev"), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// stageCompletions are the stages of names, each app/stage, that start with toComplete: those of
// app, or of every app when app is empty, described by the apps they are of. fallback, the stage
// there is without --stage, is always one of them.
func stageCompletions(names []string, app, fallback, toComplete string) []cobra.Completion {
	apps := map[string][]string{fallback: nil}
	for _, name := range names {
		a, stage, ok := strings.Cut(name, "/")
		if !ok || (app != "" && a != app) {
			continue
		}
		apps[stage] = append(apps[stage], a)
	}
	var out []cobra.Completion
	for _, stage := range slices.Sorted(maps.Keys(apps)) {
		if !strings.HasPrefix(stage, toComplete) {
			continue
		}
		if app != "" || len(apps[stage]) == 0 {
			out = append(out, stage)
			continue
		}
		out = append(out, cobra.CompletionWithDesc(stage, andList(slices.Sorted(slices.Values(apps[stage])))))
	}
	return out
}

// functions completes a function of the configuration here, described by its runtime.
func (c completer) functions(opts *Options) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		p, err := c.project(completionContext(cmd), opts)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return named(p.Functions, toComplete, func(fn config.Function) string {
			if fn.IsImage() {
				return "image"
			}
			return fn.Runtime
		}), cobra.ShellCompDirectiveNoFileComp
	}
}

// resources completes the first argument with a resource of the configuration here, described
// by its type, and nothing after it.
func (c completer) resources(opts *Options) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		p, err := c.project(completionContext(cmd), opts)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return named(p.Resources, toComplete, func(r config.Resource) string { return r.Type }), cobra.ShellCompDirectiveNoFileComp
	}
}

// named completes the keys of m that start with toComplete, sorted, each described by describe.
func named[T any](m map[string]T, toComplete string, describe func(T) string) []cobra.Completion {
	var out []cobra.Completion
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if strings.HasPrefix(name, toComplete) {
			out = append(out, cobra.CompletionWithDesc(name, describe(m[name])))
		}
	}
	return out
}

// functionFlag adds -f to cmd, the function it works on, which it needs, and completes the
// functions of the configuration here.
func functionFlag(cmd *cobra.Command, function *string, usage string, opts *Options, c completer) {
	cmd.Flags().StringVarP(function, "function", "f", "", usage)
	_ = cmd.MarkFlagRequired("function")
	_ = cmd.RegisterFlagCompletionFunc("function", c.functions(opts))
}

// fileArgument completes the first argument with a file ending in one of exts, or with a
// directory when there are none, and nothing after it.
func fileArgument(exts ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		switch {
		case len(args) > 0:
			return nil, cobra.ShellCompDirectiveNoFileComp
		case len(exts) == 0:
			return nil, cobra.ShellCompDirectiveFilterDirs
		default:
			return exts, cobra.ShellCompDirectiveFilterFileExt
		}
	}
}

// noFileFlags makes the flags of cmd and its subcommands that take a value, and complete neither
// values nor files of their own, complete nothing, rather than cobra's file names.
func noFileFlags(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		noFileFlags(sub)
	}
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.NoOptDefVal != "" || completesFiles(f) {
			return
		}
		if _, ok := cmd.GetFlagCompletionFunc(f.Name); !ok {
			_ = cmd.RegisterFlagCompletionFunc(f.Name, cobra.NoFileCompletions)
		}
	})
}

// completesFiles reports whether f completes file or directory names, as a cobra annotation
// says.
func completesFiles(f *pflag.Flag) bool {
	_, files := f.Annotations[cobra.BashCompFilenameExt]
	_, dirs := f.Annotations[cobra.BashCompSubdirsInDir]
	return files || dirs
}
