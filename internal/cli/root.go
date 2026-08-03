// Package cli wires Sublime's subcommands. It is imported by cmd/sublime and
// exercised directly in tests, without needing to exec the built binary.
package cli

import (
	"github.com/spf13/cobra"
)

const defaultConfigPath = "/config/config.yaml"

// defaultAPIAddr is the base URL CLI thin-client subcommands (status,
// reprocess, libraries) talk to when --api isn't given: the default
// address `serve` binds to.
const defaultAPIAddr = "http://localhost:8080"

// NewRootCommand builds Sublime's root cobra command with all subcommands
// attached.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "sublime",
		Short:        "Sublime watches media libraries and syncs subtitles for them",
		SilenceUsage: true,
	}

	var configPath string
	root.PersistentFlags().StringVar(&configPath, "config", defaultConfigPath, "path to config.yaml")

	var apiAddr string
	root.PersistentFlags().StringVar(&apiAddr, "api", defaultAPIAddr, "base URL of a running sublime serve daemon's HTTP API")

	root.AddCommand(newLibrariesCommand(&apiAddr))
	root.AddCommand(newStatusCommand(&apiAddr))
	root.AddCommand(newReprocessCommand(&apiAddr))
	root.AddCommand(newServeCommand(&configPath))

	return root
}
