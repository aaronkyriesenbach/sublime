package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aaronkyriesenbach/sublime/internal/api"
)

func newLibrariesCommand(apiAddr *string) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "libraries",
		Short: "List the libraries registered with the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := newAPIClient(*apiAddr)

			var resp api.LibrariesResponse
			if err := client.get(cmd.Context(), "/libraries", nil, &resp); err != nil {
				return err
			}

			if jsonOutput {
				return writeJSONOutput(cmd.OutOrStdout(), resp)
			}
			return printLibraries(cmd.OutOrStdout(), resp.Libraries)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print the raw API JSON response")
	return cmd
}

func printLibraries(w io.Writer, libraries []api.LibraryEntry) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tPATH\tLANGUAGES\tPENDING\tIN PROGRESS\tSYNCED\tFAILED"); err != nil {
		return fmt.Errorf("writing library header: %w", err)
	}
	for _, lib := range libraries {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			lib.Name, lib.Path, strings.Join(lib.Languages, ", "),
			strconv.Itoa(lib.Pending), strconv.Itoa(lib.InProgress), strconv.Itoa(lib.Synced), strconv.Itoa(lib.Failed),
		); err != nil {
			return fmt.Errorf("writing library output: %w", err)
		}
	}
	return tw.Flush()
}
