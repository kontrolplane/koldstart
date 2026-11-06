// Package cli is the koldstart command, which also runs as kdst.
package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kontrolplane/koldstart/internal/version"
)

// The names the binary answers to. kdst is a link to koldstart.
const (
	nameLong  = "koldstart"
	nameShort = "kdst"
)

// The formats of -o.
const (
	outputText = "text"
	outputJSON = "json"
)

// debugLog is where --debug writes, in the directory koldstart runs in.
const debugLog = "debug.log"

// The exit codes: a command that failed, and a command line that was refused.
const (
	exitFailed = 1
	exitUsage  = 2
)

// Options are the global flags, shared by every command.
type Options struct {
	Stage   string
	Config  string
	Profile string
	Region  string
	Debug   bool
	// name is the name the binary was run as, for errors that say what to run.
	name string
	// notes is where loading the configuration says it resolves its PklProject first; stderr
	// when nil.
	notes io.Writer
}

// binary is the name the binary was run as, koldstart when it is not known.
func (o *Options) binary() string {
	return cmp.Or(o.name, nameLong)
}

// Name returns the name to show in help and errors for the binary at path: kdst when it was run
// through the link, koldstart otherwise.
func Name(path string) string {
	if filepath.Base(path) == nameShort {
		return nameShort
	}
	return nameLong
}

// The groups of commands in the help.
const (
	groupStart  = "start"
	groupDeploy = "deploying"
	groupWork   = "working"
	groupOther  = "other"
)

// Root builds the command, named name in its help, with ui as its terminal interface, or no ui
// command when it is nil. It defines --help and --version itself, so cobra adds neither -v nor
// its own wording, and -v stays free. Commands are listed in the order they are added, so it
// turns off cobra's sorting, which is global. A command line it refuses returns an error that
// names the help to read, for which ExitCode is 2. The root runs only to print its help, so its
// usage leaves out the line for running it.
func Root(name string, ui UI) *cobra.Command {
	opts := &Options{name: name}
	var logFile *os.File

	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:   name,
		Short: "deploy serverless applications to aws lambda",
		Long: fmt.Sprintf(`%s builds the functions a koldstart.pkl describes and deploys them, with their events and
resources, into your own aws account, recording what it deployed in a bucket there. A command that
works on a stage works on the one --stage names, dev without it.`, name),
		Example: fmt.Sprintf(`  %[1]s init orders
  %[1]s plan
  %[1]s deploy --stage prod`, name),
		Version:           version.Current().String(),
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              rootArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := requiredFlags(cmd); err != nil {
				return err
			}
			if !opts.Debug {
				return nil
			}
			f, err := os.OpenFile(debugLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("opening %s for --debug: %w", debugLog, err)
			}
			logFile = f
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if logFile == nil {
				return nil
			}
			return logFile.Close()
		},
	}
	root.SetVersionTemplate(name + " {{.Version}}\n")
	root.SetUsageTemplate(strings.Replace(root.UsageTemplate(), "{{if .Runnable}}", "{{if and .Runnable .HasParent}}", 1))
	root.SetFlagErrorFunc(flagError)

	flags := root.PersistentFlags()
	flags.StringVarP(&opts.Stage, "stage", "s", envOr("KOLDSTART_STAGE", "dev"), "stage to work on; $KOLDSTART_STAGE")
	flags.StringVarP(&opts.Config, "config", "c", envOr("KOLDSTART_CONFIG", "koldstart.pkl"), "pkl file; $KOLDSTART_CONFIG")
	flags.StringVar(&opts.Profile, "profile", os.Getenv("AWS_PROFILE"), "aws profile to use; $AWS_PROFILE")
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "aws region, over the configured one; $AWS_REGION")
	flags.BoolVar(&opts.Debug, "debug", false, "write debug logs to "+debugLog)
	flags.BoolP("help", "h", false, "print the flags and exit")
	root.Flags().Bool("version", false, "print the version and exit")
	_ = root.MarkPersistentFlagFilename("config", "pkl")
	for _, flag := range []string{"stage", "profile", "region"} {
		_ = root.RegisterFlagCompletionFunc(flag, cobra.NoFileCompletions)
	}

	root.AddGroup(
		&cobra.Group{ID: groupStart, Title: "Start a project:"},
		&cobra.Group{ID: groupDeploy, Title: "Deploy a stage:"},
		&cobra.Group{ID: groupWork, Title: "Work with a stage:"},
		&cobra.Group{ID: groupOther, Title: "Other commands:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, cmd := range cmds {
			cmd.GroupID = group
			root.AddCommand(cmd)
		}
	}
	add(groupStart, initCommand(name, opts), migrateCommand(name), upgradeCommand(name, opts))
	add(groupDeploy, packageCommand(name, opts), planCommand(name, opts), deployCommand(name, opts),
		importCommand(name, opts), removeCommand(name, opts), unlockCommand(name, opts))
	add(groupWork, invokeCommand(name, opts), offlineCommand(name, opts), logsCommand(name, opts), outputsCommand(name, opts))
	if ui != nil {
		add(groupWork, uiCommand(name, opts, ui))
	}
	add(groupOther, versionCommand(name))
	root.SetHelpCommand(helpCommand(name))
	root.SetHelpCommandGroupID(groupOther)
	root.SetCompletionCommandGroupID(groupOther)
	completionCommand(root)
	for _, cmd := range root.Commands() {
		checkArgs(cmd)
	}

	return root
}

