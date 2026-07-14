package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestSteeringInterruptsAndResumesExactPiSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_harness-steering")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	run, err := runstore.Create("harness steering", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	if _, err := run.StartAgentJournal(1, "pi worker", "pi-test", "Test", "original task"); err != nil {
		t.Fatal(err)
	}

	logPath := installFakeCLI(t, "pi", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
case " $* " in
  *" --session "*)
    printf '%s\n' 'steered exact-session result'
    ;;
  *)
    trap 'exit 130' INT
    printf '%s\n' READY >> "$DYNA_FAKE_LOG"
    while :; do sleep 1; done
    ;;
esac
`)
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	var delivered []runstore.SteeringMessage
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() {
		result, err := RunWithJournalAndSteering(ctx, profile.Profile{
			Name: "pi-test", Harness: profile.HarnessPi,
			Env: map[string]string{"DYNA_FAKE_LOG": logPath},
		}, "original task", t.TempDir(), true, JournalOptions{}, SteeringOptions{
			RunID: run.Meta.ID, AgentID: 1,
			OnMessage: func(message runstore.SteeringMessage) { delivered = append(delivered, message) },
		})
		done <- outcome{result: result, err: err}
	}()
	if !waitForTestFile(logPath, "READY", 2*time.Second) {
		t.Fatal("initial pi session did not start")
	}
	if err := runstore.SubmitAgentSteering(run.Meta.ID, 1, "prioritize the parser boundary"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil || got.result.Output != "steered exact-session result" {
			t.Fatalf("steered result = %#v, %v", got.result, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("steered worker did not resume")
	}
	if len(delivered) != 1 || delivered[0].Message != "prioritize the parser boundary" {
		t.Fatalf("delivered messages = %#v", delivered)
	}
	log := readTestFile(t, logPath)
	assertSameAssignedSession(t, log, "--session-id", "--session")
	if !strings.Contains(log, "[DYNA STEERING]") || !strings.Contains(log, "prioritize the parser boundary") || strings.Contains(log, sessionNudgePrompt) {
		t.Fatalf("steering prompt was not isolated from recovery:\n%s", log)
	}
	if err := runstore.SubmitAgentSteering(run.Meta.ID, 1, "too late"); err == nil {
		t.Fatal("completed worker still accepted steering")
	}
}

func TestJournalInactivityNudgesExactCodexSession(t *testing.T) {
	journalPath := createHarnessJournal(t, 1)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
input=$(cat)
printf 'INPUT %s\n' "$input" >> "$DYNA_FAKE_LOG"
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*)
    printf '%s\n' '{"ts":1,"kind":"finding","message":"The reminder reached the original session.","next":"Finish the original task.","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
    printf '%s' 'finished after journal reminder' > "$out"
    ;;
  *)
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-journal-exact"}'
    sleep 30
    ;;
esac
`)

	var nudges []JournalNudge
	var entries []runstore.AgentJournalEntry
	journal := JournalOptions{
		Path:      journalPath,
		IdleAfter: 40 * time.Millisecond,
		OnNudge:   func(nudge JournalNudge) { nudges = append(nudges, nudge) },
		OnEntry:   func(entry runstore.AgentJournalEntry) { entries = append(entries, entry) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p := profile.Profile{
		Name: "journal-codex", Harness: profile.HarnessCodex,
		Env: map[string]string{
			"DYNA_FAKE_LOG":          logPath,
			runstore.AgentJournalEnv: journalPath,
		},
	}

	result, err := RunWithJournal(ctx, p, "inspect quietly", t.TempDir(), true, journal)
	if err != nil {
		t.Fatalf("RunWithJournal() error = %v", err)
	}
	if result.Output != "finished after journal reminder" {
		t.Fatalf("output = %q", result.Output)
	}
	if result.JournalNudges != 1 {
		t.Fatalf("JournalNudges = %d, want 1", result.JournalNudges)
	}
	if result.Nudged {
		t.Fatalf("Nudged = true for a journal reminder; transient-recovery state must remain separate: %#v", result)
	}
	if len(nudges) != 1 || !nudges[0].Delivered || nudges[0].Reason != JournalNudgeIdle {
		t.Fatalf("OnNudge deliveries = %#v, want one delivered idle reminder", nudges)
	}
	if len(entries) != 1 || entries[0].Source != "agent" || entries[0].Kind != "finding" || entries[0].Message == "" {
		t.Fatalf("OnEntry entries = %#v, want one valid agent finding", entries)
	}

	log := readTestFile(t, logPath)
	if got := strings.Count(log, "CALL "); got != 2 {
		t.Fatalf("invocation count = %d, want initial plus exact-session continuation\n%s", got, log)
	}
	if !strings.Contains(log, "CALL exec resume") || !strings.Contains(log, "thread-journal-exact -") {
		t.Fatalf("journal continuation did not target the exact Codex thread:\n%s", log)
	}
	if strings.Count(log, "INPUT "+journalNudgePrompt) != 1 {
		t.Fatalf("journal reminder prompt count != 1:\n%s", log)
	}
	if strings.Contains(log, "INPUT "+sessionNudgePrompt) {
		t.Fatalf("journal inactivity was mislabeled as transient recovery:\n%s", log)
	}
}

func TestJournalWaitsForLateExactSessionIDThenNudges(t *testing.T) {
	journalPath := createHarnessJournal(t, 38)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*)
    printf '%s\n' '{"ts":1,"kind":"finding","message":"late session id still received its reminder","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
    printf '%s' 'late id resumed' > "$out"
    ;;
  *)
    sleep 0.08
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-late-journal"}'
    sleep 30
    ;;
