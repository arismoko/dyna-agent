package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

type workflowEventRecorder struct {
	mu     sync.Mutex
	events []runstore.Event
}

func (r *workflowEventRecorder) append(event runstore.Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *workflowEventRecorder) snapshot() []runstore.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runstore.Event(nil), r.events...)
}

func mockWorkflowStore(maxConcurrent, maxCalls int) *profile.Store {
	return &profile.Store{Profiles: []profile.Profile{{
		Name: "mock", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
		MaxConcurrent: maxConcurrent, MaxCallsPerRun: maxCalls,
	}}}
}

func writeWorkflowScript(t *testing.T, dir, name, src string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func executeWorkflowTest(t *testing.T, script string, opts Options) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	opts.ScriptSrc = script
	return Execute(ctx, opts)
}

func TestWorkflowResolvesRegistryNameAndPassesArgs(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "echo.js", `return {received: args.value};`)

	result, err := executeWorkflowTest(t, `return await workflow("echo", {value: 42});`, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: dir, WorkflowDir: registry,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != `{"received":42}` {
		t.Fatalf("result = %s, want registry child result", result)
	}
}

func TestWorkflowNestingStopsAtOneLevel(t *testing.T) {
	dir := t.TempDir()
	rootPath := writeWorkflowScript(t, dir, "root.js", `return await workflow("./child.js");`)
	writeWorkflowScript(t, dir, "child.js", `return await workflow("./grandchild.js");`)
	writeWorkflowScript(t, dir, "grandchild.js", `return "must not execute";`)

	_, err := executeWorkflowTest(t, `return await workflow("./child.js");`, Options{
		ScriptPath: rootPath, Store: mockWorkflowStore(0, 0), WorkDir: dir,
	})
	if err == nil || !strings.Contains(err.Error(), "workflow nesting limit exceeded") || !strings.Contains(err.Error(), "maximum depth is 1") {
		t.Fatalf("Execute() error = %v, want clear one-level nesting diagnostic", err)
	}
}

func TestNestedWorkflowSharesLifetimeAgentCap(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent("nested", {profile: "mock"});`)
	recorder := &workflowEventRecorder{}
	script := `
return await parallel([
  () => workflow("child"),
  () => agent("sibling", {profile: "mock"}),
]);`
	resultJSON, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: dir, WorkflowDir: registry,
		MaxConc: 2, MaxAgents: 1, OnEvent: recorder.append,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result []any
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatal(err)
	}
	nulls := 0
	for _, value := range result {
		if value == nil {
			nulls++
		}
	}
	if len(result) != 2 || nulls != 1 {
		t.Fatalf("result = %s, want exactly one capped call", resultJSON)
	}
	starts := 0
	for _, event := range recorder.snapshot() {
		if event.T == "agent_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("agent_start count = %d, want one shared-cap launch", starts)
	}
}

func TestNestedWorkflowSharesConcurrencyLimits(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		maxConcurrent        int
		profileMaxConcurrent int
	}{
		{name: "run semaphore", maxConcurrent: 1},
		{name: "profile semaphore", maxConcurrent: 2, profileMaxConcurrent: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			registry := filepath.Join(dir, "registry")
			if err := os.Mkdir(registry, 0o755); err != nil {
				t.Fatal(err)
			}
			writeWorkflowScript(t, registry, "child.js", `return await agent("nested", {profile: "mock"});`)
			recorder := &workflowEventRecorder{}
			script := `return await parallel([
  () => workflow("child"),
  () => agent("sibling", {profile: "mock"}),
]);`
			_, err := executeWorkflowTest(t, script, Options{
				Store: mockWorkflowStore(tc.profileMaxConcurrent, 0), WorkDir: dir,
				WorkflowDir: registry, MaxConc: tc.maxConcurrent, OnEvent: recorder.append,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			running, peak := 0, 0
			for _, event := range recorder.snapshot() {
				switch event.T {
				case "agent_run":
					running++
					if running > peak {
						peak = running
					}
				case "agent_end":
					if event.Cached {
						continue
					}
					running--
				}
			}
			if peak != 1 || running != 0 {
				t.Fatalf("running peak/final = %d/%d, want 1/0; events=%#v", peak, running, recorder.snapshot())
			}
		})
	}
}

func TestNestedWorkflowSharesProfileCallLimit(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent("nested", {profile: "mock"});`)
	script := `return await parallel([
  () => workflow("child"),
  () => agent("sibling", {profile: "mock"}),
]);`
	_, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 1), WorkDir: dir, WorkflowDir: registry, MaxConc: 2,
	})
	if err == nil || !strings.Contains(err.Error(), `profile "mock" call limit exceeded (1 per run)`) {
		t.Fatalf("Execute() error = %v, want shared profile call cap abort", err)
	}
}

