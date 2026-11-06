package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
)

// The themes of --theme.
var themes = []string{"auto", "dark", "light"}

// UI runs the terminal interface on a console, in the theme --theme names. cli does not import
// the interface: main passes it to Root.
type UI func(ctx context.Context, c *Console, theme string) error

func uiCommand(name string, opts *Options, ui UI) *cobra.Command {
	var theme string
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "open the terminal interface",
		Long: `ui opens a terminal interface for every app and stage the account has deployed, in every region
with a state bucket. In a project's directory it opens on the project's stage, and can plan, deploy
and run its functions on this machine; any stage can be browsed, invoked, unlocked and removed.`,
		Example: fmt.Sprintf(`  %[1]s ui
  %[1]s ui --stage prod --theme light
  %[1]s ui --profile prod --region eu-west-1`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !slices.Contains(themes, theme) {
				return usage(cmd, fmt.Errorf("--theme %q: want auto, dark or light", theme))
			}
			c, err := openConsole(cmd.Context(), name, opts)
			if err != nil {
				return err
			}
			return ui(cmd.Context(), c, theme)
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "auto", "the colours: auto, dark or light")
	_ = cmd.RegisterFlagCompletionFunc("theme", cobra.FixedCompletions(themes, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