esac
`)
	var nudges []JournalNudge
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := RunWithJournal(ctx, profile.Profile{
		Name: "late-id", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}, "wait for session", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: 30 * time.Millisecond,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	})
	if err != nil || result.Output != "late id resumed" {
		t.Fatalf("RunWithJournal() = %#v, %v", result, err)
	}
	if len(nudges) != 1 || !nudges[0].Delivered || nudges[0].Reason != JournalNudgeIdle {
		t.Fatalf("late session reminder = %#v", nudges)
	}
	if log := readTestFile(t, logPath); strings.Count(log, "CALL ") != 2 || !strings.Contains(log, "thread-late-journal -") {
		t.Fatalf("late exact session was not resumed:\n%s", log)
	}
}

func TestFastWorkerWithoutEntryGetsExactSessionJournalReminder(t *testing.T) {
	journalPath := createHarnessJournal(t, 32)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
input=$(cat)
printf 'INPUT %s\n' "$input" >> "$DYNA_FAKE_LOG"
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*)
    printf '%s\n' '{"ts":1,"kind":"verification","message":"Recorded the completed check.","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
    printf '%s' 'replacement output must not escape' > "$out"
    ;;
  *)
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-fast-journal"}'
    printf '%s' 'original successful result' > "$out"
    ;;
esac
`)

	var nudges []JournalNudge
	result, err := RunWithJournal(context.Background(), profile.Profile{
		Name: "fast-codex", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}, "finish quickly", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: time.Minute,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	})
	if err != nil {
		t.Fatalf("RunWithJournal() error = %v", err)
	}
	if result.Output != "original successful result" || result.JournalNudges != 1 {
		t.Fatalf("result = %#v, want preserved original output and one reminder", result)
	}
	if len(nudges) != 1 || !nudges[0].Delivered || nudges[0].Reason != JournalNudgeMissing {
		t.Fatalf("nudges = %#v, want one delivered missing-entry reminder", nudges)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "CALL ") != 2 || !strings.Contains(log, "thread-fast-journal -") || !strings.Contains(log, "INPUT "+journalCompletionPrompt) {
		t.Fatalf("missing-entry continuation was not delivered to the exact session:\n%s", log)
	}
}

func TestCompletionJournalReminderIsBoundedAndPreservesResult(t *testing.T) {
	journalPath := createHarnessJournal(t, 39)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*) sleep 30 ;;
  *)
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-bounded-completion"}'
    printf '%s' 'original bounded result' > "$out"
    ;;
esac
`)
	var nudges []JournalNudge
	started := time.Now()
	result, err := RunWithJournal(context.Background(), profile.Profile{
		Name: "bounded-completion", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}, "finish quickly", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: time.Minute, CompletionTimeout: 50 * time.Millisecond,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	})
	if err != nil || result.Output != "original bounded result" || time.Since(started) > time.Second {
		t.Fatalf("bounded completion reminder = %#v, %v after %s", result, err, time.Since(started))
	}
	if len(nudges) != 2 || !nudges[0].Delivered || nudges[1].Delivered || nudges[1].Reason != JournalNudgeMissing {
		t.Fatalf("bounded ignored reminder outcomes = %#v", nudges)
	}
}

func TestCancellationDuringCompletionJournalReminderWins(t *testing.T) {
	journalPath := createHarnessJournal(t, 40)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*) printf '%s\n' REMINDER_READY >> "$DYNA_FAKE_LOG"; sleep 30 ;;
  *)
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-cancel-completion"}'
    printf '%s' 'original before cancellation' > "$out"
    ;;
esac
`)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := RunWithJournal(ctx, profile.Profile{
			Name: "cancel-completion", Harness: profile.HarnessCodex,
			Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
		}, "quick task", t.TempDir(), true, JournalOptions{
			Path: journalPath, IdleAfter: time.Minute, CompletionTimeout: time.Minute,
		})
		done <- outcome{result: result, err: err}
	}()
	if !waitForTestFile(logPath, "REMINDER_READY", 2*time.Second) {
		cancel()
		<-done
		t.Fatal("completion reminder did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "canceled/timed out") {
			t.Fatalf("cancellation was masked by original result: %#v, %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion reminder ignored parent cancellation")
	}
}

func TestJournalStateAllowsOnlyOneCompletionReminderAcrossTurns(t *testing.T) {
	journalPath := createHarnessJournal(t, 41)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
printf '%s\n' '{"type":"thread.started","thread_id":"thread-shared-completion"}'
printf '%s' 'schema turn output' > "$out"
`)
	p := profile.Profile{
		Name: "shared-completion", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}
	state := NewJournalState()
	opts := JournalOptions{Path: journalPath, IdleAfter: time.Minute, State: state}
	for i := 0; i < 2; i++ {
		if result, err := RunWithJournal(context.Background(), p, "schema turn", t.TempDir(), true, opts); err != nil || result.Output != "schema turn output" {
			t.Fatalf("turn %d = %#v, %v", i+1, result, err)
		}
	}
	if calls := strings.Count(readTestFile(t, logPath), "CALL "); calls != 3 {
		t.Fatalf("invocations = %d, want first turn + one reminder + second turn", calls)
	}
}

func TestJournalInterruptAllowsSessionFlushBeforeResume(t *testing.T) {
	journalPath := createHarnessJournal(t, 33)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
input=$(cat)
printf 'INPUT %s\n' "$input" >> "$DYNA_FAKE_LOG"
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
case " $* " in
  *" resume "*)
    grep -q FLUSHED "$DYNA_FAKE_LOG"
    printf '%s\n' '{"ts":1,"kind":"update","message":"Resumed after a graceful flush.","source":"agent"}' >> "$DYNA_AGENT_JOURNAL"
    printf '%s' 'finished after graceful reminder' > "$out"
    ;;
  *)
    trap 'printf "%s\n" FLUSHED >> "$DYNA_FAKE_LOG"; exit 130' INT
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-graceful-journal"}'
    printf '%s\n' READY >> "$DYNA_FAKE_LOG"
    while :; do sleep 1 || true; done
    ;;
esac
`)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := RunWithJournal(ctx, profile.Profile{
		Name: "graceful-codex", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}, "long inspection", t.TempDir(), true, JournalOptions{Path: journalPath, IdleAfter: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("RunWithJournal() error = %v", err)
	}
	if result.Output != "finished after graceful reminder" || result.JournalNudges != 1 {
		t.Fatalf("result = %#v", result)
	}
	log := readTestFile(t, logPath)
	if strings.Index(log, "FLUSHED") < 0 || strings.Index(log, "FLUSHED") > strings.LastIndex(log, "CALL ") {
		t.Fatalf("session was not flushed before exact-session resume:\n%s", log)
	}
}

