//go:build windows

package pi

import (
	"os"
	"os/exec"
	"os/signal"
)

func runPiProcess(cmd *exec.Cmd) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case sig := <-signals:
			_ = cmd.Process.Signal(sig)
		case <-done:
		}
	}()
	return cmd.Wait()
}
