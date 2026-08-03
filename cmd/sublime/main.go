// Command sublime is Sublime's entrypoint binary.
package main

import (
	"os"

	"github.com/aaronkyriesenbach/sublime/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