func TestCancellationDuringJournalGraceNeverResumes(t *testing.T) {
	journalPath := createHarnessJournal(t, 37)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
case " $* " in
  *" resume "*) printf '%s\n' UNEXPECTED_RESUME >> "$DYNA_FAKE_LOG" ;;
  *)
    trap 'printf "%s\n" INT_RECEIVED >> "$DYNA_FAKE_LOG"; trap "" INT' INT
    trap '' TERM
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-cancel-grace"}'
    while :; do sleep 1 || true; done
    ;;
esac
`)
	p := profile.Profile{
		Name: "cancel-grace", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := RunWithJournal(ctx, p, "cancel during flush", t.TempDir(), true, JournalOptions{
			Path: journalPath, IdleAfter: 30 * time.Millisecond,
		})
		done <- outcome{result: result, err: err}
	}()
	if !waitForTestFile(logPath, "INT_RECEIVED", 2*time.Second) {
		cancel()
		<-done
		t.Fatal("journal interrupt did not enter its graceful flush window")
	}
	cancel()
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "canceled/timed out") || got.result.JournalNudges != 0 {
			t.Fatalf("cancellation outcome = %#v, %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation during journal grace did not stop the worker")
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "CALL ") != 1 || strings.Contains(log, "UNEXPECTED_RESUME") {
		t.Fatalf("canceled graceful interruption resumed the session:\n%s", log)
	}
}

func TestJournalSupervisorValidEntryResetsInactivity(t *testing.T) {
	journalPath := createHarnessJournal(t, 2)
	var entries []runstore.AgentJournalEntry
	supervisor := newJournalSupervisor(JournalOptions{
		Path:      journalPath,
		IdleAfter: time.Minute,
		OnEntry:   func(entry runstore.AgentJournalEntry) { entries = append(entries, entry) },
	})
	supervisor.state.lastActivity = time.Now().Add(-2 * time.Minute)
	if !supervisor.reminderDue() {
		t.Fatal("precondition: inactivity reminder is not due")
	}
	oldActivity := supervisor.state.lastActivity
	if err := runstore.AppendAgentJournalPath(journalPath, runstore.AgentJournalEntry{
		Kind: "verification", Message: "Focused checks passed.", Next: "Continue implementation.", Source: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	supervisor.poll()
	if supervisor.reminderDue() {
		t.Fatal("valid complete agent entry did not reset the inactivity deadline")
	}
	if !supervisor.state.lastActivity.After(oldActivity) || supervisor.state.reminded || supervisor.state.unavailableNote {
		t.Fatalf("supervisor state after activity = %#v", supervisor)
	}
	if len(entries) != 1 || entries[0].Kind != "verification" {
		t.Fatalf("OnEntry entries = %#v", entries)
	}
}

func TestJournalStateKeepsCommittedOffsetAcrossHarnessTurns(t *testing.T) {
	journalPath := createHarnessJournal(t, 34)
	state := NewJournalState()
	first := newJournalSupervisor(JournalOptions{Path: journalPath, IdleAfter: time.Minute, State: state})
	initialOffset := first.offset
	appendRawJournalTest(t, journalPath, `{"ts":1,"kind":"finding","message":"spans attempts","source":"agent"`)
	first.poll()
	if first.offset != initialOffset {
		t.Fatalf("partial record advanced offset to %d, want %d", first.offset, initialOffset)
	}

	var entries []runstore.AgentJournalEntry
	second := newJournalSupervisor(JournalOptions{
		Path: journalPath, IdleAfter: time.Minute, State: state,
		OnEntry: func(entry runstore.AgentJournalEntry) { entries = append(entries, entry) },
	})
	if second.offset != initialOffset {
		t.Fatalf("next harness turn offset = %d, want committed %d", second.offset, initialOffset)
	}
	appendRawJournalTest(t, journalPath, "}\n")
	second.poll()
	if len(entries) != 1 || entries[0].Message != "spans attempts" || !second.hasAgentEntry() {
		t.Fatalf("completed cross-turn entry = %#v", entries)
	}
}

func TestJournalStateKeepsIdleDeadlineAcrossHarnessTurns(t *testing.T) {
	journalPath := createHarnessJournal(t, 36)
	installFakeCLI(t, "quiet-state-worker", `#!/bin/sh
set -eu
cat >/dev/null
sleep 0.04
printf '%s\n' ok
`)
	p := profile.Profile{Name: "quiet-state", Harness: profile.HarnessCustom, Command: []string{"quiet-state-worker"}}
	state := NewJournalState()
	var nudges []JournalNudge
	opts := JournalOptions{
		Path: journalPath, IdleAfter: 60 * time.Millisecond, State: state,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	}
	for i := 0; i < 2; i++ {
		if _, err := RunWithJournal(context.Background(), p, "schema turn", t.TempDir(), true, opts); err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
	}
	idle := 0
	for _, nudge := range nudges {
		if nudge.Reason == JournalNudgeIdle {
			idle++
		}
	}
	if idle != 1 {
		t.Fatalf("idle reminders = %d in %#v, want one deadline shared across turns", idle, nudges)
	}
}

func TestJournalSupervisorIgnoresNonEntries(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*testing.T, string)
		wantSameOffset bool
	}{
		{
			name: "touch",
			mutate: func(t *testing.T, path string) {
				now := time.Now().Add(time.Second)
				if err := os.Chtimes(path, now, now); err != nil {
					t.Fatal(err)
				}
			},
			wantSameOffset: true,
		},
		{
			name:   "malformed complete record",
			mutate: func(t *testing.T, path string) { appendRawJournalTest(t, path, "not-json\n") },
		},
		{
			name: "partial valid-looking record",
			mutate: func(t *testing.T, path string) {
				appendRawJournalTest(t, path, `{"ts":1,"kind":"update","message":"unfinished","source":"agent"}`)
			},
			wantSameOffset: true,
		},
		{
			name: "invalid complete agent record",
			mutate: func(t *testing.T, path string) {
				appendRawJournalTest(t, path, "{\"ts\":0,\"kind\":\"update\",\"message\":\"invalid timestamp\",\"source\":\"agent\"}\n")
			},
		},
		{
			name: "system completion",
			mutate: func(t *testing.T, path string) {
				if err := runstore.AppendAgentJournalPath(path, runstore.AgentJournalEntry{
					Kind: "complete", Message: "Agent completed.", Source: "system",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journalPath := createHarnessJournal(t, i+10)
			entryCalls := 0
			supervisor := newJournalSupervisor(JournalOptions{
				Path: journalPath, IdleAfter: time.Minute,
				OnEntry: func(runstore.AgentJournalEntry) { entryCalls++ },
			})
			initialOffset := supervisor.offset
			supervisor.state.lastActivity = time.Now().Add(-2 * time.Minute)
			tt.mutate(t, journalPath)
			supervisor.poll()

			if !supervisor.reminderDue() {
				t.Fatal("non-entry incorrectly reset the inactivity deadline")
			}
			if entryCalls != 0 {
				t.Fatalf("OnEntry calls = %d, want 0", entryCalls)
			}
			if tt.wantSameOffset && supervisor.offset != initialOffset {
				t.Fatalf("offset = %d, want unchanged %d", supervisor.offset, initialOffset)
			}
		})
	}
}

func TestQuietNonResumableWorkerIsNotInterruptedOrRetried(t *testing.T) {
	journalPath := createHarnessJournal(t, 30)
	logPath := installFakeCLI(t, "quiet-worker", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
	/bin/cat >/dev/null
sleep 0.15
printf '%s\n' 'FINISHED' >> "$DYNA_FAKE_LOG"
printf '%s\n' 'custom worker completed'
`)
	p := profile.Profile{
		Name: "quiet-custom", Harness: profile.HarnessCustom, Command: []string{"quiet-worker"},
		Env: map[string]string{"DYNA_FAKE_LOG": logPath},
	}
	var deliveries []JournalNudge
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := RunWithJournal(ctx, p, "quiet task", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: 30 * time.Millisecond,
		OnNudge: func(nudge JournalNudge) { deliveries = append(deliveries, nudge) },
	})
	if err != nil {
		t.Fatalf("RunWithJournal() error = %v", err)
	}
	if result.Output != "custom worker completed" || result.Nudged || result.JournalNudges != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(deliveries) != 2 || deliveries[0].Delivered || deliveries[0].Reason != JournalNudgeIdle || deliveries[1].Delivered || deliveries[1].Reason != JournalNudgeMissing {
		t.Fatalf("OnNudge deliveries = %#v, want unavailable idle and missing-entry reminders", deliveries)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "CALL ") != 1 || !strings.Contains(log, "FINISHED") {
		t.Fatalf("quiet non-resumable worker was killed or retried:\n%s", log)
	}
}

