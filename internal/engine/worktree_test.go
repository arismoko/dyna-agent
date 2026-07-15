package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestAddWorktreeKeepsCleanCommittedChanges(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	base := worktreeTestGit(t, repo, "rev-parse", "HEAD")

	wt, cleanup, err := addWorktree(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
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
