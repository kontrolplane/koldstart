package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

func upgradeCommand(name string, opts *Options) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "point the project's PklProject at this koldstart's schema",
		Long: fmt.Sprintf(`upgrade sets the koldstart dependency in the PklProject of the configuration, beside it or the
nearest above it, to the schema package of this release of %[1]s, resolves it into
PklProject.deps.json, and prints what changed, such as koldstart 0.1.0 → 0.2.0. Without a
PklProject it writes one beside the configuration. A dev build has no release, so it sets the
dependency to the schema it embeds, extracted into ~/.cache/koldstart.

--check changes nothing: it says whether the PklProject wants this koldstart's schema, and exits 1
when it does. A PklProject with a newer release than this koldstart's is refused without --force.`, name),
		Example: fmt.Sprintf(`  %[1]s upgrade
  %[1]s upgrade --check
  %[1]s upgrade --force --config services/orders/koldstart.pkl`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			want, err := schema.Current()
			if err != nil {
				return err
			}
			return upgrade(cmd.Context(), cmd.OutOrStdout(), pklResolver(name), upgradeOptions{
				name: name, config: opts.Config, want: want, check: check, force: force,
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "say whether an upgrade is wanted, and exit 1 when it is")
	cmd.Flags().BoolVar(&force, "force", false, "go back to this koldstart's release from a newer one")
	return cmd
}

// upgradeOptions are what upgrade does: to the PklProject of config, setting it to want.
type upgradeOptions struct {
	name   string
	config string
	want   schema.Dependency
	check  bool
	force  bool
}

// upgrade sets the koldstart dependency of the configuration's PklProject to o.want, and
// resolves it.
//
//nolint:gosec // reads and writes the project's PklProject, which everyone reads
func upgrade(ctx context.Context, w io.Writer, resolve resolver, o upgradeOptions) error {
	dir, err := pkl.FindProject(o.config)
	if err != nil {
		return err
	}
	if dir == "" {
		abs, err := filepath.Abs(o.config)
		if err != nil {
			return fmt.Errorf("finding %s: %w", o.config, err)
		}
		dir = filepath.Dir(abs)
	}
	path := filepath.Join(dir, pkl.ProjectFile)
	shownPath := relative(path)
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", shownPath, err)
	}
	var have *schema.Dependency
	if exists {
		d, err := schema.ReadDependency(string(data))
		switch {
		case err == nil:
			have = &d
		case !errors.Is(err, schema.ErrNoDependency):
			return fmt.Errorf("reading %s: %w: set it by hand to %s", shownPath, err, o.want.Entry())
		}
	}
	from := "none"
	if have != nil {
		from = have.String()
	}
	change := fmt.Sprintf("koldstart %s → %s", from, o.want)
	switch {
	case have != nil && sameDependency(*have, o.want, dir):
		stale, err := pkl.Stale(dir)
		if err != nil {
			return err
		}
		switch {
		case !stale:
			_, _ = fmt.Fprintf(w, "%s has koldstart %s, this koldstart's: nothing to upgrade\n", shownPath, o.want)
			return nil
		case o.check:
			return fmt.Errorf("%s has koldstart %s, this koldstart's, but %s is missing or older: run %s upgrade", shownPath, o.want, pkl.DepsFile, o.name)
		}
		if err := resolve(ctx, dir); err != nil {
			return fmt.Errorf("%s is set to koldstart %s, but not resolved: %w", shownPath, o.want, err)
		}
		_, _ = fmt.Fprintf(w, "resolved %s into %s\n", shownPath, pkl.DepsFile)
		return nil
	case have != nil && !have.Local() && !o.want.Local() && schema.Compare(have.Version, o.want.Version) > 0:
		if o.check {
			_, _ = fmt.Fprintf(w, "%s has koldstart %s, newer than this koldstart's %s: upgrade %s to %s\n", shownPath, have.Version, o.want.Version, o.name, have.Version)
			return nil
		}
		if !o.force {
			return fmt.Errorf("%s has koldstart %s, newer than this koldstart's %s: upgrade %s to %s, or pass --force to go back to %s", shownPath, have.Version, o.want.Version, o.name, have.Version, o.want.Version)
		}
	case o.check && !exists:
		return fmt.Errorf("there is no %s for %s: run %s upgrade to write one, with koldstart %s", pkl.ProjectFile, o.config, o.name, o.want)
	case o.check:
		return fmt.Errorf("%s wants an upgrade, %s: run %s upgrade", shownPath, change, o.name)
	}

	text := o.want.PklProject()
	if exists {
		if text, err = schema.SetDependency(string(data), o.want); err != nil {
			return fmt.Errorf("reading %s: %w", shownPath, err)
		}
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", shownPath, err)
	}
	_, _ = fmt.Fprintln(w, change)
	if err := resolve(ctx, dir); err != nil {
		return fmt.Errorf("%s is set to koldstart %s, but not resolved: %w", shownPath, o.want, err)
	}
	_, _ = fmt.Fprintf(w, "resolved %s into %s\n", shownPath, pkl.DepsFile)
	return nil
}

// sameDependency reports whether a and b are the same release, or the same local directory, a
// relative one taken from dir, the directory of the PklProject.
func sameDependency(a, b schema.Dependency, dir string) bool {
	if a.Local() != b.Local() {
		return false
	}
	if !a.Local() {
		return a.Version == b.Version
	}
	abs := func(d string) string {
		if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		return filepath.Clean(d)
	}
	return abs(a.Dir) == abs(b.Dir)
}
