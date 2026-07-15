package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	profilescli "dyna-agent/internal/cli/profiles"
	"dyna-agent/internal/profile"
)

func TestPromptPostUpdateSetupAnswers(t *testing.T) {
	var out bytes.Buffer
	got, err := promptPostUpdateSetup(strings.NewReader("yes\nn\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	want := postUpdateAnswers{Replace: true, Managed: false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("answers = %#v, want %#v", got, want)
	}
	for _, question := range []string{"Replace your local profiles", "Keep them automatically updated"} {
		if !strings.Contains(out.String(), question) {
			t.Fatalf("prompt output is missing %q: %s", question, out.String())
		}
	}
	if strings.Contains(out.String(), "guidance") || strings.Contains(out.String(), "CLAUDE.md") || strings.Contains(out.String(), "AGENTS.md") {
		t.Fatalf("prompt still offers shared guidance installation: %s", out.String())
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

func TestPostUpdateApplyCommandHasNoGuidanceFlag(t *testing.T) {
	cmd := NewPostUpdateApplyCommand(func() string { return "v0.0.0" })
	if flag := cmd.Flags().Lookup("guidance"); flag != nil {
		t.Fatalf("retired guidance flag is still registered: %#v", flag)
	}
}

func TestRecurringSetupRefreshesOnlyStillManagedProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	bundle := []byte(`{"profiles":[
		{"name":"managed","description":"release","harness":"mock","model":"new","taste":8,"intelligence":9,"cost":7,"managed":true},
		{"name":"opted-out","description":"release","harness":"mock","model":"new","taste":8,"intelligence":9,"cost":7,"managed":true}
	]}`)
	if err := profile.SetBundledDefaults(bundle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profile.SetBundledDefaults(profilescli.BundledDefaults()) })
	store := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{
		{Name: "managed", Description: "old", Harness: profile.HarnessMock, Model: "old", Taste: 1, Intelligence: 2, Cost: 3, Managed: true},
		{Name: "opted-out", Description: "local", Harness: profile.HarnessMock, Model: "mine", Taste: 4, Intelligence: 5, Cost: 6},
	}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	// Legacy consent may say replace/manage were accepted once. Recurring setup
	// must not replay those choices after the user later opted a profile out.
	if err := applyRecurringPostUpdateSetup(postUpdateAnswers{Replace: true, Managed: true}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	managed, _ := got.Get("managed")
	if managed.Model != "new" || managed.Description != "release" || !managed.Managed {
		t.Fatalf("managed profile was not refreshed: %#v", managed)
	}
	optedOut, _ := got.Get("opted-out")
	if optedOut.Model != "mine" || optedOut.Description != "local" || optedOut.Managed {
		t.Fatalf("recurring setup replayed one-time consent: %#v", optedOut)
	}
}

func TestExplicitUpdateReusesLegacyConsentWithoutTerminalPrompt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	legacy := []byte("{\n  \"version\": \"v1.0.0\",\n  \"answers\": {\"replace\": true, \"managed\": false, \"guidance\": true}\n}\n")
	if err := os.MkdirAll(filepath.Dir(postUpdateStatePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(postUpdateStatePath(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "args")
	executable := filepath.Join(t.TempDir(), "dyna-new")
	writeExecutable(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n")
	t.Setenv("CAPTURE_ARGS", capture)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader("n\nn\n"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	OfferSetupAfterUpdate(cmd, executable, "v2.0.0")
	got := readFile(t, capture)
	for _, want := range []string{"--replace=true", "--managed=false", "--stamp-version=v2.0.0", "--recurring=true"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("explicit update did not reuse consent %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--guidance") {
		t.Fatalf("explicit update forwarded retired guidance consent:\n%s", got)
	}
}

func TestInvalidConsentStateWithoutVersionIsRejected(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(postUpdateStatePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(postUpdateStatePath(), []byte(`{"answers":{"guidance":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPostUpdateState(); err == nil {
		t.Fatal("consent state without a version was accepted")
	}
}
