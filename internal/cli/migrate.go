package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/migrate"
	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/version"
)

// schemaVersion is the version of the schema package a converted file amends: this koldstart's,
// when it is a release, else the first one.
func schemaVersion() string {
	v := strings.TrimPrefix(version.Current().Version, "v")
	if regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return v
	}
	return "0.1.0"
}

// migrateCommand converts a serverless.yml.
//
//nolint:gosec // reads the serverless.yml the user names
func migrateCommand(name string) *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "migrate [serverless.yml]",
		Short: "convert a serverless framework project",
		Long: `migrate converts a serverless.yml, ./serverless.yml without a path, into a koldstart.pkl beside
it. What it cannot convert is left in the file as a TODO comment above where it belongs, and
listed at the end. The file is checked with pkl before it is written, so it is valid even where it
is not complete.`,
		Example: fmt.Sprintf(`  %[1]s migrate
  %[1]s migrate path/to/serverless.yml --out koldstart.pkl`, name),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in := "serverless.yml"
			if len(args) == 1 {
				in = args[0]
			}
			if out == "" {
				out = filepath.Join(filepath.Dir(in), "koldstart.pkl")
			}
			if _, err := os.Stat(out); err == nil && !force {
				return fmt.Errorf("%s exists: pass --force to write over it, or --out to write elsewhere", out)
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("reading %s: %w", out, err)
			}
			src, err := os.ReadFile(in)
			if err != nil {
				return fmt.Errorf("reading %s: %w", in, err)
			}
			res, err := migrate.Convert(src, schemaVersion(), filepath.Dir(in))
			if err != nil {
				return err
			}
			binary, err := pkl.Find(ctx)
			if err != nil {
				return fmt.Errorf("finding pkl to check the file: %w", err)
			}
			problems, err := migrate.Check(ctx, pkl.Evaluator{Binary: binary}, res.Pkl, schemaVersion(), res.Stage)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, []byte(res.Pkl), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", out, err)
			}
			printMigration(cmd, out, append(res.Todos, problems...))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "where to write the koldstart.pkl, beside serverless.yml without it")
	cmd.Flags().BoolVar(&force, "force", false, "write over the koldstart.pkl when there is one")
	return cmd
}

func printMigration(cmd *cobra.Command, out string, todos []migrate.Todo) {
	w := cmd.OutOrStdout()
	if len(todos) == 0 {
		_, _ = fmt.Fprintf(w, "wrote %s, with everything converted\n", out)
		return
	}
	_, _ = fmt.Fprintf(w, "wrote %s; left to you:\n\n", out)
	for _, t := range todos {
		_, _ = fmt.Fprintf(w, "  %s\n", t)
	}
	_, _ = fmt.Fprintf(w, "\nthe TODO comments in %s say where each one belongs\n", out)
}
