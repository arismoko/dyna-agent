package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadEventsFromInitialAndAppend(t *testing.T) {
	initial := "{\"t\":\"run_start\",\"title\":\"first\"}\n"
	path := writeRunFile(t, "events.jsonl", initial)

	events, offset, err := ReadEventsFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("initial read: %v", err)
	}
	if len(events) != 1 || events[0].T != "run_start" || events[0].Title != "first" {
		t.Fatalf("initial events = %#v", events)
	}
	if want := int64(len(initial)); offset != want {
		t.Fatalf("initial offset = %d, want %d", offset, want)
	}

	appended := "{\"t\":\"log\",\"msg\":\"second\"}\n"
	appendRunFile(t, path, appended)
	events, next, err := ReadEventsFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("appended read: %v", err)
	}
	if len(events) != 1 || events[0].T != "log" || events[0].Msg != "second" {
		t.Fatalf("appended events = %#v", events)
	}
	if want := int64(len(initial) + len(appended)); next != want {
		t.Fatalf("appended offset = %d, want %d", next, want)
	}

	events, unchanged, err := ReadEventsFrom("wf_test", next)
	if err != nil {
		t.Fatalf("unchanged read: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("unchanged events = %#v, want none", events)
	}
	if unchanged != next {
		t.Fatalf("unchanged offset = %d, want %d", unchanged, next)
	}
}

func TestReadEventsFromRetainsPartialTrailingRecord(t *testing.T) {
	complete := "{\"t\":\"run_start\",\"title\":\"first\"}\n"
	partial := "{\"t\":\"phase\",\"title\":\"sec"
	path := writeRunFile(t, "events.jsonl", complete+partial)

	events, offset, err := ReadEventsFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("read with partial tail: %v", err)
	}
	if len(events) != 1 || events[0].Title != "first" {
		t.Fatalf("events = %#v", events)
	}
	if want := int64(len(complete)); offset != want {
		t.Fatalf("offset = %d, want committed boundary %d", offset, want)
	}

	remainder := "ond\"}\n"
	appendRunFile(t, path, remainder)
	events, next, err := ReadEventsFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("read completed tail: %v", err)
	}
	if len(events) != 1 || events[0].T != "phase" || events[0].Title != "second" {
		t.Fatalf("completed events = %#v", events)
	}
	if want := int64(len(complete + partial + remainder)); next != want {
		t.Fatalf("completed offset = %d, want %d", next, want)
	}
}

func TestReadEventsFromResetsAfterTruncation(t *testing.T) {
	old := "{\"t\":\"log\",\"msg\":\"" + strings.Repeat("old", 64) + "\"}\n"
	path := writeRunFile(t, "events.jsonl", old)

	_, offset, err := ReadEventsFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("read old file: %v", err)
	}

	replacement := "{\"t\":\"run_start\",\"title\":\"replacement\"}\n"
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	events, next, err := ReadEventsFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if len(events) != 1 || events[0].Title != "replacement" {
		t.Fatalf("replacement events = %#v", events)
	}
	if want := int64(len(replacement)); next != want {
		t.Fatalf("replacement offset = %d, want %d", next, want)
	}
}

func TestReadEventsFromSkipsCommittedMalformedLines(t *testing.T) {
	bad := "not json\n"
	good := "{\"t\":\"log\",\"msg\":\"valid\"}\n"
	partialBad := "{\"t\":\"log\""
	path := writeRunFile(t, "events.jsonl", bad+good+partialBad)

	events, offset, err := ReadEventsFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("read malformed records: %v", err)
	}
	if len(events) != 1 || events[0].Msg != "valid" {
		t.Fatalf("events = %#v", events)
	}
	if want := int64(len(bad + good)); offset != want {
		t.Fatalf("offset = %d, want %d", offset, want)
	}

	appendRunFile(t, path, "\n")
	events, next, err := ReadEventsFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("read committed malformed tail: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("malformed tail produced events: %#v", events)
	}
	if want := int64(len(bad + good + partialBad + "\n")); next != want {
		t.Fatalf("offset after malformed tail = %d, want %d", next, want)
	}
}

