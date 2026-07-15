//go:build !windows

package pi

import (
	"os/exec"
	"syscall"
)

// runPiProcess replaces the dyna process with pi. An interactive TUI must own
// the terminal's foreground process group; exec keeps the existing one, and
// signals, exit codes, and job control all behave as if pi were launched
// directly. It only returns on failure.
func runPiProcess(cmd *exec.Cmd) error {
	return syscall.Exec(cmd.Path, cmd.Args, cmd.Env)
}
