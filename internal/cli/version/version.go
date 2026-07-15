package version

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func CommandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}
	return 1
}

func Resolve(stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			revision := setting.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
			return "dev+" + revision
		}
	}
	return "dev"
}

func NewCommand(stamped string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the installed dyna version",
		Run: func(c *cobra.Command, _ []string) {
			fmt.Fprintf(c.OutOrStdout(), "dyna %s\n", Resolve(stamped))
		},
	}
}
