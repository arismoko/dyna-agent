package interactive

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/updatecmd"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/tui"
)

//go:embed guide/GUIDE.md
var guideMD string

func GuideMarkdown() string {
	return guideMD
}

var stOK = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "35", Dark: "42"})

func NewGuideCommand() *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:   "guide",
		Short: "Print the workflow scripting guide (agents: read this first)",
		RunE: func(c *cobra.Command, _ []string) error {
			if plain || !isTTY() {
				fmt.Print(guideMD)
				return nil
			}
			out, err := glamour.Render(guideMD, "auto")
			if err != nil {
				fmt.Print(guideMD)
				return nil
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false, "raw markdown (default when piped)")
	return cmd
}

func NewTUICommand(currentVersion string) *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the dashboard: configure profiles, watch workflows live",
		RunE: func(c *cobra.Command, _ []string) error {
			if c.Flags().Changed("session") {
				if err := runstore.ValidateSessionID(session); err != nil {
					return fmt.Errorf("invalid session filter: %w", err)
				}
			}
			maybeAutoUpdateForTUI(c, currentVersion)
			return tui.Run(guideMD, session)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "only view and manage runs owned by this session")
	return cmd
}

func maybeAutoUpdateForTUI(c *cobra.Command, currentVersion string) {
	if os.Getenv("DYNA_NO_AUTO_UPDATE") == "1" || !isTTY() {
		return
	}
	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Minute)
	defer cancel()
	result, err := updatecmd.Config(currentVersion).Apply(ctx, false, true)
	if err != nil || !result.Updated {
		return // Automatic checks are best effort and never block normal use on errors.
	}
	fmt.Fprintln(c.ErrOrStderr(), stOK.Render(fmt.Sprintf("updated dyna %s -> %s; this TUI session will finish on %s", result.Current, result.Latest, result.Current)))
	_ = updatecmd.RefreshInstalledSkills(ctx, result.Target, io.Discard)
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
