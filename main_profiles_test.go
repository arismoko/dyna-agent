package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestProfilesAddDisableSubagentsFlagAndJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runProfilesCommand(t, "add", "--name", "solo", "--harness", "mock", "--disable-subagents")
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.Get("solo")
	if !ok || !p.DisableSubagents {
		t.Fatalf("saved profile = %#v, %v", p, ok)
	}
	list := captureStdout(t, func() { runProfilesCommand(t, "list", "--json") })
	show := captureStdout(t, func() { runProfilesCommand(t, "show", "solo") })
	if !strings.Contains(list, `"disableSubagents": true`) || !strings.Contains(show, `"disableSubagents": true`) {
		t.Fatalf("JSON output omitted disableSubagents: list=%s show=%s", list, show)
	}

	runProfilesCommand(t, "add", "--name", "solo", "--harness", "mock", "--disable-subagents=false")
	store, err = profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, _ = store.Get("solo")
	if p.DisableSubagents {
		t.Fatal("--disable-subagents=false did not restore the compatible default")
	}
}

func runProfilesCommand(t *testing.T, args ...string) {
	t.Helper()
	cmd := profilesCmd()
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("profiles %v: %v", args, err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
