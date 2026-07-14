package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateCapturesSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-captured")
	t.Setenv(SessionEnv, strings.Repeat("s", 160))
	run, err := Create("session", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	if got := run.Meta.Session; got != strings.Repeat("s", 128) {
		t.Fatalf("captured session = %q (len %d)", got, len(got))
	}
	stored, err := ReadMeta(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Session != run.Meta.Session {
		t.Fatalf("stored session = %q, want %q", stored.Session, run.Meta.Session)
	}
}

func TestCreateOmitsEmptySessionAndReadsOldMeta(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-empty")
	t.Setenv(SessionEnv, "")
	run, err := Create("no session", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	b, err := os.ReadFile(filepath.Join(run.Dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"session"`) {
		t.Fatalf("empty session was persisted: %s", b)
	}

	var old Meta
	if err := json.Unmarshal([]byte(`{"id":"wf_old","name":"old","status":"ok","startedAt":"2026-01-01T00:00:00Z"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Session != "" || old.StartedAt != (time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("old meta = %#v", old)
	}
}
