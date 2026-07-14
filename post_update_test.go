package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestPromptPostUpdateSetupAnswers(t *testing.T) {
	var out bytes.Buffer
	got, err := promptPostUpdateSetup(strings.NewReader("yes\nn\ny\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	want := postUpdateAnswers{Replace: true, Managed: false, Guidance: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("answers = %#v, want %#v", got, want)
	}
	for _, question := range []string{"Replace your local profiles", "Keep them automatically updated", "Install a short guidance block"} {
		if !strings.Contains(out.String(), question) {
			t.Fatalf("prompt output is missing %q: %s", question, out.String())
		}
	}
}

func TestShouldPromptPostUpdateNeverPromptsWorkersOrHeadlessCommands(t *testing.T) {
	tests := []struct {
		name                string
		command             string
		stdinTTY, stdoutTTY bool
		disabled, worker    bool
		version             string
		want                bool
	}{
		{name: "interactive root command", command: "list", stdinTTY: true, stdoutTTY: true, version: "v1.2.3", want: true},
		{name: "headless stdin", command: "list", stdoutTTY: true, version: "v1.2.3"},
		{name: "headless stdout", command: "list", stdinTTY: true, version: "v1.2.3"},
		{name: "dyna worker", command: "list", stdinTTY: true, stdoutTTY: true, worker: true, version: "v1.2.3"},
		{name: "journal", command: "journal", stdinTTY: true, stdoutTTY: true, version: "v1.2.3"},
		{name: "run", command: "run", stdinTTY: true, stdoutTTY: true, version: "v1.2.3"},
		{name: "update handles its own prompt", command: "update", stdinTTY: true, stdoutTTY: true, version: "v1.2.3"},
		{name: "internal apply", command: "_post-update-apply", stdinTTY: true, stdoutTTY: true, version: "v1.2.3"},
		{name: "disabled", command: "list", stdinTTY: true, stdoutTTY: true, disabled: true, version: "v1.2.3"},
		{name: "development build", command: "list", stdinTTY: true, stdoutTTY: true, version: "dev+abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPromptPostUpdate(tt.command, tt.stdinTTY, tt.stdoutTTY, tt.version, tt.disabled, tt.worker); got != tt.want {
				t.Fatalf("shouldPromptPostUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func TestDevNullIsNotTerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })
	if isTerminalFile(devNull) {
		t.Fatal("os.DevNull was detected as a terminal")
	}
}

func TestPostUpdateSetupNeverPromptsWorkers(t *testing.T) {
	if shouldOfferPostUpdateSetup(true, true, true) {
		t.Fatal("worker with terminal streams was allowed to receive setup prompts")
	}
}

func TestPostUpdateStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	want := postUpdateAnswers{Replace: true, Managed: true}
	if err := writePostUpdateState("v2.0.0", want); err != nil {
		t.Fatal(err)
	}
	got, err := readPostUpdateState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v2.0.0" || !reflect.DeepEqual(got.Answers, want) {
		t.Fatalf("state = %#v", got)
	}
}
