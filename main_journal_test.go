package main

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestJournalCmdOutsideWorker(t *testing.T) {
	t.Setenv(runstore.AgentJournalEnv, "")
	cmd := journalCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"Made progress"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "only available inside a dyna worker") {
		t.Fatalf("journal command error = %v", err)
	}
}

func TestJournalCmdAppendsQuietlyWithDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_cli-journal")
	run, err := runstore.Create("cli journal", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "null", nil) })
	path, err := run.StartAgentJournal(2, "cli worker", "terra", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runstore.AgentJournalEnv, path)

	cmd := journalCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"Mapped the storage contract", "--next", "Run the tests"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("journal command error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("journal command output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	entries, _, err := runstore.ReadAgentJournalPathFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Kind != "update" || entries[1].Message != "Mapped the storage contract" || entries[1].Next != "Run the tests" || entries[1].Source != "agent" {
		t.Fatalf("journal entries = %#v", entries)
	}
}
