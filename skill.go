package main

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/setup"
)

func skillCmd() *cobra.Command {
	return setup.NewSkillCommand()
}
