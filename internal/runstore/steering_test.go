package runstore

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAgentSteeringBoundaryAndLifecycle(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-boundary")
	if err := SubmitAgentSteering(run.Meta.ID, 1, "too early"); err == nil || !strings.Contains(err.Error(), "does not support live steering") {
		t.Fatalf("submission before activation = %v", err)
	}
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		runID   string
		agentID int
		message string
		want    string
	}{
		{name: "run id", runID: "../wf_bad", agentID: 1, message: "x", want: "invalid run id"},
		{name: "agent id", runID: run.Meta.ID, agentID: 0, message: "x", want: "positive integer"},
		{name: "empty", runID: run.Meta.ID, agentID: 1, message: "  ", want: "must not be empty"},
		{name: "utf8", runID: run.Meta.ID, agentID: 1, message: string([]byte{0xff}), want: "valid UTF-8"},
		{name: "size", runID: run.Meta.ID, agentID: 1, message: strings.Repeat("x", MaxSteeringMessageBytes+1), want: "too long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SubmitAgentSteering(tt.runID, tt.agentID, tt.message); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SubmitAgentSteering() error = %v, want %q", err, tt.want)
			}
		})
	}

	if err := SubmitAgentSteering(run.Meta.ID, 1, "  inspect the parser first  "); err != nil {
		t.Fatal(err)
	}
	messages, offset, err := ReadAgentSteeringFrom(run.Meta.ID, 1, 0, false)
	if err != nil || len(messages) != 1 || messages[0].Message != "inspect the parser first" || messages[0].TS <= 0 {
		t.Fatalf("first read = %#v, offset %d, %v", messages, offset, err)
	}
	if messages, next, err := ReadAgentSteeringFrom(run.Meta.ID, 1, offset, true); err != nil || len(messages) != 0 || next != offset {
		t.Fatalf("final read = %#v, offset %d, %v", messages, next, err)
	}
	if err := SubmitAgentSteering(run.Meta.ID, 1, "after completion"); err == nil || !strings.Contains(err.Error(), "not an active steerable worker") {
		t.Fatalf("submission after deactivation = %v", err)
	}
}

func TestAgentSteeringConcurrentSubmissionsRemainWhole(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-concurrent")
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}

	const count = 24
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- SubmitAgentSteering(run.Meta.ID, 1, fmt.Sprintf("message-%02d", i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	messages, _, err := ReadAgentSteeringFrom(run.Meta.ID, 1, 0, false)
	if err != nil || len(messages) != count {
		t.Fatalf("messages = %d, err = %v", len(messages), err)
	}
	seen := make(map[string]bool, count)
	for _, message := range messages {
		seen[message.Message] = true
	}
	for i := 0; i < count; i++ {
		if !seen[fmt.Sprintf("message-%02d", i)] {
			t.Fatalf("missing concurrent message %02d", i)
		}
	}
}

func createSteeringTestRun(t *testing.T, id string) *Run {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", id)
	t.Setenv(AgentJournalRootEnv, "")
	run, err := Create("steering test", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "null", nil) })
	if _, err := run.StartAgentJournal(1, "worker", "test", "", "task"); err != nil {
		t.Fatal(err)
	}
	return run
}
