package main

import (
	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/setup"
	versioncmd "dyna-agent/internal/cli/version"
)

func postUpdateApplyCmd() *cobra.Command {
	return setup.NewPostUpdateApplyCommand(func() string { return versioncmd.Resolve(version) })
}
