package main

import (
	"github.com/spf13/cobra"

	piCLI "dyna-agent/internal/cli/pi"
)

func piCmd() *cobra.Command {
	return piCLI.NewCommand()
}
