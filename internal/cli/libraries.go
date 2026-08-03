package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

func newLibrariesCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "libraries",
		Short: "List the libraries registered in config.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			return printLibraries(cmd.OutOrStdout(), cfg.Libraries)
		},
	}
}

func printLibraries(w io.Writer, libraries []domain.Library) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, lib := range libraries {
		languages := make([]string, len(lib.Languages))
		for i, tag := range lib.Languages {
			languages[i] = tag.String()
		}

		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", lib.Name, lib.Path, strings.Join(languages, ", ")); err != nil {
			return fmt.Errorf("writing library output: %w", err)
		}
	}
	return tw.Flush()
}