func TestCancellationWinsWithoutJournalContinuation(t *testing.T) {
	journalPath := createHarnessJournal(t, 31)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
case " $* " in
  *" resume "*) printf '%s\n' 'UNEXPECTED_RESUME' >> "$DYNA_FAKE_LOG" ;;
  *)
    printf '%s\n' '{"type":"thread.started","thread_id":"thread-cancel-journal"}'
    printf '%s\n' 'READY' >> "$DYNA_FAKE_LOG"
    sleep 30
    ;;
esac
`)
	p := profile.Profile{Name: "cancel-codex", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	nudgeCalls := 0
	cwd := t.TempDir()
	go func() {
		result, err := RunWithJournal(ctx, p, "cancel me", cwd, true, JournalOptions{
			Path: journalPath, IdleAfter: 5 * time.Second,
			OnNudge: func(JournalNudge) { nudgeCalls++ },
		})
		done <- outcome{result: result, err: err}
	}()
	if !waitForTestFile(logPath, "READY", 2*time.Second) {
		cancel()
		<-done
		t.Fatal("fake Codex worker did not start")
	}
	cancel()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithJournal did not return after cancellation")
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "canceled/timed out") {
		t.Fatalf("error = %v, want cancellation", got.err)
	}
	if got.result.Nudged || got.result.JournalNudges != 0 || nudgeCalls != 0 {
		t.Fatalf("cancellation triggered continuation: result=%#v OnNudge=%d", got.result, nudgeCalls)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "CALL ") != 1 || strings.Contains(log, "UNEXPECTED_RESUME") || strings.Contains(log, journalNudgePrompt) {
		t.Fatalf("canceled worker was continued:\n%s", log)
	}
}

func TestFailedCompletionReminderStartIsNotReportedDelivered(t *testing.T) {
	journalPath := createHarnessJournal(t, 35)
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
/bin/cat >/dev/null
out=
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  previous=$arg
done
printf '%s\n' '{"type":"thread.started","thread_id":"thread-start-failure"}'
printf '%s' 'valid result before reminder failure' > "$out"
/bin/rm -f "$0"
`)
	fakeCodex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fakeCodex))
	var nudges []JournalNudge
	result, err := RunWithJournal(context.Background(), profile.Profile{
		Name: "vanishing-codex", Harness: profile.HarnessCodex,
		Env: map[string]string{"DYNA_FAKE_LOG": logPath, runstore.AgentJournalEnv: journalPath},
	}, "quick task", t.TempDir(), true, JournalOptions{
		Path: journalPath, IdleAfter: time.Minute,
		OnNudge: func(nudge JournalNudge) { nudges = append(nudges, nudge) },
	})
	if err != nil || result.Output != "valid result before reminder failure" {
		t.Fatalf("RunWithJournal() = %#v, %v", result, err)
	}
	if result.JournalNudges != 0 || len(nudges) != 1 || nudges[0].Delivered || nudges[0].Reason != JournalNudgeMissing {
		t.Fatalf("failed resume start was reported as delivered: result=%#v nudges=%#v", result, nudges)
	}
}

