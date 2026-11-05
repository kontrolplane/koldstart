package cli

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
)

func packageCommand(name string, opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "package",
		Short: "build and zip every function, without deploying",
		Long: `package builds every function the way deploy does, into .koldstart/build/<stage>/<function>.zip,
and prints the size and hash of each zip.`,
		Example: fmt.Sprintf(`  %[1]s package
  %[1]s package --stage prod`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadProject(cmd.Context(), opts)
			if err != nil {
				return err
			}
			b := build.Builder{Root: p.Root, Stage: p.Stage}
			var archives []build.Archive
			for _, fn := range slices.Sorted(maps.Keys(p.Functions)) {
				arc, err := b.Build(cmd.Context(), fn, p.Functions[fn])
				if err != nil {
					return err
				}
				archives = append(archives, arc)
			}
			return printArchives(cmd.OutOrStdout(), p.Root, archives)
		},
	}
}

func printArchives(w io.Writer, root string, archives []build.Archive) error {
	if len(archives) == 0 {
		_, err := fmt.Fprintln(w, "no functions to build")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FUNCTION\tSIZE\tSHA256\tZIP")
	for _, a := range archives {
		if a.Image {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Function, "-", a.SHA256[:12], "container image, built on deploy")
			continue
		}
		rel, err := filepath.Rel(root, a.Path)
		if err != nil {
			rel = a.Path
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Function, humanSize(a.Size), a.SHA256[:12], rel)
	}
	return tw.Flush()
}

// humanSize formats a byte count the way ls -h does.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