func versionCommand(name string) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "print the version",
		Long: `version prints the version of this binary. -o json prints it with the commit, the build date,
the version of go and the platform it was built for, as one json object with its keys sorted:

  {
    "commit": "0123456789abcdef0123456789abcdef01234567",
    "date": "2026-11-04T00:00:00Z",
    "go": "go1.26.0",
    "modified": false,
    "platform": "linux/amd64",
    "version": "v0.1.0"
  }`,
		Example: fmt.Sprintf(`  %[1]s version
  %[1]s version -o json
  %[1]s version -o json | jq -r .commit`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(cmd, output); err != nil {
				return err
			}
			return printVersion(cmd.OutOrStdout(), output, version.Current())
		},
	}
	outputFlag(cmd, &output)
	return cmd
}

// outputFlag adds -o to cmd, text or json, and completes its values.
func outputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", outputText, "the format: text or json")
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]cobra.Completion{outputText, outputJSON}, cobra.ShellCompDirectiveNoFileComp))
}

// checkOutput refuses an -o other than text or json, as a usage error.
func checkOutput(cmd *cobra.Command, output string) error {
	if output != outputText && output != outputJSON {
		return usage(cmd, fmt.Errorf("-o %q: want text or json", output))
	}
	return nil
}

func printVersion(w io.Writer, output string, info version.Info) error {
	switch output {
	case outputText:
		_, err := fmt.Fprintln(w, info)
		return err
	case outputJSON:
		return writeJSON(w, info)
	default:
		return fmt.Errorf("-o %q: want text or json", output)
	}
}

// helpCommand prints the help of the command its arguments name. Unlike cobra's, it refuses a
// command that does not exist, rather than printing the root's help and succeeding.
func helpCommand(name string) *cobra.Command {
	return &cobra.Command{
		Use:     "help [command]",
		Short:   "print the help of a command",
		Long:    "help prints what a command does, its flags and examples, as --help does.",
		Example: fmt.Sprintf("  %[1]s help deploy\n  %[1]s help deploy function", name),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			parent, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var completions []cobra.Completion
			for _, sub := range parent.Commands() {
				if sub.IsAvailableCommand() && strings.HasPrefix(sub.Name(), toComplete) {
					completions = append(completions, cobra.CompletionWithDesc(sub.Name(), sub.Short))
				}
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil {
				return err
			}
			if len(rest) > 0 {
				return unknownCommand(target, rest[0])
			}
			return target.Help()
		},
	}
}

// completionCommand adds cobra's completion command to root, in the wording of the others.
func completionCommand(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() != "completion" {
			continue
		}
		cmd.Short = "print the completion script for a shell"
		cmd.Long = fmt.Sprintf(`completion prints the script that completes the commands and flags of %[1]s in bash, fish,
powershell or zsh. The help of each shell says where the script goes.`, root.Name())
		cmd.Example = fmt.Sprintf("  %[1]s completion zsh --help\n  source <(%[1]s completion zsh)", root.Name())
		for _, shell := range cmd.Commands() {
			shell.Short = "print the completion script for " + shell.Name()
		}
	}
}

// usageError is a command line that was refused: an unknown command or flag, a missing flag, or
// arguments the command does not take. It names the help of the command to read.
type usageError struct {
	err error
	// help is the command whose help to read.
	help string
}

func (e *usageError) Error() string {
	return fmt.Sprintf("%s (see %s --help)", e.err, e.help)
}

func (e *usageError) Unwrap() error {
	return e.err
}

// usage refuses a command line given to cmd.
func usage(cmd *cobra.Command, err error) error {
	return &usageError{err: err, help: cmd.CommandPath()}
}

