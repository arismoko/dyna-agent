package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestJournalWorkerPromptContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wf_prompt-contract", "agents", "7", "journal.jsonl")
	task := "Inspect the repository without changing it, then report findings."
	prompt := journalWorkerPrompt(task, path)

	for _, required := range []string{
		"dyna journal",
		path,
		"read-only exploration",
		"only allowed write",
		"does not modify the target workspace",
		"one or two sentences plus an optional next step",
		"Keep working after every entry",
		"still return the final response",
		"separate from any final-response JSON schema",
		"[ORIGINAL TASK]",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("journalWorkerPrompt() does not contain %q:\n%s", required, prompt)
		}
	}
	if !strings.HasSuffix(prompt, task) {
		t.Fatalf("caller task was not preserved verbatim at the end of the prompt:\n%s", prompt)
	}
	if strings.Index(prompt, path) > strings.Index(prompt, "[ORIGINAL TASK]") {
		t.Fatalf("journal instructions/path must be distinct from and precede the caller task:\n%s", prompt)
	}
}

func TestExecuteKeepsCallerPromptAndWritesCompleteAgentJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-journal-test")

	const callerPrompt = `RESPOND: {"answer":"ok"}`
	const schemaJSON = `{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`
	script := fmt.Sprintf(`
const schema = JSON.parse(%s);
const result = await agent(%s, {
  profile: "mock-journal",
  label: "journaled worker",
  phase: "Verification",
  schema,
});
return result;
`, strconv.Quote(schemaJSON), strconv.Quote(callerPrompt))

	run, err := runstore.Create("engine journal test", script, nil)
	if err != nil {
		t.Fatalf("runstore.Create() error = %v", err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "mock-journal", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resultJSON, err := Execute(ctx, Options{
		ScriptSrc: script,
		Store:     store,
		Run:       run,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resultJSON != `{"answer":"ok"}` {
		t.Fatalf("result = %s, want schema-valid mock result", resultJSON)
	}

	rootEntries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatalf("ReadJournal() error = %v", err)
	}
	if len(rootEntries) != 1 {
		t.Fatalf("root journal entries = %#v, want one completed call", rootEntries)
	}
	root := rootEntries[0]
	if root.Prompt != callerPrompt {
		t.Fatalf("root JournalEntry.Prompt = %q, want original caller prompt %q", root.Prompt, callerPrompt)
	}
	if strings.Contains(root.Prompt, "DYNA WORK JOURNAL") || strings.Contains(root.Prompt, "OUTPUT FORMAT") {
		t.Fatalf("root journal persisted an internal prompt wrapper: %q", root.Prompt)
	}
	wantKey := callKey("mock-journal", callerPrompt, schemaJSON)
	if root.Key != wantKey {
		t.Fatalf("call key = %q, want caller-prompt key %q", root.Key, wantKey)
	}
	wrappedKey := callKey("mock-journal", journalWorkerPrompt(callerPrompt, "journal-path"), schemaJSON)
	if root.Key == wrappedKey {
		t.Fatal("call key was derived from the journal instruction envelope")
	}

	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatalf("ReadAgentJournalFrom() error = %v", err)
	}
	if len(agentEntries) != 3 {
		t.Fatalf("agent journal entries = %#v, want start + agent update + complete", agentEntries)
	}
	if start := agentEntries[0]; start.Kind != "start" || start.Source != "system" || start.AgentID != 1 || start.Prompt != callerPrompt {
		t.Fatalf("start entry = %#v", start)
	}
	if update := agentEntries[1]; update.Kind != "update" || update.Source != "agent" || update.Message == "" {
		t.Fatalf("agent-authored entry = %#v", update)
	}
	if complete := agentEntries[2]; complete.Kind != "complete" || complete.Source != "system" || complete.Message == "" {
		t.Fatalf("completion entry = %#v", complete)
	}
}

func TestDisableSubagentsPromptFollowsTaskAndPrecedesSchema(t *testing.T) {
	const task = "Complete the caller task."
	workerTask := disableSubagentsWorkerPrompt(task)
	restriction := "[DYNA PROFILE RESTRICTION]"
	if !strings.HasPrefix(workerTask, task) || strings.Index(workerTask, restriction) <= strings.Index(workerTask, task) {
		t.Fatalf("worker task = %q", workerTask)
	}
	wrapped := journalWorkerPrompt(workerTask, "/tmp/journal.jsonl")
	if strings.Index(wrapped, "DYNA WORK JOURNAL") > strings.Index(wrapped, task) || strings.Index(wrapped, task) > strings.Index(wrapped, restriction) {
		t.Fatalf("prompt wrappers are out of order:\n%s", wrapped)
	}
	base := wrapped + "\n\n---\nOUTPUT FORMAT: schema"
	if strings.Index(base, restriction) > strings.Index(base, "OUTPUT FORMAT") {
		t.Fatalf("schema wrapper did not remain last:\n%s", base)
	}
}

func TestDisableSubagentsPromptReachesUnsupportedHarnessOnlyWhenEnabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%v", disabled), func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "prompt.txt")
			store := &profile.Store{Profiles: []profile.Profile{{
				Name: "custom", Harness: profile.HarnessCustom, Default: true,
				Taste: 5, Intelligence: 5, Cost: 5, DisableSubagents: disabled,
				Command: []string{"/bin/sh", "-c", `cat > "$CAPTURE"; printf done`},
				Env:     map[string]string{"CAPTURE": capture},
			}}}
			result, err := Execute(context.Background(), Options{
				ScriptSrc: `return await agent("caller task", {profile: "custom"});`,
				Store:     store, WorkDir: t.TempDir(),
			})
			if err != nil || result != `"done"` {
				t.Fatalf("Execute() = %s, %v", result, err)
			}
			prompt, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			hasRestriction := strings.Contains(string(prompt), "DYNA PROFILE RESTRICTION")
			if hasRestriction != disabled || !strings.HasPrefix(string(prompt), "caller task") {
				t.Fatalf("captured prompt = %q, disableSubagents=%v", prompt, disabled)
			}
		})
	}
}

