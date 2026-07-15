package interactive

import (
	"strings"
	"testing"
)

func TestTuiCmdSessionFilterIsOptionalAndValidated(t *testing.T) {
	cmd := NewTUICommand("dev")
	flag := cmd.Flags().Lookup("session")
	if flag == nil || flag.DefValue != "" {
		t.Fatalf("tui session flag = %#v, want optional empty default", flag)
	}

	cmd.SetArgs([]string{"--session", ""})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid session filter") {
		t.Fatalf("empty explicit TUI session error = %v", err)
	}

	cmd = NewTUICommand("dev")
	cmd.SetArgs([]string{"--session", strings.Repeat("s", 129)})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "maximum 128 bytes") {
		t.Fatalf("oversized TUI session error = %v", err)
	}
}