// ExitCode is the exit code for an error that Root returned: 2 for a command line it refused, and
// 1 for a command that failed.
func ExitCode(err error) int {
	if _, ok := errors.AsType[*usageError](err); ok {
		return exitUsage
	}
	return exitFailed
}

// rootArgs refuses a command that does not exist. The root is runnable so that cobra leaves this
// to it, rather than refusing the command in its own wording before any command runs.
func rootArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return unknownCommand(cmd, args[0])
	}
	return nil
}

// unknownCommand refuses arg as a command of cmd, suggesting the commands it may be a typo of:
// those it starts, and those two edits away.
func unknownCommand(cmd *cobra.Command, arg string) error {
	cmd.SuggestionsMinimumDistance = cmp.Or(cmd.SuggestionsMinimumDistance, 2)
	msg := fmt.Sprintf("unknown command %q for %s", arg, cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(arg); len(suggestions) > 0 {
		msg += ": did you mean " + orList(suggestions) + "?"
	}
	return usage(cmd, errors.New(msg))
}

// checkArgs makes the arguments cmd and its subcommands refuse usage errors, saying what they take,
// and completes no file names for a command that takes no arguments.
func checkArgs(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		checkArgs(sub)
	}
	_, takes, _ := strings.Cut(cmd.Use, " ")
	if takes == "" && cmd.ValidArgsFunction == nil && len(cmd.ValidArgs) == 0 {
		cmd.ValidArgsFunction = cobra.NoFileCompletions
	}
	if cmd.Args == nil {
		return
	}
	check := cmd.Args
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if check(cmd, args) == nil {
			return nil
		}
		switch {
		case len(args) > 0 && cmd.HasAvailableSubCommands():
			return unknownCommand(cmd, args[0])
		case takes == "":
			return usage(cmd, fmt.Errorf("%s takes no arguments, got %q", cmd.CommandPath(), args[0]))
		default:
			return usage(cmd, fmt.Errorf("%s takes %s, got %s", cmd.CommandPath(), takes, arguments(len(args))))
		}
	}
}

// arguments says how many arguments there are.
func arguments(n int) string {
	switch n {
	case 0:
		return "none"
	case 1:
		return "1 argument"
	default:
		return fmt.Sprintf("%d arguments", n)
	}
}

// durationFlag is the type pflag names a flag of a time.Duration by.
const durationFlag = "duration"

// flagError refuses a flag cmd does not have, or one without its value, suggesting the flags an
// unknown one may be a typo of. A duration it cannot read says what one looks like, as go's
// parser knows no days.
func flagError(cmd *cobra.Command, err error) error {
	if invalid, ok := errors.AsType[*pflag.InvalidValueError](err); ok && invalid.GetFlag().Value.Type() == durationFlag {
		return usage(cmd, fmt.Errorf("--%s %q: want a duration such as 90s, 30m or 72h", invalid.GetFlag().Name, invalid.GetValue()))
	}
	unknown, ok := errors.AsType[*pflag.NotExistError](err)
	if !ok || unknown.GetSpecifiedName() == "" {
		return usage(cmd, err)
	}
	typed := unknown.GetSpecifiedName()
	var suggestions []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && (strings.HasPrefix(f.Name, typed) || distance(typed, f.Name) <= 2) {
			suggestions = append(suggestions, "--"+f.Name)
		}
	})
	if len(suggestions) == 0 {
		return usage(cmd, err)
	}
	return usage(cmd, fmt.Errorf("%w: did you mean %s?", err, orList(suggestions)))
}

// requiredFlags refuses cmd when a flag it needs was not given. cobra checks them only after the
// command has started, and in its own wording.
func requiredFlags(cmd *cobra.Command) error {
	var missing []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, required := f.Annotations[cobra.BashCompOneRequiredFlag]; required && !f.Changed {
			name := "--" + f.Name
			if f.Shorthand != "" {
				name = "-" + f.Shorthand + "/" + name
			}
			missing = append(missing, name)
		}
	})
	if len(missing) == 0 {
		return nil
	}
	return usage(cmd, fmt.Errorf("%s needs %s", cmd.CommandPath(), andList(missing)))
}

// distance is the levenshtein distance between a and b, the edits that turn one into the other.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// orList names the choices: a, b or c.
func orList(choices []string) string {
	return joinLast(choices, " or ")
}

// andList names the things: a, b and c.
func andList(things []string) string {
	return joinLast(things, " and ")
}

// joinLast joins words with commas, and the last two with last.
func joinLast(words []string, last string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + last + words[len(words)-1]
}

// envOr returns the environment variable key, or fallback when it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