func TestCodexFailureNudgesExactSession(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
input=$(cat)
printf 'INPUT %s\n' "$input" >> "$DYNA_FAKE_LOG"
out=
resume=0
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  if [ "$arg" = "resume" ]; then resume=1; fi
  previous=$arg
done
if [ "$resume" -eq 1 ]; then
  printf 'recovered result' > "$out"
  exit 0
fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-capacity"}'
printf '%s\n' 'Selected model is at capacity' >&2
exit 1
`)

	p := profile.Profile{
		Name: "sol-max", Harness: profile.HarnessCodex, Model: "gpt-test",
		ExtraArgs: []string{"-c", "model_reasoning_effort=xhigh"},
		Env:       map[string]string{"DYNA_FAKE_LOG": logPath},
	}
	result, err := runTest(context.Background(), p, "review the change", t.TempDir())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "recovered result" || !result.Nudged {
		t.Fatalf("Run() result = %#v, want recovered nudged result", result)
	}

	log := readTestFile(t, logPath)
	if got := strings.Count(log, "CALL "); got != 2 {
		t.Fatalf("invocation count = %d, want 2\n%s", got, log)
	}
	if !strings.Contains(log, "CALL exec resume") || !strings.Contains(log, "thread-capacity -") {
		t.Fatalf("resume did not target exact thread:\n%s", log)
	}
	if strings.Count(log, "--model gpt-test") != 2 || strings.Count(log, "model_reasoning_effort=xhigh") != 2 {
		t.Fatalf("model/extra args were not preserved:\n%s", log)
	}
	if !strings.Contains(log, "INPUT "+sessionNudgePrompt) {
		t.Fatalf("nudge prompt not delivered:\n%s", log)
	}
}

func TestCodexFailureWithoutSessionDoesNotRetry(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
printf '%s\n' 'Selected model is at capacity' >&2
exit 1
`)
	p := profile.Profile{Name: "sol-max", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Selected model is at capacity") {
		t.Fatalf("Run() error = %v, want capacity error", err)
	}
	if result.Nudged {
		t.Fatalf("Run() unexpectedly nudged without a session: %#v", result)
	}
	if got := strings.Count(readTestFile(t, logPath), "CALL "); got != 1 {
		t.Fatalf("invocation count = %d, want 1", got)
	}
}

func TestRecoveryBudgetCanDisableNudge(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"thread-no-budget"}'
printf '%s\n' 'temporary failure' >&2
exit 1
`)
	p := profile.Profile{Name: "sol-max", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := RunWithRecovery(context.Background(), p, "task", t.TempDir(), false)
	if err == nil || result.Nudged {
		t.Fatalf("RunWithRecovery() = %#v, %v; want unrecovered failure", result, err)
	}
	if got := strings.Count(readTestFile(t, logPath), "CALL "); got != 1 {
		t.Fatalf("invocation count = %d, want 1", got)
	}
}

func TestCodexEmptyFinalMessageNudgesInsteadOfReturningEvents(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
out=
resume=0
previous=
for arg in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then out=$arg; fi
  if [ "$arg" = "resume" ]; then resume=1; fi
  previous=$arg
done
if [ "$resume" -eq 1 ]; then
  printf '%s' 'final after empty output' > "$out"
  exit 0
fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-empty"}'
exit 0
`)
	p := profile.Profile{Name: "sol-max", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err != nil || result.Output != "final after empty output" || !result.Nudged {
		t.Fatalf("Run() = %#v, %v; want recovered final message", result, err)
	}
	if strings.Contains(result.Output, "thread.started") {
		t.Fatalf("Run() returned Codex transport events: %q", result.Output)
	}
}

