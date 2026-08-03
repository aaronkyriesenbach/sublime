package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aaronkyriesenbach/sublime/internal/api"
)

func newReprocessCommand(apiAddr *string) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "reprocess <path>",
		Short: "Force a file, directory, or library to be reprocessed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*apiAddr)

			var resp api.ReprocessResponse
			if err := client.post(cmd.Context(), "/reprocess", api.ReprocessRequest{Path: args[0]}, &resp); err != nil {
				return err
			}

			if jsonOutput {
				return writeJSONOutput(cmd.OutOrStdout(), resp)
			}

			_, err := fmt.Fprintf(cmd.OutOrStdout(), "accepted: reprocessing %s (poll `sublime status` for progress)\n", resp.Scope.Path)
			return err
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print the raw API JSON response")
	return cmd
}
