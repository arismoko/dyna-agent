package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestExecuteDeliversSteeringAndEmitsUpdate(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("DYNA_RUN_ID", "wf_engine-steering")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	claudePath := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
case " $* " in
  *" --resume "*)
    printf '%s\n' '{"ts":1,"kind":"verification","message":"Applied live steering.","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
    printf '%s\n' 'steered engine result'
    ;;
  *)
    trap 'exit 130' INT
    printf '%s\n' READY >> "$DYNA_FAKE_LOG"
    while :; do sleep 1; done
    ;;
esac
`
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workflow := `return await agent("original task", {profile: "steer", label: "live worker"});`
	run, err := runstore.Create("engine steering", workflow, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "", nil)
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "steer", Harness: profile.HarnessClaudeCode, Default: true,
		Taste: 5, Intelligence: 5, Cost: 5,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath},
	}}}
	type outcome struct {
		result string
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	workDir := t.TempDir()
	go func() {
		result, err := Execute(ctx, Options{ScriptSrc: workflow, Store: store, Run: run, WorkDir: workDir})
		done <- outcome{result: result, err: err}
	}()
	if !waitForEngineFile(logPath, "READY", 2*time.Second) {
		t.Fatal("engine worker did not become ready")
	}
	if err := runstore.SubmitAgentSteering(run.Meta.ID, 1, "focus on the decoder"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result != `"steered engine result"` {
			t.Fatalf("Execute() = %q, %v", got.result, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not finish after steering")
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundSteer, foundEnd := false, false
	for _, event := range events {
		if event.T == "agent_steer" && event.ID == 1 && event.Status == "delivered" && event.Msg == "focus on the decoder" {
			foundSteer = true
		}
		if event.T == "agent_end" && event.ID == 1 && event.Status == "ok" {
			foundEnd = true
		}
	}
	if !foundSteer || !foundEnd {
		t.Fatalf("events missing steering/end update: %#v", events)
	}
	entries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	if got := strings.Join(kinds, ","); got != "start,steer,verification,complete" {
		t.Fatalf("agent journal kinds = %s", got)
	}
}

func TestSteeringStartFailureRecordsDispatchWithoutDelivery(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("DYNA_RUN_ID", "wf_engine-steering-start-failure")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	claudePath := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
trap '/bin/rm -f "$0"; exit 130' INT
printf '%s\n' READY >> "$DYNA_FAKE_LOG"
while :; do /bin/sleep 1; done
`
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	workflow := `return await agent("original task", {profile: "steer", label: "live worker"});`
	run, err := runstore.Create("engine steering start failure", workflow, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("error", "", nil)
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "steer", Harness: profile.HarnessClaudeCode, Default: true,
		Taste: 5, Intelligence: 5, Cost: 5,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath},
	}}}
	type outcome struct {
		result string
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	workDir := t.TempDir()
	go func() {
		result, err := Execute(ctx, Options{ScriptSrc: workflow, Store: store, Run: run, WorkDir: workDir})
		done <- outcome{result: result, err: err}
	}()
	if !waitForEngineFile(logPath, "READY", 2*time.Second) {
		t.Fatal("engine worker did not become ready")
	}
	if err := runstore.SubmitAgentSteering(run.Meta.ID, 1, "focus before restart"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "executable file not found") {
			t.Fatalf("Execute() = %q, %v; want continuation start failure", got.result, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not return after the continuation failed to start")
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.T == "agent_steer" && event.Status == "delivered" {
			t.Fatalf("failed continuation was reported delivered: %#v", events)
		}
	}
	entries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	if got := strings.Join(kinds, ","); got != "start,steer,error" {
		t.Fatalf("agent journal kinds = %s, want dispatch before start error", got)
	}
	if got := strings.Count(readEngineFile(t, logPath), "CALL "); got != 1 {
		t.Fatalf("process invocation count = %d, want 1", got)
	}
}

func waitForEngineFile(path, substring string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), substring) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func readEngineFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
