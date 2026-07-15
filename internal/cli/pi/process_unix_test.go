//go:build !windows

package pi

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPiCmdPreservesChildExitCode(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nexit 7\n")
	cmd := piCommandSubprocess(t, binDir)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("dyna pi exit = %v, want 7", err)
	}
}

func TestPiCmdForwardsTerminationToChildProcessGroup(t *testing.T) {
	binDir := t.TempDir()
	childPID := filepath.Join(t.TempDir(), "child-pid")
	terminated := filepath.Join(t.TempDir(), "terminated")
	writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$CAPTURE_CHILD_PID\"\ntrap 'printf terminated > \"$CAPTURE_TERMINATED\"; exit 0' TERM HUP INT\nwhile :; do /bin/sleep 1; done\n")
	cmd := piCommandSubprocess(t, binDir)
	cmd.Env = append(cmd.Env, "CAPTURE_CHILD_PID="+childPID, "CAPTURE_TERMINATED="+terminated)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(childPID); err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("pi child did not publish its pid")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wrapper after forwarded SIGTERM: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, terminated)); got != "terminated" {
		t.Fatalf("pi termination marker = %q", got)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("pi child %d remains alive: %v", pid, err)
	}
}
