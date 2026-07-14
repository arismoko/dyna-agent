package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
)

func TestPiCmdRegistered(t *testing.T) {
	cmd, _, err := newRootCommand().Find([]string{"pi"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Name() != "pi" {
		t.Fatalf("pi command = %#v", cmd)
	}
	if !cmd.DisableFlagParsing {
		t.Fatal("pi command parses passthrough flags")
	}
}

func TestPiCmdMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cmd := piCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "pi is not installed") {
		t.Fatalf("pi command error = %v", err)
	}
}

func TestPiCmdLaunchesWithExtensionSessionAndArgs(t *testing.T) {
	binDir := t.TempDir()
	home := t.TempDir()
	data := t.TempDir()
	capture := t.TempDir()
	piPath := filepath.Join(binDir, "pi")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$DYNA_SESSION\" > \"$CAPTURE_SESSION\"\nprintf '%s\\n' \"$DYNA_BIN\" > \"$CAPTURE_BIN\"\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("CAPTURE_ARGS", filepath.Join(capture, "args"))
	t.Setenv("CAPTURE_SESSION", filepath.Join(capture, "session"))
	t.Setenv("CAPTURE_BIN", filepath.Join(capture, "bin"))
	t.Setenv(runstore.SessionEnv, "stale-session")
	t.Setenv("DYNA_BIN", "stale-binary")
	t.Setenv("DYNA_NO_AUTO_UPDATE", "1")

	cmd := newRootCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"pi", "--model", "test-model", "-c"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	args := readLines(t, filepath.Join(capture, "args"))
	wantExtension := filepath.Join(data, "dyna", "pi-extension", "dyna.ts")
	wantArgs := []string{"--extension", wantExtension, "--model", "test-model", "-c"}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("pi args = %#v, want %#v", args, wantArgs)
	}
	session := strings.TrimSpace(readFile(t, filepath.Join(capture, "session")))
	if !regexp.MustCompile(`^pisess_[0-9a-f]{16}$`).MatchString(session) {
		t.Fatalf("DYNA_SESSION = %q", session)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "bin"))); got == "" || got == "stale-binary" {
		t.Fatalf("DYNA_BIN = %q", got)
	}
	if got := readFile(t, filepath.Join(home, ".pi", "agent", "skills", "dyna", "SKILL.md")); got != skillFrontmatter+skillBody {
		t.Fatal("pi skill was not installed with the current bundled content")
	}
}

func TestPiCommandSubprocess(t *testing.T) {
	if os.Getenv("GO_PI_COMMAND_HELPER") != "1" {
		return
	}
	cmd := newRootCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"pi"})
	if err := cmd.Execute(); err != nil {
		os.Exit(commandExitCode(err))
	}
	os.Exit(0)
}

func TestProvisionPiExtension(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(piExtensionTS) || !strings.Contains(got, "export default") {
		t.Fatal("provisioned extension does not match the embedded TypeScript")
	}

	old := time.Unix(946684800, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionPiExtension(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("unchanged extension was rewritten: info=%v err=%v", info, err)
	}

	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionPiExtension(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(piExtensionTS) {
		t.Fatal("stale extension was not refreshed")
	}
}

func TestRunsListSessionFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-one")
	t.Setenv(runstore.SessionEnv, "s1")
	first, err := runstore.Create("first", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Finish("ok", "1", nil)

	t.Setenv("DYNA_RUN_ID", "wf_session-none")
	t.Setenv(runstore.SessionEnv, "")
	second, err := runstore.Create("second", "return 2", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Finish("ok", "2", nil)

	cmd := runsCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"list", "--json", "--session", "s1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var runs []runstore.Meta
	if err := json.Unmarshal(stdout.Bytes(), &runs); err != nil {
		t.Fatalf("decode filtered list: %v\n%s", err, stdout.String())
	}
	if len(runs) != 1 || runs[0].ID != first.Meta.ID || runs[0].Session != "s1" {
		t.Fatalf("filtered runs = %#v", runs)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func piCommandSubprocess(t *testing.T, binDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPiCommandSubprocess$")
	cmd.Env = append(os.Environ(),
		"GO_PI_COMMAND_HELPER=1",
		"PATH="+binDir,
		"HOME="+t.TempDir(),
		"XDG_DATA_HOME="+t.TempDir(),
		"DYNA_NO_AUTO_UPDATE=1",
	)
	return cmd
}
