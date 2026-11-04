// Package cli is the koldstart command, which also runs as kdst.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

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

// Options are the global flags, shared by every command.
type Options struct {
	Stage   string
	Config  string
	Profile string
	Region  string
	Debug   bool
}

// Name returns the name to show in help and errors for the binary at path: kdst when it was run
// through the link, koldstart otherwise.
func Name(path string) string {
	if filepath.Base(path) == nameShort {
		return nameShort
	}
	return nameLong
}

// Root builds the command, named name in its help.
func Root(name string) *cobra.Command {
	opts := &Options{}
	var logFile *os.File

	root := &cobra.Command{
		Use:           name,
		Short:         "deploy serverless applications to aws lambda",
		Version:       version.Current().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
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

	flags := root.PersistentFlags()
	flags.StringVarP(&opts.Stage, "stage", "s", envOr("KOLDSTART_STAGE", "dev"), "stage to work on; $KOLDSTART_STAGE")
	flags.StringVarP(&opts.Config, "config", "c", envOr("KOLDSTART_CONFIG", "koldstart.pkl"), "configuration file; $KOLDSTART_CONFIG")
	flags.StringVar(&opts.Profile, "profile", os.Getenv("AWS_PROFILE"), "aws profile from the shared aws config; $AWS_PROFILE")
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "aws region, takes precedence over region in the configuration; $AWS_REGION")
	flags.BoolVar(&opts.Debug, "debug", false, "write debug logs to "+debugLog)
	// Defined here so cobra adds neither -v nor its own wording; -v stays free.
	flags.BoolP("help", "h", false, "print the flags and exit")
	root.Flags().Bool("version", false, "print the version and exit")

	root.AddCommand(versionCommand(name))
	root.AddCommand(packageCommand(name, opts))
	root.AddCommand(planCommand(name, opts))
	root.AddCommand(deployCommand(name, opts))
	root.AddCommand(removeCommand(name, opts))
	root.AddCommand(unlockCommand(name, opts))
	root.AddCommand(outputsCommand(name, opts))

	return root
}

func versionCommand(name string) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "print the version",
		Example: fmt.Sprintf(`  %[1]s version
  %[1]s version -o json`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printVersion(cmd.OutOrStdout(), output, version.Current())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "text or json")
	return cmd
}

func printVersion(w io.Writer, output string, info version.Info) error {
	switch output {
	case outputText:
		_, err := fmt.Fprintln(w, info)
		return err
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	default:
		return fmt.Errorf("-o %q: want text or json", output)
	}
}

// envOr returns the environment variable key, or fallback when it is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
