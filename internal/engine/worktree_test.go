package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestAddWorktreeKeepsCleanCommittedChanges(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	base := worktreeTestGit(t, repo, "rev-parse", "HEAD")

	wt, gotBase, cleanup, err := addWorktree(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if gotBase != base {
		t.Fatalf("addWorktree() base = %q, want %q", gotBase, base)
	}
	t.Cleanup(func() {
		gitRun(context.Background(), repo, "worktree", "remove", "--force", wt)
		os.RemoveAll(wt)
	})

	if err := os.WriteFile(filepath.Join(wt, "committed.txt"), []byte("committed change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, wt, "add", "committed.txt")
	worktreeTestGit(t, wt, "commit", "-m", "commit isolated work")
	if status := worktreeTestGit(t, wt, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree status = %q, want clean", status)
	}
	if head := worktreeTestGit(t, wt, "rev-parse", "HEAD"); head == base {
		t.Fatal("committed worktree HEAD did not advance from its base")
	}

	if kept := cleanup(); !kept {
		t.Fatal("cleanup removed a clean worktree containing a new commit")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("kept worktree is unavailable: %v", err)
	}
}

func TestExecuteRecordsRemovedWorktreeDistinctFromNoWorktree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-worktree-status")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	repo := initWorktreeTestRepo(t)
	script := `
await agent("isolated task", {profile: "mock-worktree", label: "isolated", isolation: "worktree"});
await agent("plain task", {profile: "mock-worktree", label: "plain"});
return "done";
`
	run, err := runstore.Create("engine worktree status test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "mock-worktree", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}

	if _, err := Execute(context.Background(), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: repo}); err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("journal entries = %#v, want two", entries)
	}
	if isolated := entries[0]; isolated.Label != "isolated" || isolated.Worktree != "removed" || isolated.Dir != "" {
		t.Fatalf("isolated journal entry = %#v, want removed worktree with no kept dir", isolated)
	}
	if plain := entries[1]; plain.Label != "plain" || plain.Worktree != "" || plain.Dir != "" {
		t.Fatalf("plain journal entry = %#v, want no worktree status", plain)
	}

	events, err := runstore.ReadEvents(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	end := make(map[string]runstore.Event)
	for _, event := range events {
		if event.T == "agent_end" {
			end[event.Label] = event
		}
	}
	if got := end["isolated"]; got.Worktree != "removed" || got.Dir != "" {
		t.Fatalf("isolated agent_end = %#v, want explicit removed status", got)
	}
	if got := end["plain"]; got.Worktree != "" || got.Dir != "" {
		t.Fatalf("plain agent_end = %#v, want no worktree status", got)
	}

	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range agentEntries {
		if entry.Kind == "git-commit" || entry.Kind == "git-diff" {
			t.Fatalf("unchanged isolated worktree journal contains git entry: %#v", entry)
		}
	}
}

func TestExecuteJournalsKeptWorktreeGitChanges(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-worktree-git-journal")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	repo := initWorktreeTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "deleted.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "modified.txt"), []byte("old one\nold two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, repo, "add", "deleted.txt", "modified.txt")
	worktreeTestGit(t, repo, "commit", "-m", "add worktree fixtures")
	base := worktreeTestGit(t, repo, "rev-parse", "HEAD")

	const script = `return await agent("change isolated files", {profile: "worktree-writer", label: "writer", isolation: "worktree"});`
	run, err := runstore.Create("engine worktree git journal test", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "worktree-writer", Harness: profile.HarnessCustom, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
		Command: []string{"/bin/sh", "-c", `
printf 'untracked\n' > created.txt
rm deleted.txt
printf 'new one\nnew two\nnew three\n' > modified.txt
printf 'first\n' > committed-one.txt
git add committed-one.txt
git commit -m 'first isolated commit' >/dev/null
printf 'second\n' > committed-two.txt
git add committed-two.txt
git commit -m 'second isolated commit' >/dev/null
printf done
`},
	}}}

	if _, err := Execute(context.Background(), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: repo}); err != nil {
		t.Fatal(err)
	}
	rootEntries, err := runstore.ReadJournal(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootEntries) != 1 || rootEntries[0].Worktree != "kept" || rootEntries[0].Dir == "" {
		t.Fatalf("root journal entries = %#v, want one kept worktree", rootEntries)
	}
	keptDir := rootEntries[0].Dir
	t.Cleanup(func() {
		gitRun(context.Background(), repo, "worktree", "remove", "--force", keptDir)
		os.RemoveAll(keptDir)
	})

	journalPath := filepath.Join(run.Dir, "agents", "1", "journal.jsonl")
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("agent journal was not written at %s: %v", journalPath, err)
	}
	agentEntries, _, err := runstore.ReadAgentJournalFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var commits, diffs []string
	completeIndex := -1
	for i, entry := range agentEntries {
		switch entry.Kind {
		case "git-commit":
			if entry.Source != "system" {
				t.Fatalf("commit entry source = %q, want system", entry.Source)
			}
			commits = append(commits, entry.Message)
		case "git-diff":
			if entry.Source != "system" {
				t.Fatalf("diff entry source = %q, want system", entry.Source)
			}
			diffs = append(diffs, entry.Message)
		case "complete":
			completeIndex = i
		}
	}
	wantCommits := strings.Split(worktreeTestGit(t, keptDir, "log", "--reverse", "--format=%h %s", base+"..HEAD"), "\n")
	if !reflect.DeepEqual(commits, wantCommits) {
		t.Fatalf("git-commit messages = %#v, want %#v", commits, wantCommits)
	}
	wantDiffs := map[string]bool{
		"created committed-one.txt +1/-0": true,
		"created committed-two.txt +1/-0": true,
		"created created.txt":             true,
		"deleted deleted.txt":             true,
		"modified modified.txt +3/-2":     true,
	}
	if len(diffs) != len(wantDiffs) {
		t.Fatalf("git-diff messages = %#v, want %#v", diffs, wantDiffs)
	}
	for _, message := range diffs {
		if !wantDiffs[message] {
			t.Errorf("unexpected git-diff message %q; want %#v", message, wantDiffs)
		}
	}
	if completeIndex < 0 {
		t.Fatalf("agent journal has no complete entry: %#v", agentEntries)
	}
	for i, entry := range agentEntries {
		if (entry.Kind == "git-commit" || entry.Kind == "git-diff") && i > completeIndex {
			t.Fatalf("git entry at index %d follows complete entry at index %d: %#v", i, completeIndex, agentEntries)
		}
	}
}

