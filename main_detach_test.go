package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestDetachedRunSurvivesCallerScriptRemoval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process groups use Unix Setsid")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "dyna")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-buildvcs=false", "-o", bin, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build detached-run fixture: %v\n%s", err, output)
	}

	configDir := t.TempDir()
	dataDir := t.TempDir()
	store := &profile.Store{Path: filepath.Join(configDir, "dyna", "profiles.json"), Profiles: []profile.Profile{{
		Name: "fixture", Description: "fixture", Harness: profile.HarnessMock,
		Taste: 5, Intelligence: 5, Cost: 5, Default: true,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	callerScript := filepath.Join(t.TempDir(), "caller-workflow.js")
	if err := os.WriteFile(callerScript, []byte(`return {ok: true, source: "staged"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	env = setEnv(env, "XDG_CONFIG_HOME", configDir)
	env = setEnv(env, "XDG_DATA_HOME", dataDir)
	env = setEnv(env, "DYNA_NO_AUTO_UPDATE", "1")
	env = setEnv(env, "DYNA_SESSION", "detach-fixture")

	launchCtx, cancelLaunch := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelLaunch()
	launch := exec.CommandContext(launchCtx, bin, "run", callerScript, "--detach", "--json", "--quiet")
	launch.Env = env
	var stdout, stderr bytes.Buffer
	launch.Stdout, launch.Stderr = &stdout, &stderr
	if err := launch.Run(); err != nil {
		t.Fatalf("launch detached workflow: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(runID, "wf_") {
		t.Fatalf("detached run id = %q, stderr=%s", runID, stderr.String())
	}
	if err := os.Remove(callerScript); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(callerScript); !os.IsNotExist(err) {
		t.Fatalf("caller script still exists: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWait()
	wait := exec.CommandContext(waitCtx, bin, "runs", "wait", runID, "--timeout", "20")
	wait.Env = env
	result, err := wait.CombinedOutput()
	if err != nil {
		daemon, _ := os.ReadFile(filepath.Join(dataDir, "dyna", "runs", runID, "daemon.log"))
		t.Fatalf("wait for detached workflow: %v\n%s\ndaemon=%s", err, result, daemon)
	}
	if got := strings.TrimSpace(string(result)); got != `{"ok":true,"source":"staged"}` {
		t.Fatalf("detached result = %q", got)
	}
	runScript := filepath.Join(dataDir, "dyna", "runs", runID, "script.js")
	if got, err := os.ReadFile(runScript); err != nil || !bytes.Contains(got, []byte(`source: "staged"`)) {
		t.Fatalf("run-owned script = %q, err=%v", got, err)
	}
	var meta struct {
		Name string `json:"name"`
	}
	metaBytes, err := os.ReadFile(filepath.Join(dataDir, "dyna", "runs", runID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Name != "caller-workflow" {
		t.Fatalf("detached run name = %q, meta=%s", meta.Name, metaBytes)
	}
}