func TestWorkflowDoesNotDeadlockInParallelOrPipeline(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `return await agent("nested " + args.value, {profile: "mock"});`)
	script := `
const parallelResult = await parallel([
  () => workflow("child", {value: "parallel"}),
  () => agent("sibling", {profile: "mock"}),
]);
const pipelineResult = await pipeline([1, 2],
  (item) => workflow("child", {value: item})
);
return {parallelResult, pipelineResult};`
	result, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: dir, WorkflowDir: registry, MaxConc: 1,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result, "parallelResult") || !strings.Contains(result, "pipelineResult") || strings.Contains(result, "null") {
		t.Fatalf("result = %s, want completed parallel and pipeline child calls", result)
	}
}

func TestNestedWorkflowPersistsChildArtifactsAndUsesParentResumeCache(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry")
	if err := os.Mkdir(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowScript(t, registry, "child.js", `
phase("Child phase");
return await agent('RESPOND: {"answer":"nested"}', {profile: "mock", schema: {type: "object"}});`)
	rootScript := `phase("Parent phase"); return await workflow("child", {value: 1});`

	t.Setenv("DYNA_RUN_ID", "wf_nested-first")
	first, err := runstore.Create("nested first", rootScript, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkflowTest(t, rootScript, Options{
		Store: mockWorkflowStore(0, 0), Run: first, WorkDir: dir, WorkflowDir: registry,
	})
	first.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(first.Meta.ID)
	if err != nil || len(entries) != 1 || entries[0].Workflow != "nested-1" {
		t.Fatalf("parent journal = %#v, %v", entries, err)
	}
	childDir := filepath.Join(first.Dir, "workflows", "nested-1")
	for _, name := range []string{"script.js", "meta.json", "events.jsonl", "journal.jsonl", "result.json"} {
		if _, err := os.Stat(filepath.Join(childDir, name)); err != nil {
			t.Fatalf("missing child artifact %s: %v", name, err)
		}
	}
	childJournal, err := os.ReadFile(filepath.Join(childDir, "journal.jsonl"))
	if err != nil || !strings.Contains(string(childJournal), `"workflow":"nested-1"`) {
		t.Fatalf("child journal = %q, %v", childJournal, err)
	}
	events, err := runstore.ReadEvents(first.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundStart, foundNestedAgent := false, false
	for _, event := range events {
		if event.T == "workflow_start" && event.Workflow == "nested-1" && event.Parent == first.Meta.ID && event.Phase == "Parent phase" {
			foundStart = true
		}
		if event.T == "agent_start" && event.Workflow == "nested-1" && event.Phase == "Child phase" {
			foundNestedAgent = true
		}
	}
	if !foundStart || !foundNestedAgent {
		t.Fatalf("nested relationship missing from parent events: %#v", events)
	}

	t.Setenv("DYNA_RUN_ID", "wf_nested-resumed")
	second, err := runstore.Create("nested resumed", rootScript, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = executeWorkflowTest(t, rootScript, Options{
		Store: mockWorkflowStore(0, 0), Run: second, WorkDir: dir, WorkflowDir: registry,
		Cache: NewCache(entries),
	})
	second.Finish("ok", result, err)
	if err != nil {
		t.Fatal(err)
	}
	resumedEvents, err := runstore.ReadEvents(second.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached := false
	for _, event := range resumedEvents {
		if event.T == "agent_end" && event.Workflow == "nested-1" && event.Cached {
			cached = true
		}
	}
	if !cached {
		t.Fatalf("nested agent did not use parent resume cache: %#v", resumedEvents)
	}
	resumedEntries, err := runstore.ReadJournal(second.Meta.ID)
	if err != nil || len(resumedEntries) != 1 || !resumedEntries[0].Cached || resumedEntries[0].Workflow != "nested-1" {
		t.Fatalf("resumed parent journal = %#v, %v", resumedEntries, err)
	}
	if _, err := os.Stat(filepath.Join(second.Dir, "workflows", "nested-1", "journal.jsonl")); err != nil {
		t.Fatalf("resumed child journal is not discoverable: %v", err)
	}
}
