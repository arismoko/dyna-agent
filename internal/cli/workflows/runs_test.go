package workflows

import (
	"bytes"
	"encoding/json"
	"testing"

	"dyna-agent/internal/runstore"
)

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

	cmd := NewRunsCommand()
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
