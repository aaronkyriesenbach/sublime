package cli

import (
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aaronkyriesenbach/sublime/internal/api"
)

func newStatusCommand(apiAddr *string) *cobra.Command {
	var (
		library    string
		path       string
		state      string
		limit      int
		offset     int
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync status for libraries and files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := url.Values{}
			if library != "" {
				query.Set("library", library)
			}
			if path != "" {
				query.Set("path", path)
			}
			if state != "" {
				query.Set("state", state)
			}
			if limit != 0 {
				query.Set("limit", strconv.Itoa(limit))
			}
			if offset != 0 {
				query.Set("offset", strconv.Itoa(offset))
			}

			client := newAPIClient(*apiAddr)

			var resp api.StatusResponse
			if err := client.get(cmd.Context(), "/status", query, &resp); err != nil {
				return err
			}

			if jsonOutput {
				return writeJSONOutput(cmd.OutOrStdout(), resp)
			}
			return printStatus(cmd.OutOrStdout(), resp)
		},
	}

	cmd.Flags().StringVar(&library, "library", "", "scope to a single library by name")
	cmd.Flags().StringVar(&path, "path", "", "scope to a file or directory path")
	cmd.Flags().StringVar(&state, "state", "", `"all" to include synced files (default: pending/failed only)`)
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of files to return (scoped requests only)")
	cmd.Flags().IntVar(&offset, "offset", 0, "number of files to skip (scoped requests only)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print the raw API JSON response")

	return cmd
}

func printStatus(w io.Writer, resp api.StatusResponse) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "LIBRARY\tPENDING\tIN PROGRESS\tSYNCED\tFAILED"); err != nil {
		return fmt.Errorf("writing library summary header: %w", err)
	}
	for _, lib := range resp.Libraries {
		if _, err := fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", lib.Name, lib.Pending, lib.InProgress, lib.Synced, lib.Failed); err != nil {
			return fmt.Errorf("writing library summary: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if err := printSuspendedProviders(w, resp.Providers); err != nil {
		return err
	}

	if resp.Files == nil {
		return nil
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	ftw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(ftw, "PATH\tLANGUAGE\tSTATUS\tREASON\tATTEMPTED"); err != nil {
		return fmt.Errorf("writing file header: %w", err)
	}
	for _, f := range resp.Files {
		langs := make([]string, 0, len(f.Languages))
		for lang := range f.Languages {
			langs = append(langs, lang)
		}
		slices.Sort(langs)

		for _, lang := range langs {
			ls := f.Languages[lang]
			if _, err := fmt.Fprintf(ftw, "%s\t%s\t%s\t%s\t%s\n", f.Path, lang, ls.Status, ls.Reason, strings.Join(ls.Attempted, ",")); err != nil {
				return fmt.Errorf("writing file state: %w", err)
			}
		}
	}
	if err := ftw.Flush(); err != nil {
		return err
	}

	if resp.Total != nil {
		if _, err := fmt.Fprintf(w, "\n%d total (showing %d, offset %d)\n", *resp.Total, len(resp.Files), derefOrZero(resp.Offset)); err != nil {
			return err
		}
	}
	return nil
}

// printSuspendedProviders explains why pairs are waiting. Healthy Providers are
// omitted so the common case stays as terse as before.
func printSuspendedProviders(w io.Writer, providers []api.ProviderEntry) error {
	var suspended []api.ProviderEntry
	for _, p := range providers {
		if p.Suspended {
			suspended = append(suspended, p)
		}
	}
	if len(suspended) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	ptw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(ptw, "PROVIDER\tSUSPENDED UNTIL"); err != nil {
		return fmt.Errorf("writing provider header: %w", err)
	}
	for _, p := range suspended {
		until := "unknown"
		if p.ResumeAt != nil {
			until = p.ResumeAt.Local().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(ptw, "%s\t%s\n", p.Name, until); err != nil {
			return fmt.Errorf("writing provider state: %w", err)
		}
	}
	return ptw.Flush()
}

func derefOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
