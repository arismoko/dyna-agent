package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestProfileCallLimitDrainsAcceptedWorkersAndJournalsSuccess(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-call-cap-drain")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	const script = `
const running = [0, 1, 2, 3].map(i =>
  agent("accepted-" + i, {profile: "capped", label: "accepted-" + i})
);
await sleep(100);
return await parallel([
  ...[4, 5].map(i => () =>
    agent("overflow-" + i, {profile: "capped", label: "overflow-" + i})
  ),
]);
`
	run, err := runstore.Create("call cap drain test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("error", "expected call cap failure", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "capped", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10, MaxConcurrent: 4, MaxCallsPerRun: 4,
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := Execute(ctx, Options{
		ScriptSrc: script,
		Store:     store,
		Run:       run,
		WorkDir:   t.TempDir(),
		MaxConc:   4,
	})
	if err == nil || !strings.Contains(err.Error(), `profile "capped" call limit exceeded (4 per run)`) {
		t.Fatalf("Execute() = %s, %v; want profile call-limit failure", result, err)
	}

	entries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("journal entries = %#v, want only the four accepted calls", entries)
	}
	seenIDs := make(map[int]bool)
	for i, entry := range entries {
		if entry.Error != "" {
			t.Errorf("accepted journal entry %d has error %q", i, entry.Error)
		}
		if entry.ID < 1 || entry.ID > 4 || seenIDs[entry.ID] || entry.Profile != "capped" || entry.Prompt != fmt.Sprintf("accepted-%d", entry.ID-1) {
			t.Errorf("accepted journal entry %d = %#v", i, entry)
		}
		seenIDs[entry.ID] = true
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	overflowSeen := false
	completedAfterOverflow := 0
	for _, event := range events {
		if event.T == "agent_end" && event.Status == "error" && strings.Contains(event.Error, "call limit exceeded") {
			overflowSeen = true
		}
		if overflowSeen && event.T == "agent_end" && event.Status == "ok" && event.ID <= 4 {
			completedAfterOverflow++
		}
		if event.ID <= 4 && event.T == "agent_end" && event.Status == "error" {
			t.Errorf("accepted call %d was canceled by overflow: %#v", event.ID, event)
		}
	}
	if !overflowSeen || completedAfterOverflow == 0 {
		t.Fatalf("events do not show accepted workers completing after overflow: %#v", events)
	}

	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer resumeCancel()
	resumed, err := Execute(resumeCtx, Options{
		ScriptSrc: script,
		Store:     store,
		Cache:     NewCache(entries),
		WorkDir:   t.TempDir(),
		MaxConc:   4,
	})
	if err != nil {
		t.Fatalf("resumed Execute() error = %v; accepted calls should replay without consuming the live cap", err)
	}
	var resumedValues []any
	if err := json.Unmarshal([]byte(resumed), &resumedValues); err != nil || len(resumedValues) != 2 {
		t.Fatalf("resumed result = %s, %v; want both formerly rejected calls", resumed, err)
	}
	for i, value := range resumedValues {
		if value == nil {
			t.Errorf("resumed result %d is null: %s", i, resumed)
		}
	}
}

func TestProfilesRemainingIsLiveReadOnlyRunBudget(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{
		{
			Name: "capped", Harness: profile.HarnessMock, Default: true,
			Taste: 5, Intelligence: 5, Cost: 10, MaxCallsPerRun: 3,
		},
		{
			Name: "unlimited", Harness: profile.HarnessMock,
			Taste: 5, Intelligence: 5, Cost: 10,
		},
	}}
	const script = `
const capped = profiles.find(p => p.name === "capped");
const unlimited = profiles.find(p => p.name === "unlimited");
const initial = capped.remaining;
const first = agent("first", {profile: capped.name});
const afterFirst = capped.remaining;
const second = agent("second", {profile: capped.name});
const afterSecond = capped.remaining;
await Promise.all([first, second]);
const afterComplete = capped.remaining;
capped.remaining = 99;
return {
  initial,
  afterFirst,
  afterSecond,
  afterComplete,
  afterWrite: capped.remaining,
  readOnly: Object.getOwnPropertyDescriptor(capped, "remaining").set === undefined,
  unlimited: unlimited.remaining,
};
`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := Execute(ctx, Options{ScriptSrc: script, Store: store, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var got struct {
		Initial       int  `json:"initial"`
		AfterFirst    int  `json:"afterFirst"`
		AfterSecond   int  `json:"afterSecond"`
		AfterComplete int  `json:"afterComplete"`
		AfterWrite    int  `json:"afterWrite"`
		ReadOnly      bool `json:"readOnly"`
		Unlimited     any  `json:"unlimited"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result %q: %v", result, err)
	}
	if got.Initial != 3 || got.AfterFirst != 2 || got.AfterSecond != 1 || got.AfterComplete != 1 || got.AfterWrite != 1 {
		t.Fatalf("remaining values = %#v, want 3,2,1,1,1", got)
	}
	if !got.ReadOnly || got.Unlimited != nil {
		t.Fatalf("remaining metadata = %#v, want read-only capped value and null unlimited value", got)
	}
}
