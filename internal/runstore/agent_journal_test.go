package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func createAgentJournalTestRun(t *testing.T) *Run {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_journal-test")
	run, err := Create("journal test", "return null", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { run.Finish("ok", "null", nil) })
	return run
}

func TestStartAgentJournalLayoutAndMetadata(t *testing.T) {
	run := createAgentJournalTestRun(t)
	prompt := "Inspect every relevant file.\nKeep the original prompt in full."
	before := time.Now().UnixMilli()
	path, err := run.StartAgentJournal(7, "read-only explorer", "terra", "Discovery", prompt)
	if err != nil {
		t.Fatalf("StartAgentJournal() error = %v", err)
	}
	wantPath, err := filepath.Abs(filepath.Join(run.Dir, "agents", "7", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath {
		t.Fatalf("journal path = %q, want %q", path, wantPath)
	}

	entries, offset, err := ReadAgentJournalFrom(run.Meta.ID, 7, 0)
	if err != nil {
		t.Fatalf("ReadAgentJournalFrom() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want one start entry", entries)
	}
	start := entries[0]
	if start.Kind != "start" || start.Message != "Agent started" || start.Source != "system" {
		t.Fatalf("start identity = %#v", start)
	}
	if start.AgentID != 7 || start.Label != "read-only explorer" || start.Profile != "terra" || start.Phase != "Discovery" || start.Prompt != prompt {
		t.Fatalf("start metadata = %#v", start)
	}
	if start.TS < before || start.TS > time.Now().UnixMilli() {
		t.Fatalf("start timestamp = %d, want current unix millis", start.TS)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if offset != info.Size() {
		t.Fatalf("offset = %d, want file size %d", offset, info.Size())
	}
	if _, err := run.StartAgentJournal(7, "duplicate", "terra", "", "prompt"); err == nil {
		t.Fatal("duplicate StartAgentJournal() succeeded")
	}

	if err := run.AppendAgentJournal(7, AgentJournalEntry{
		TS: 1, Kind: "nudge", Message: "Please record a progress update and continue.", Next: "Continue the original task", Source: "agent",
	}); err != nil {
		t.Fatalf("AppendAgentJournal() error = %v", err)
	}
	appended, _, err := ReadAgentJournalPathFrom(path, offset)
	if err != nil {
		t.Fatalf("read appended entry: %v", err)
	}
	if len(appended) != 1 || appended[0].Source != "system" || appended[0].TS <= 1 || appended[0].Kind != "nudge" {
		t.Fatalf("system append = %#v", appended)
	}
}

func TestAgentJournalConcurrentAppendsRemainWholeRecords(t *testing.T) {
	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(9, "parallel worker", "luna", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}

	const updates = 32
	var wg sync.WaitGroup
	errs := make(chan error, updates)
	for i := 0; i < updates; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AppendAgentJournalPath(path, AgentJournalEntry{
				Kind: "update", Message: fmt.Sprintf("parallel update %d", i), Source: "agent",
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent append error = %v", err)
		}
	}

	entries, _, err := ReadAgentJournalPathFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != updates+1 {
		t.Fatalf("entry count = %d, want %d whole JSONL records", len(entries), updates+1)
	}
	seen := make(map[string]bool, updates)
	for _, entry := range entries[1:] {
		seen[entry.Message] = true
	}
	for i := 0; i < updates; i++ {
		message := fmt.Sprintf("parallel update %d", i)
		if !seen[message] {
			t.Fatalf("missing %q from %#v", message, entries)
		}
	}
}

func TestReadAgentJournalPathFromCommitsOnlyCompleteRecords(t *testing.T) {
	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(3, "explorer", "luna", "", "full prompt")
	if err != nil {
		t.Fatal(err)
	}
	_, startOffset, err := ReadAgentJournalPathFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"ts":1,"kind":"update","message":"half then whole","source":"agent"`
	if _, err := f.WriteString(partial); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entries, unchanged, err := ReadAgentJournalPathFrom(path, startOffset)
	if err != nil {
		t.Fatalf("partial read error = %v", err)
	}
	if len(entries) != 0 || unchanged != startOffset {
		t.Fatalf("partial read = (%#v, %d), want no entries and offset %d", entries, unchanged, startOffset)
	}

	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("}\nnot-json\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := AppendAgentJournalPath(path, AgentJournalEntry{Kind: "decision", Message: "Use the complete-line reader", Source: "system"}); err != nil {
		t.Fatal(err)
	}

	entries, nextOffset, err := ReadAgentJournalPathFrom(path, startOffset)
	if err != nil {
		t.Fatalf("completed read error = %v", err)
	}
	if len(entries) != 2 || entries[0].Message != "half then whole" || entries[1].Kind != "decision" {
		t.Fatalf("completed entries = %#v", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if nextOffset != info.Size() {
		t.Fatalf("next offset = %d, want %d; malformed complete record should be committed", nextOffset, info.Size())
	}
}

func TestAppendAgentJournalFromEnvAndValidation(t *testing.T) {
	t.Setenv(AgentJournalEnv, "")
	if err := AppendAgentJournalFromEnv("update", "working", ""); err == nil || !strings.Contains(err.Error(), "only available inside a dyna worker") {
		t.Fatalf("outside-worker error = %v", err)
	}

	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(11, "worker", "sol", "Build", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentJournalEnv, path)

	tests := []struct {
		name, kind, message, next string
	}{
		{name: "empty kind", kind: "", message: "working"},
		{name: "invalid kind", kind: "Needs Review", message: "working"},
		{name: "empty message", kind: "update", message: " \n "},
		{name: "long message", kind: "update", message: strings.Repeat("x", agentJournalMaxMessageBytes+1)},
		{name: "long next", kind: "update", message: "working", next: strings.Repeat("x", agentJournalMaxMessageBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := AppendAgentJournalFromEnv(tt.kind, tt.message, tt.next); err == nil {
				t.Fatalf("AppendAgentJournalFromEnv(%q, %q, %q) succeeded", tt.kind, tt.message, tt.next)
			}
		})
	}

	before := time.Now().UnixMilli()
	if err := AppendAgentJournalFromEnv("decision", "  Keep JSONL append-only.  ", "  Add reader tests.  "); err != nil {
		t.Fatalf("AppendAgentJournalFromEnv() error = %v", err)
	}
	entries, _, err := ReadAgentJournalPathFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v, want start plus agent entry", entries)
	}
	entry := entries[1]
	if entry.Kind != "decision" || entry.Message != "Keep JSONL append-only." || entry.Next != "Add reader tests." || entry.Source != "agent" {
		t.Fatalf("agent entry = %#v", entry)
	}
	if entry.TS < before || entry.TS > time.Now().UnixMilli() {
		t.Fatalf("agent timestamp = %d, want current unix millis", entry.TS)
	}
}

func TestAgentJournalRootSurvivesWorkerXDGOverrideAndConfinesRun(t *testing.T) {
	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(12, "worker", "sol", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentJournalEnv, path)
	t.Setenv(AgentJournalRootEnv, run.Dir)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := AppendAgentJournalFromEnv("update", "profile XDG override did not redirect the journal", ""); err != nil {
		t.Fatalf("AppendAgentJournalFromEnv() with pinned root: %v", err)
	}
	entries, _, err := ReadAgentJournalPathFrom(path, 0)
	if err != nil || len(entries) != 2 || entries[1].Source != "agent" {
		t.Fatalf("pinned-root journal entries = %#v, %v", entries, err)
	}

	otherPath := filepath.Join(filepath.Dir(run.Dir), "wf_other", "agents", "12", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(otherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentJournalEnv, otherPath)
	if err := AppendAgentJournalFromEnv("update", "must stay in this run", ""); err == nil {
		t.Fatal("pinned journal root allowed a different run")
	}
}

func TestAgentJournalPathSafety(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"run traversal", func() error { _, err := AgentJournalPath("../wf_escape", 1); return err }},
		{"absolute run", func() error { _, err := AgentJournalPath("/tmp/wf_escape", 1); return err }},
		{"backslash traversal", func() error { _, err := AgentJournalPath(`wf_..\\escape`, 1); return err }},
		{"zero agent", func() error { _, err := AgentJournalPath("wf_safe", 0); return err }},
		{"negative agent", func() error { _, err := AgentJournalPath("wf_safe", -1); return err }},
		{"relative monitor path", func() error {
			_, _, err := ReadAgentJournalPathFrom("runs/wf_safe/agents/1/journal.jsonl", 9)
			return err
		}},
		{"wrong filename", func() error {
			_, _, err := ReadAgentJournalPathFrom(filepath.Join(t.TempDir(), "runs", "wf_safe", "agents", "1", "notes.jsonl"), 9)
			return err
		}},
		{"non-numeric agent", func() error {
			return AppendAgentJournalPath(filepath.Join(t.TempDir(), "runs", "wf_safe", "agents", "one", "journal.jsonl"), AgentJournalEntry{Kind: "update", Message: "x"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	if entries, offset, err := ReadAgentJournalFrom("wf_missing", 1, 23); !os.IsNotExist(err) || entries != nil || offset != 23 {
		t.Fatalf("missing agent journal = (%#v, %d, %v), want nil entries, preserved offset, not-exist error", entries, offset, err)
	}

	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(4, "worker", "terra", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendAgentJournalPath(path, AgentJournalEntry{Kind: "update", Message: "wrong id", AgentID: 5}); err == nil {
		t.Fatal("mismatched entry/path agent id accepted")
	}
	if entries, offset, err := ReadAgentJournalFrom("../wf_escape", 4, 23); err == nil || entries != nil || offset != 23 {
		t.Fatalf("unsafe run read = (%#v, %d, %v)", entries, offset, err)
	}
}

func TestCreateRejectsTraversingPresetRunID(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("DYNA_RUN_ID", "../wf_escape")
	if run, err := Create("unsafe", "return null", nil); err == nil {
		run.Finish("error", "", nil)
		t.Fatal("Create() accepted a traversing DYNA_RUN_ID")
	}
	if _, err := os.Stat(filepath.Join(dataHome, "wf_escape")); !os.IsNotExist(err) {
		t.Fatalf("traversing run directory was created: %v", err)
	}
}

func TestAgentJournalRejectsSymlinkTargets(t *testing.T) {
	run := createAgentJournalTestRun(t)
	path, err := run.StartAgentJournal(14, "worker", "terra", "", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	realPath := path + ".real"
	if err := os.Rename(path, realPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := AppendAgentJournalPath(path, AgentJournalEntry{Kind: "update", Message: "must not escape", Source: "agent"}); err == nil {
		t.Fatal("append followed a symlink journal")
	}
	if _, _, err := ReadAgentJournalPathFrom(path, 0); err == nil {
		t.Fatal("reader followed a symlink journal")
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "untouched\n" {
		t.Fatalf("outside symlink target changed: %q, %v", b, err)
	}
}

func TestStartAgentJournalRejectsSymlinkedAgentDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runDir := filepath.Join(RunsDir(), "wf_symlink-parent")
	if err := os.MkdirAll(filepath.Join(runDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(runDir, "agents", "1")); err != nil {
		t.Fatal(err)
	}
	run := &Run{Dir: runDir}
	if _, err := run.StartAgentJournal(1, "worker", "terra", "", "prompt"); err == nil {
		t.Fatal("StartAgentJournal followed a symlinked agent directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "journal.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("journal escaped through symlinked parent: %v", err)
	}
}
