// Package cli wires Sublime's subcommands. It is imported by cmd/sublime and
// exercised directly in tests, without needing to exec the built binary.
package cli

import (
	"github.com/spf13/cobra"
)

const defaultConfigPath = "/config/config.yaml"

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

	root.AddCommand(newLibrariesCommand(&configPath))

	return root
}
