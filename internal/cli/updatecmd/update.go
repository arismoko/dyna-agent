package updatecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/setup"
	"dyna-agent/internal/runstore"
	selfupdate "dyna-agent/internal/update"
)

func NewCommand(currentVersion string) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install the latest stable GitHub release",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg := Config(currentVersion)
			if check {
				result, err := cfg.Check(c.Context(), false)
				if err != nil {
					return err
				}
				printUpdateStatus(c.OutOrStdout(), result)
				return nil
			}
			result, err := cfg.Apply(c.Context(), force, false)
			if errors.Is(err, selfupdate.ErrDevelopmentBuild) {
				return fmt.Errorf("%w; release builds self-update, or use `dyna update --force` to replace this build", err)
			}
			if err != nil {
				return err
			}
			if !result.Updated {
				printUpdateStatus(c.OutOrStdout(), result)
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "updated dyna %s -> %s; running processes continue safely on %s\n", result.Current, result.Latest, result.Current)
			if err := RefreshInstalledSkills(c.Context(), result.Target, c.OutOrStdout()); err != nil {
				fmt.Fprintf(c.ErrOrStderr(), "warning: dyna updated, but skill refresh failed: %v\n", err)
			}
			setup.OfferSetupAfterUpdate(c, result.Target, result.Latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check the latest release without installing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace a development, equal, or newer local build")
	cmd.MarkFlagsMutuallyExclusive("check", "force")
	return cmd
}

func Config(currentVersion string) selfupdate.Config {
	return selfupdate.Config{
		Version:   releaseVersion(currentVersion),
		Repo:      selfupdate.DefaultRepo,
		StatePath: filepath.Join(runstore.DataDir(), "update-check.json"),
	}
}

func releaseVersion(currentVersion string) string {
	if currentVersion == "" || currentVersion == "dev" {
		return "dev"
	}
	return currentVersion
}

func printUpdateStatus(w io.Writer, result selfupdate.Result) {
	switch {
	case result.Latest == "":
		fmt.Fprintln(w, "no stable GitHub release is published yet")
	case result.Available:
		fmt.Fprintf(w, "dyna %s is available (installed: %s)\n", result.Latest, result.Current)
	case result.Current == "" || strings.HasPrefix(result.Current, "dev"):
		fmt.Fprintf(w, "latest stable release: %s (installed build: %s)\n", result.Latest, result.Current)
	default:
		fmt.Fprintf(w, "dyna %s is up to date\n", result.Current)
	}
}

func RefreshInstalledSkills(ctx context.Context, executable string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, "skill", "install")
	cmd.Env = append(os.Environ(), "DYNA_NO_AUTO_UPDATE=1")
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}
