// dyna: harness-agnostic dynamic multi-agent workflows.
// CLI for agents (codex, claude-code, opencode) + TUI for humans.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/interactive"
	"dyna-agent/internal/cli/journal"
	"dyna-agent/internal/cli/profiles"
	"dyna-agent/internal/cli/setup"
	"dyna-agent/internal/cli/updatecmd"
	versioncmd "dyna-agent/internal/cli/version"
	"dyna-agent/internal/cli/workflows"
	"dyna-agent/internal/profile"
)

func init() {
	if err := profile.SetBundledDefaults(profiles.BundledDefaults()); err != nil {
		panic(fmt.Sprintf("parse bundled profiles: %v", err))
	}
}

// version is stamped from the release tag with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(versioncmd.CommandExitCode(err))
	}
}

func newRootCommand() *cobra.Command {
	resolvedVersion := versioncmd.Resolve(version)
	root := &cobra.Command{
		Use:     "dyna",
		Short:   "Dynamic multi-agent workflows for any coding agent",
		Version: resolvedVersion,
		Long: "dyna runs JavaScript workflow scripts that orchestrate registered worker\n" +
			"profiles (claude-code, codex, opencode, custom CLIs). Agents use the CLI;\n" +
			"humans use `dyna tui` to configure profiles and watch runs live.",
		SilenceUsage: true,
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			return setup.MaybeOfferPostUpdateSetup(c, resolvedVersion)
		},
	}
	root.SetVersionTemplate("dyna {{.Version}}\n")
	root.AddCommand(
		profiles.NewCommand(),
		workflows.NewRunCommand(),
		workflows.NewRunsCommand(),
		journal.NewCommand(),
		interactive.NewGuideCommand(),
		interactive.NewTUICommand(version),
		workflows.NewDemoCommand(),
		skillCmd(),
		updatecmd.NewCommand(version),
		versioncmd.NewCommand(version),
		postUpdateApplyCmd(),
	)
	return root
}
