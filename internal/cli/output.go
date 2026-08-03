package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// writeJSONOutput pretty-prints resp as JSON for a subcommand's --json flag.
func writeJSONOutput(w io.Writer, resp any) error {
	encoded, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding JSON output: %w", err)
	}
	_, err = fmt.Fprintln(w, string(encoded))
	return err
}