func TestReadJournalFromAndLegacyTrailingRecord(t *testing.T) {
	first := "{\"id\":1,\"label\":\"first\",\"profile\":\"p\",\"key\":\"k1\",\"prompt\":\"one\",\"result\":1}\n"
	path := writeRunFile(t, "journal.jsonl", first)

	entries, offset, err := ReadJournalFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("initial journal read: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != 1 || entries[0].Label != "first" {
		t.Fatalf("initial entries = %#v", entries)
	}

	second := "{\"id\":2,\"label\":\"second\",\"profile\":\"p\",\"key\":\"k2\",\"prompt\":\"two\",\"result\":2}"
	appendRunFile(t, path, second)
	entries, unchanged, err := ReadJournalFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("partial journal read: %v", err)
	}
	if len(entries) != 0 || unchanged != offset {
		t.Fatalf("partial journal read = (%#v, %d), want no entries and offset %d", entries, unchanged, offset)
	}

	// The original whole-file API intentionally keeps accepting a valid final
	// JSON record without a newline.
	all, err := ReadJournal("wf_test")
	if err != nil {
		t.Fatalf("legacy journal read: %v", err)
	}
	if len(all) != 2 || all[1].ID != 2 {
		t.Fatalf("legacy entries = %#v", all)
	}

	appendRunFile(t, path, "\n")
	entries, next, err := ReadJournalFrom("wf_test", offset)
	if err != nil {
		t.Fatalf("completed journal read: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != 2 || entries[0].Label != "second" {
		t.Fatalf("completed entries = %#v", entries)
	}
	if want := int64(len(first + second + "\n")); next != want {
		t.Fatalf("journal offset = %d, want %d", next, want)
	}
}

func TestReadEventsLegacyTrailingRecord(t *testing.T) {
	writeRunFile(t, "events.jsonl", "{\"t\":\"log\",\"msg\":\"no newline\"}")

	events, err := ReadEvents("wf_test")
	if err != nil {
		t.Fatalf("legacy event read: %v", err)
	}
	if len(events) != 1 || events[0].Msg != "no newline" {
		t.Fatalf("legacy events = %#v", events)
	}

	incremental, offset, err := ReadEventsFrom("wf_test", 0)
	if err != nil {
		t.Fatalf("incremental event read: %v", err)
	}
	if len(incremental) != 0 || offset != 0 {
		t.Fatalf("incremental events = (%#v, %d), want no committed records", incremental, offset)
	}
}

func TestIncrementalReadersMissingFiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if events, offset, err := ReadEventsFrom("wf_missing", 17); !os.IsNotExist(err) || events != nil || offset != 17 {
		t.Fatalf("missing events = (%#v, %d, %v)", events, offset, err)
	}
	if entries, offset, err := ReadJournalFrom("wf_missing", 23); !os.IsNotExist(err) || entries != nil || offset != 23 {
		t.Fatalf("missing journal = (%#v, %d, %v)", entries, offset, err)
	}
}

func TestReadJSONLinesFromSkipsOversizedCompleteRecord(t *testing.T) {
	oversized := `{"t":"log","msg":"` + strings.Repeat("x", 128) + `"}` + "\n"
	valid := "{\"t\":\"log\",\"msg\":\"after\"}\n"
	path := writeRunFile(t, "events.jsonl", oversized+valid)

	events, offset, err := readJSONLinesFrom[Event](path, 0, 64)
	if err != nil {
		t.Fatalf("read oversized record: %v", err)
	}
	if len(events) != 1 || events[0].Msg != "after" {
		t.Fatalf("events after oversized record = %#v", events)
	}
	if want := int64(len(oversized + valid)); offset != want {
		t.Fatalf("offset = %d, want %d", offset, want)
	}
}

func TestLegacyReadersRejectOversizedRecords(t *testing.T) {
	t.Run("journal", func(t *testing.T) {
		writeRunFile(t, "journal.jsonl", `{"result":"`+strings.Repeat("x", 16*1024*1024)+`"}`+"\n")
		if _, err := ReadJournal("wf_test"); err == nil {
			t.Fatal("ReadJournal() succeeded after scanner token limit")
		}
	})
	t.Run("events", func(t *testing.T) {
		writeRunFile(t, "events.jsonl", `{"msg":"`+strings.Repeat("x", 4*1024*1024)+`"}`+"\n")
		if _, err := ReadEvents("wf_test"); err == nil {
			t.Fatal("ReadEvents() succeeded after scanner token limit")
		}
	})
}

func writeRunFile(t *testing.T, name, contents string) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := filepath.Join(RunsDir(), "wf_test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func appendRunFile(t *testing.T, path, contents string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s for append: %v", filepath.Base(path), err)
	}
	if _, err := f.WriteString(contents); err != nil {
		f.Close()
		t.Fatalf("append %s: %v", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", filepath.Base(path), err)
	}
}
