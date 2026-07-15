package profiles

import (
	"encoding/json"
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

func TestProfilesAddManagedFlagAndManualOverwrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runProfilesCommand(t, "add", "--name", "custom", "--harness", "mock", "--managed")
	show := captureStdout(t, func() { runProfilesCommand(t, "show", "custom") })
	if !strings.Contains(show, `"managed": true`) {
		t.Fatalf("show output omitted managed indicator: %s", show)
	}

	runProfilesCommand(t, "add", "--name", "custom", "--harness", "mock")
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.Get("custom")
	if !ok || p.Managed {
		t.Fatalf("manual overwrite did not clear managed: %#v, %v", p, ok)
	}
}

func TestBundledProfilesPolicy(t *testing.T) {
	var bundle struct {
		Profiles []profile.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(defaultProfilesJSON, &bundle); err != nil {
		t.Fatalf("parse bundled profiles: %v", err)
	}
	if len(bundle.Profiles) != 5 {
		t.Fatalf("bundled profile count = %d, want 5", len(bundle.Profiles))
	}

	disable := map[string]bool{"luna": true, "sol": true, "terra": true}
	efforts := map[string]string{"luna": "high", "terra": "high", "sol": "high", "sol-max": "xhigh"}
	seen := make(map[string]bool)
	var raw struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(defaultProfilesJSON, &raw); err != nil {
		t.Fatal(err)
	}
	rawByName := make(map[string]map[string]any)
	for _, p := range raw.Profiles {
		rawByName[p["name"].(string)] = p
	}
	for _, p := range bundle.Profiles {
		seen[p.Name] = true
		if !p.Managed {
			t.Errorf("%s is not managed", p.Name)
		}
		if p.DisableSubagents != disable[p.Name] {
			t.Errorf("%s disableSubagents = %v, want %v", p.Name, p.DisableSubagents, disable[p.Name])
		}
		_, hasKey := rawByName[p.Name]["disableSubagents"]
		if hasKey != disable[p.Name] {
			t.Errorf("%s disableSubagents key present = %v, want %v", p.Name, hasKey, disable[p.Name])
		}
		if want, ok := efforts[p.Name]; ok {
			got := ""
			for _, arg := range p.ExtraArgs {
				if strings.HasPrefix(arg, "model_reasoning_effort=") {
					got = strings.TrimPrefix(arg, "model_reasoning_effort=")
				}
			}
			if got != want {
				t.Errorf("%s reasoning effort = %q, want %q", p.Name, got, want)
			}
		}
	}
	for _, name := range []string{"fable", "luna", "sol", "sol-max", "terra"} {
		if !seen[name] {
			t.Errorf("missing bundled profile %s", name)
		}
	}
}

func TestProfilesInitRegistersManagedBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runProfilesCommand(t, "init")
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Profiles) != 5 {
		t.Fatalf("initialized profile count = %d, want 5", len(store.Profiles))
	}
	for _, p := range store.Profiles {
		if !p.Managed {
			t.Errorf("initialized profile %s is not managed", p.Name)
		}
	}
}

func runProfilesCommand(t *testing.T, args ...string) {
	t.Helper()
	cmd := NewCommand()
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