func TestCodexCancellationDoesNotNudge(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-canceled"}'
sleep 30
`)
	p := profile.Profile{Name: "sol-max", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	result, err := runTest(ctx, p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "canceled/timed out") {
		t.Fatalf("Run() error = %v, want timeout", err)
	}
	if result.Nudged {
		t.Fatalf("Run() nudged after cancellation: %#v", result)
	}
	if got := strings.Count(readTestFile(t, logPath), "CALL "); got != 1 {
		t.Fatalf("invocation count = %d, want 1", got)
	}
}

func TestContextTerminationDoesNotLookLikeEmptyOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := attemptError(ctx, profile.Profile{Name: "luna"}, []string{"codex"}, attempt{
		started: true, contextDone: true,
	})
	if err == nil || !strings.Contains(err.Error(), "canceled/timed out") {
		t.Fatalf("attemptError() = %v, want cancellation/timeout", err)
	}
	if strings.Contains(err.Error(), "empty output") {
		t.Fatalf("attemptError() hid context termination as an empty response: %v", err)
	}
}

func TestCodexReportsBothFailures(t *testing.T) {
	logPath := installFakeCLI(t, "codex", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
cat >/dev/null
case " $* " in
  *" resume "*) printf '%s\n' 'capacity persists' >&2 ;;
  *) printf '%s\n' '{"type":"thread.started","thread_id":"thread-twice"}'; printf '%s\n' 'first capacity failure' >&2 ;;
esac
exit 1
`)
	p := profile.Profile{Name: "sol-max", Harness: profile.HarnessCodex, Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "first capacity failure") || !strings.Contains(err.Error(), "same-session nudge failed") || !strings.Contains(err.Error(), "capacity persists") {
		t.Fatalf("Run() error = %v, want both diagnostics", err)
	}
	if !result.Nudged {
		t.Fatalf("Run() result = %#v, want attempted nudge", result)
	}
	if got := strings.Count(readTestFile(t, logPath), "CALL "); got != 2 {
		t.Fatalf("invocation count = %d, want 2", got)
	}
}

func TestClaudeEmptyOutputNudgesAssignedSession(t *testing.T) {
	logPath := installFakeCLI(t, "claude", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
input=$(cat)
printf 'INPUT %s\n' "$input" >> "$DYNA_FAKE_LOG"
case " $* " in
  *" --resume "*) printf '%s\n' 'claude recovered' ;;
  *) exit 0 ;;
esac
`)
	p := profile.Profile{Name: "fable", Harness: profile.HarnessClaudeCode, Model: "fable", Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err != nil || result.Output != "claude recovered" || !result.Nudged {
		t.Fatalf("Run() = %#v, %v; want recovered result", result, err)
	}
	assertSameAssignedSession(t, readTestFile(t, logPath), "--session-id", "--resume")
}

func TestPiFailureNudgesAssignedSession(t *testing.T) {
	logPath := installFakeCLI(t, "pi", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
case " $* " in
  *" --session "*) printf '%s\n' 'pi recovered' ;;
  *) printf '%s\n' 'temporary provider failure' >&2; exit 1 ;;
esac
`)
	p := profile.Profile{Name: "pi", Harness: profile.HarnessPi, Model: "pi-model", Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err != nil || result.Output != "pi recovered" || !result.Nudged {
		t.Fatalf("Run() = %#v, %v; want recovered result", result, err)
	}
	assertSameAssignedSession(t, readTestFile(t, logPath), "--session-id", "--session")
}

func TestOpenCodeFailureNudgesParsedSession(t *testing.T) {
	logPath := installFakeCLI(t, "opencode", `#!/bin/sh
set -eu
printf 'CALL %s\n' "$*" >> "$DYNA_FAKE_LOG"
case " $* " in
  *" --session session-open "*)
    printf '%s\n' '{"type":"text","sessionID":"session-open","part":{"messageID":"msg-old","text":"intermediate"}}'
    printf '%s\n' '{"type":"text","sessionID":"session-open","part":{"messageID":"msg-final","text":"open"}}'
    printf '%s\n' '{"type":"text","sessionID":"session-open","part":{"messageID":"msg-final","text":"code recovered"}}'
    ;;
  *)
    printf '%s\n' '{"type":"step_start","sessionID":"session-open","part":{"type":"step-start"}}'
    printf '%s\n' 'temporary provider failure' >&2
    exit 1
    ;;
esac
`)
	p := profile.Profile{Name: "oc", Harness: profile.HarnessOpenCode, Model: "provider/model", Env: map[string]string{"DYNA_FAKE_LOG": logPath}}

	result, err := runTest(context.Background(), p, "task", t.TempDir())
	if err != nil || result.Output != "open\ncode recovered" || !result.Nudged {
		t.Fatalf("Run() = %#v, %v; want parsed recovered result", result, err)
	}
	log := readTestFile(t, logPath)
	if got := strings.Count(log, "CALL "); got != 2 || !strings.Contains(log, "--session session-open") {
		t.Fatalf("exact OpenCode session was not resumed:\n%s", log)
	}
	if strings.Contains(result.Output, "sessionID") {
		t.Fatalf("Run() returned raw JSONL: %q", result.Output)
	}
}

func TestExplicitControlsDisableNudge(t *testing.T) {
	tests := []struct {
		name    string
		profile profile.Profile
	}{
		{"claude", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--no-session-persistence"}}},
		{"claude-budget", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--max-budget-usd", "1"}}},
		{"claude-fork", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--fork-session"}}},
		{"codex", profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: []string{"--ephemeral"}}},
		{"opencode", profile.Profile{Harness: profile.HarnessOpenCode, ExtraArgs: []string{"--format", "default"}}},
		{"pi", profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"--no-session"}}},
		{"pi-fork", profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"--fork", "existing"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := buildInvocation(tt.profile, "task")
			if err != nil {
				t.Fatalf("buildInvocation() error = %v", err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			if inv.sessionID != nil || inv.resume != nil {
				t.Fatalf("explicitly controlled invocation is resumable: %#v", inv.initial.argv)
			}
		})
	}
}

func TestCodexInitialOnlyArgsDisableRecovery(t *testing.T) {
	for _, extra := range [][]string{
		{"--sandbox", "read-only"},
		{"--cd=/tmp/project"},
		{"--add-dir", "/tmp/other"},
		{"--oss", "--local-provider", "ollama"},
	} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: extra}, "task")
		if err != nil {
			t.Fatal(err)
		}
		defer inv.cleanup()
		if inv.resume != nil || inv.sessionID != nil {
			t.Fatalf("initial-only Codex args are resumable: %#v", extra)
		}
	}
}