func TestWorktreeGitJournalEntriesCapEachCategory(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	wt, base, _, err := addWorktree(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gitRun(context.Background(), repo, "worktree", "remove", "--force", wt)
		os.RemoveAll(wt)
	})

	for i := 0; i < worktreeJournalEntryLimit+2; i++ {
		name := fmt.Sprintf("committed-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(wt, name), []byte(fmt.Sprintf("line %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		worktreeTestGit(t, wt, "add", name)
		worktreeTestGit(t, wt, "commit", "-m", fmt.Sprintf("isolated commit %02d", i))
	}
	entries, err := worktreeGitJournalEntries(context.Background(), wt, base)
	if err != nil {
		t.Fatal(err)
	}
	var commits, diffs []string
	for _, entry := range entries {
		switch entry.Kind {
		case "git-commit":
			commits = append(commits, entry.Message)
		case "git-diff":
			diffs = append(diffs, entry.Message)
		}
	}
	if len(commits) != worktreeJournalEntryLimit+1 || commits[len(commits)-1] != "...and 2 more commits" {
		t.Fatalf("capped commit entries = %#v", commits)
	}
	if len(diffs) != worktreeJournalEntryLimit+1 || diffs[len(diffs)-1] != "...and 2 more changed files" {
		t.Fatalf("capped diff entries = %#v", diffs)
	}
}

func initWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	worktreeTestGit(t, repo, "init")
	worktreeTestGit(t, repo, "config", "user.name", "Dyna Test")
	worktreeTestGit(t, repo, "config", "user.email", "dyna-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeTestGit(t, repo, "add", "base.txt")
	worktreeTestGit(t, repo, "commit", "-m", "base")
	return repo
}

func worktreeTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