func TestExecuteWaitsForDanglingWorkerTerminalJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-dangling-journal")
	script := `
agent("keep working until the orchestrator finishes", {profile: "slow", label: "dangling worker"});
agent("wait behind the first worker", {profile: "slow", label: "queued worker"});
await agent("quick synchronization point", {profile: "mock-sync", label: "sync worker"});
return "workflow done";
`
	run, err := runstore.Create("dangling journal test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{
		{
			Name: "slow", Harness: profile.HarnessCustom, Command: []string{"/bin/sh", "-c", "sleep 30"},
			Taste: 5, Intelligence: 5, Cost: 5, Default: true, MaxConcurrent: 1,
		},
		{
			Name: "mock-sync", Harness: profile.HarnessMock,
			Taste: 5, Intelligence: 5, Cost: 10,
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resultJSON, err := Execute(ctx, Options{ScriptSrc: script, Store: store, Run: run, WorkDir: t.TempDir()})
	if err != nil || resultJSON != `"workflow done"` {
		t.Fatalf("Execute() = %s, %v", resultJSON, err)
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal := map[int]bool{}
	for _, event := range events {
		if event.T == "agent_end" && (event.ID == 1 || event.ID == 2) && event.Status == "error" &&
			(strings.Contains(event.Error, "canceled/timed out") || strings.Contains(event.Error, "context canceled")) {
			foundTerminal[event.ID] = true
		}
	}
	if !foundTerminal[1] || !foundTerminal[2] {
		t.Fatalf("running/queued worker terminal events were not persisted before Execute returned: %#v", events)
	}

	entries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].Kind != "start" || entries[len(entries)-1].Kind != "error" || entries[len(entries)-1].Source != "system" {
		t.Fatalf("dangling worker journal = %#v, want terminal system error", entries)
	}
}