func TestReadOnlyCodexJournalUsesNarrowResumablePermissionProfile(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "wf_permissions", "agents", "7", "journal.jsonl")
	for _, extra := range [][]string{
		{"--sandbox", "read-only", "-c", "model_reasoning_effort=high"},
		{"--sandbox=read-only"},
		{"-s", "read-only"},
		{"-c", `sandbox_mode="read-only"`},
		{"--config", `default_permissions=":read-only"`},
	} {
		p, err := withJournalPermissions(profile.Profile{
			Name: "readonly", Harness: profile.HarnessCodex, ExtraArgs: extra,
		}, journalPath)
		if err != nil {
			t.Fatalf("withJournalPermissions(%v) error = %v", extra, err)
		}
		if !p.SafeMode || hasAnyOption(p.ExtraArgs, "--sandbox", "-s") || hasArg(p.ExtraArgs, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("read-only conversion weakened or retained conflicting flags: %#v", p)
		}
		joined := strings.Join(p.ExtraArgs, " ")
		if !strings.Contains(joined, `default_permissions="dyna-journal"`) ||
			!strings.Contains(joined, `approval_policy="never"`) ||
			!strings.Contains(joined, `extends=":read-only"`) ||
			!strings.Contains(joined, strconv.Quote(filepath.Dir(journalPath))+`="write"`) {
			t.Fatalf("narrow journal permission config missing from %#v", p.ExtraArgs)
		}

		inv, err := buildInvocation(p, "inspect only")
		if err != nil {
			t.Fatal(err)
		}
		defer inv.cleanup()
		if inv.resume == nil || inv.sessionID == nil {
			t.Fatalf("converted read-only invocation is not exact-session resumable: %#v", inv.initial.argv)
		}
		resumed := inv.resume("thread-readonly", "continue")
		if strings.Contains(strings.Join(inv.initial.argv, " "), "dangerously-bypass") ||
			strings.Contains(strings.Join(resumed.argv, " "), "dangerously-bypass") ||
			!strings.Contains(strings.Join(resumed.argv, " "), `default_permissions="dyna-journal"`) {
			t.Fatalf("permission profile was not preserved safely on resume: initial=%#v resume=%#v", inv.initial.argv, resumed.argv)
		}
	}
}

func TestExplicitHarnessPermissionsAreNeverAutoBypassed(t *testing.T) {
	tests := []profile.Profile{
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"--sandbox", "read-only"}},
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"-c", `default_permissions="locked-down"`}},
		{Harness: profile.HarnessCodex, ExtraArgs: []string{"--profile", "locked-down"}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--permission-mode", "plan"}},
		{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--allowedTools", "Read,Grep"}},
	}
	for _, p := range tests {
		inv, err := buildInvocation(p, "inspect only")
		if err != nil {
			t.Fatalf("buildInvocation(%#v): %v", p, err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		joined := strings.Join(inv.initial.argv, " ")
		if strings.Contains(joined, "dangerously-bypass-approvals-and-sandbox") || strings.Contains(joined, "dangerously-skip-permissions") {
			t.Fatalf("explicit permissions were weakened: %s", joined)
		}
	}
}

func TestCodexReservedOutputArgsAreRejected(t *testing.T) {
	for _, extra := range [][]string{
		{"-o", "/tmp/result"},
		{"--output-last-message=/tmp/result"},
		{"-o=/tmp/result"},
		{"-o/tmp/result"},
	} {
		if inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: extra}, "task"); err == nil {
			if inv.cleanup != nil {
				inv.cleanup()
			}
			t.Fatalf("buildInvocation(%v) accepted reserved output argument", extra)
		}
	}
}

func TestMockReportsCallerTaskWithoutJournalEnvelope(t *testing.T) {
	out, err := runMock(context.Background(), "[DYNA WORK JOURNAL]\ninternal instructions\n[/DYNA WORK JOURNAL]\n\n[ORIGINAL TASK]\ninspect the decoder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "inspect the decoder") || strings.Contains(out, "DYNA WORK JOURNAL") {
		t.Fatalf("mock output leaked internal envelope: %q", out)
	}
}

func TestOpenCodeExplicitJSONRemainsResumable(t *testing.T) {
	for _, extra := range [][]string{{"--format", "json"}, {"--format=json"}} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessOpenCode, ExtraArgs: extra}, "task")
		if err != nil {
			t.Fatalf("buildInvocation(%v) error = %v", extra, err)
		}
		if inv.sessionID == nil || inv.resume == nil || inv.initial.parseOutput == nil {
			t.Fatalf("explicit JSON invocation is not resumable: %#v", inv.initial.argv)
		}
	}
}

func TestAssignedSessionIDsAreUnique(t *testing.T) {
	first, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode}, "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode}, "two")
	if err != nil {
		t.Fatal(err)
	}
	a := optionValue(first.initial.argv, "--session-id")
	b := optionValue(second.initial.argv, "--session-id")
	if a == "" || b == "" || a == b {
		t.Fatalf("assigned session IDs = %q, %q; want distinct non-empty IDs", a, b)
	}
}

