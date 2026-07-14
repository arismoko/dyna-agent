package main

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestRunsSteerCommandQueuesValidatedMessage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_cli-steering")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	run, err := runstore.Create("cli steering", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	if _, err := run.StartAgentJournal(3, "worker", "test", "", "task"); err != nil {
		t.Fatal(err)
	}
	if err := runstore.ActivateAgentSteering(run.Meta.ID, 3); err != nil {
		t.Fatal(err)
	}

	cmd := runsCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"steer", run.Meta.ID, "3", "inspect", "the parser first"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "queued steering") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	messages, _, err := runstore.ReadAgentSteeringFrom(run.Meta.ID, 3, 0, false)
	if err != nil || len(messages) != 1 || messages[0].Message != "inspect the parser first" {
		t.Fatalf("messages = %#v, %v", messages, err)
	}

	bad := runsCmd()
	bad.SetArgs([]string{"steer", run.Meta.ID, "not-a-number", "message"})
	if err := bad.Execute(); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("invalid agent id error = %v", err)
	}
}
