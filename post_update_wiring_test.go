package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestHeadlessRootCommandDoesNotPromptOrWriteConsentState(t *testing.T) {
	config := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)
	store := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{
		Name: "local", Description: "local", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })

	cmd := newRootCommand()
	cmd.SetIn(strings.NewReader("y\ny\ny\n"))
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"profiles", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "Replace your local profiles") {
		t.Fatalf("headless command prompted: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(data, "dyna", "update-consent.json")); !os.IsNotExist(err) {
		t.Fatalf("headless command wrote consent state: %v", err)
	}
}