func TestDisableSubagentsNativeControlsInitialAndResume(t *testing.T) {
	tests := []struct {
		name    string
		profile profile.Profile
		option  string
		value   string
	}{
		{"claude", profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: []string{"--verbose"}, DisableSubagents: true}, "--disallowedTools", "Agent"},
		{"codex", profile.Profile{Harness: profile.HarnessCodex, ExtraArgs: []string{"-c", "model_reasoning_effort=high"}, DisableSubagents: true}, "--disable", "multi_agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := buildInvocation(tt.profile, "task")
			if err != nil {
				t.Fatal(err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			if !hasOptionValue(inv.initial.argv, tt.option, tt.value) || inv.resume == nil {
				t.Fatalf("initial argv/control or resume missing: %#v", inv.initial.argv)
			}
			resumed := inv.resume("session-1", "continue")
			if !hasOptionValue(resumed.argv, tt.option, tt.value) {
				t.Fatalf("resume argv missing control: %#v", resumed.argv)
			}
			if optionIndex(inv.initial.argv, tt.option) <= optionIndex(inv.initial.argv, tt.profile.ExtraArgs[0]) {
				t.Fatalf("native control must follow extra args: %#v", inv.initial.argv)
			}
		})
	}
}

func TestDisableSubagentsNativeControlsAreOptInAndNarrow(t *testing.T) {
	for _, p := range []profile.Profile{
		{Harness: profile.HarnessClaudeCode},
		{Harness: profile.HarnessCodex},
		{Harness: profile.HarnessOpenCode, DisableSubagents: true},
		{Harness: profile.HarnessPi, DisableSubagents: true},
		{Harness: profile.HarnessCustom, Command: []string{"worker"}, DisableSubagents: true},
	} {
		inv, err := buildInvocation(p, "task")
		if err != nil {
			t.Fatal(err)
		}
		if inv.cleanup != nil {
			defer inv.cleanup()
		}
		joined := strings.Join(inv.initial.argv, " ")
		if strings.Contains(joined, "--disallowedTools Agent") || strings.Contains(joined, "--disable multi_agent") {
			t.Fatalf("profile %#v received an unrequested or unsupported native control: %s", p, joined)
		}
	}
}

func TestDisableSubagentsRejectsExplicitConflicts(t *testing.T) {
	for _, p := range []profile.Profile{
		{Harness: profile.HarnessClaudeCode, DisableSubagents: true, ExtraArgs: []string{"--allowedTools", "Read,Agent"}},
		{Harness: profile.HarnessClaudeCode, DisableSubagents: true, ExtraArgs: []string{"--allowedTools", "Read", "Agent(reviewer)"}},
		{Harness: profile.HarnessCodex, DisableSubagents: true, ExtraArgs: []string{"--enable", "multi_agent"}},
		{Harness: profile.HarnessCodex, DisableSubagents: true, ExtraArgs: []string{"-c", "features.multi_agent=true"}},
	} {
		if inv, err := buildInvocation(p, "task"); err == nil {
			if inv.cleanup != nil {
				inv.cleanup()
			}
			t.Fatalf("accepted conflicting profile: %#v", p)
		}
	}
}

func TestDisableSubagentsMergesClaudeDenyLists(t *testing.T) {
	p := profile.Profile{
		Harness: profile.HarnessClaudeCode, DisableSubagents: true,
		ExtraArgs: []string{"--disallowedTools", "Bash(git *)", "--disallowed-tools=WebFetch"},
	}
	inv, err := buildInvocation(p, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ option, value string }{
		{"--disallowedTools", "Bash"},
		{"--disallowedTools", "Agent"},
		{"--disallowed-tools", "WebFetch"},
		{"--disallowed-tools", "Agent"},
	} {
		if !hasOptionValue(inv.initial.argv, check.option, check.value) {
			t.Fatalf("merged argv missing %s=%s: %#v", check.option, check.value, inv.initial.argv)
		}
	}
}

func optionIndex(args []string, option string) int {
	for i, arg := range args {
		if arg == option || strings.HasPrefix(arg, option+"=") {
			return i
		}
	}
	return -1
}

func createHarnessJournal(t *testing.T, agentID int) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	run := &runstore.Run{Dir: filepath.Join(runstore.RunsDir(), "wf_harness-journal")}
	if err := os.MkdirAll(run.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := run.StartAgentJournal(agentID, "test worker", "test-profile", "Tests", "test prompt")
	if err != nil {
		t.Fatalf("StartAgentJournal() error = %v", err)
	}
	return path
}

func appendRawJournalTest(t *testing.T, path, record string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(record); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForTestFile(path, substring string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), substring) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func installFakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(dir, "calls.log")
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertSameAssignedSession(t *testing.T, log, initialOption, resumeOption string) {
	t.Helper()
	var calls []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "CALL ") {
			calls = append(calls, line)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("invocation count = %d, want 2\n%s", len(calls), log)
	}
	initialID := optionValue(strings.Fields(calls[0]), initialOption)
	resumedID := optionValue(strings.Fields(calls[1]), resumeOption)
	if initialID == "" || resumedID != initialID {
		t.Fatalf("session IDs differ: initial=%q resumed=%q\n%s", initialID, resumedID, log)
	}
}

func optionValue(args []string, option string) string {
	for i, arg := range args {
		if arg == option && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, option+"=") {
			return strings.TrimPrefix(arg, option+"=")
		}
	}
	return ""
}

func runTest(ctx context.Context, p profile.Profile, prompt, cwd string) (Result, error) {
	return run(ctx, p, prompt, cwd, true, 0)
}
