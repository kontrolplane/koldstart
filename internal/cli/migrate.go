package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/migrate"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

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
it. What it cannot convert, or converts to something that works differently, is left in the file
as a TODO comment where it belongs, and listed at the end with what to fix first. The file is
checked with pkl before it is written, so it is valid even where it is not complete.

Beside it goes a PklProject that names the schema, as init writes, resolved into
PklProject.deps.json. A PklProject that is there already is kept, the koldstart dependency added
when it has none.`,
		Example: fmt.Sprintf(`  %[1]s migrate
  %[1]s migrate path/to/serverless.yml --out koldstart.pkl
  %[1]s migrate --force`, name),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: fileArgument("yml", "yaml"),
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
			if errors.Is(err, fs.ErrNotExist) && len(args) == 0 {
				return errors.New("no serverless.yml here: run it in the project's directory, or pass the path to one")
			}
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s does not exist: pass the path to a serverless.yml", in)
			}
			if err != nil {
				return fmt.Errorf("reading %s: %w", in, err)
			}
			res, err := migrate.Convert(src, filepath.Dir(in), migrate.WithName(name))
			if err != nil {
				return err
			}
			binary, err := pkl.Find(ctx)
			if err != nil {
				return fmt.Errorf("finding pkl to check the file: %w", err)
			}
			problems, err := migrate.Check(ctx, pkl.Evaluator{Binary: binary, Name: name}, res.Pkl, res.Stage)
			if err != nil {
				return err
			}
			dep, err := schema.Current()
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, []byte(res.Pkl), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", out, err)
			}
			dir := filepath.Dir(out)
			written, err := schema.WriteProject(dir, dep)
			if err != nil {
				return err
			}
			resolved, note := resolveWritten(ctx, pklResolver(name), name, dir)
			w := cmd.OutOrStdout()
			printProject(w, filepath.Join(dir, pkl.ProjectFile), dep, written, resolved, note)
			printMigration(w, name, out, res.Stage, append(res.Todos, problems...))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "the file to write, beside serverless.yml by default")
	cmd.Flags().BoolVar(&force, "force", false, "write over a koldstart.pkl that is there")
	_ = cmd.MarkFlagFilename("out", "pkl")
	return cmd
}

// printProject says whether migrate wrote the PklProject at path or kept the one there, the
// schema it names, and whether it is resolved.
func printProject(w io.Writer, path string, dep schema.Dependency, written, resolved bool, note string) {
	if written {
		_, _ = fmt.Fprintf(w, "wrote %s, with the schema of koldstart %s\n", path, dep.Name())
	} else {
		_, _ = fmt.Fprintf(w, "kept %s, which names the schema already\n", path)
	}
	if resolved {
		_, _ = fmt.Fprintf(w, "resolved it into %s\n", pkl.DepsFile)
	}
	if note != "" {
		_, _ = fmt.Fprintln(w, note)
	}
}

// printMigration says where the koldstart.pkl was written, how many todos were left to the person
// and what they are, by kind, and the plan to run next: in the file's directory, with the stage
// serverless.yml deployed to.
func printMigration(w io.Writer, name, out, stage string, todos []migrate.Todo) {
	switch len(todos) {
	case 0:
		_, _ = fmt.Fprintf(w, "wrote %s, with everything converted\n", out)
	case 1:
		_, _ = fmt.Fprintf(w, "wrote %s, with 1 thing left to you\n\n", out)
	default:
		_, _ = fmt.Fprintf(w, "wrote %s, with %d things left to you\n\n", out, len(todos))
	}
	if len(todos) > 0 {
		_ = migrate.WriteTodos(w, todos)
	}
	marked := 0
	for _, t := range todos {
		if t.Marked {
			marked++
		}
	}
	switch {
	case marked == 0:
	case marked == len(todos):
		_, _ = fmt.Fprintf(w, "\na TODO comment in %s marks where each goes\n", out)
	default:
		_, _ = fmt.Fprintf(w, "\nTODO comments in %s mark where %d of them go\n", out, marked)
	}
	plan := name + " plan"
	if base := filepath.Base(out); base != "koldstart.pkl" {
		plan += " --config " + base
	}
	if stage != "" && stage != "dev" && !strings.Contains(stage, "${") {
		plan += " --stage " + stage
	}
	_, _ = fmt.Fprintln(w, "\nnext:")
	if dir := filepath.Dir(out); dir != "." {
		_, _ = fmt.Fprintf(w, "  cd %s\n", dir)
	}
	_, _ = fmt.Fprintf(w, "  %s\n", plan)
}
