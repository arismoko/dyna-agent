package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDisableSubagentsSerializationCompatibility(t *testing.T) {
	legacy := []byte(`{"version":2,"profiles":[{"name":"legacy","description":"","harness":"mock","taste":5,"intelligence":5,"cost":5}]}`)
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Profiles[0].DisableSubagents {
		t.Fatal("legacy profile unexpectedly disables subagents")
	}
	store.Profiles[0].DisableSubagents = true
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Profiles[0].DisableSubagents {
		t.Fatal("disableSubagents did not round-trip")
	}
	b, err := json.Marshal(Profile{Name: "default-false"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "disableSubagents") {
		t.Fatalf("false disableSubagents was not omitted: %s", b)
	}
}

func TestDefaultProfileDoesNotChangeDisableSubagents(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Name: "blocked", Default: true, DisableSubagents: true},
		{Name: "allowed"},
	}}
	p, ok := store.DefaultProfile()
	if !ok || p.Name != "blocked" || !p.DisableSubagents {
		t.Fatalf("DefaultProfile() = %#v, %v", p, ok)
	}
}

func TestLoadRefreshesManagedProfileAndPreservesUserState(t *testing.T) {
	bundle := []byte(`{"profiles":[{"name":"managed","description":"bundled","harness":"mock","model":"new","taste":8,"intelligence":9,"cost":7,"extraArgs":["--new"],"command":["new"],"env":{"NEW":"1"},"timeoutSec":42,"safeMode":true,"disableSubagents":true,"maxConcurrent":3,"maxCallsPerRun":4,"managed":true}]}`)
	if err := SetBundledDefaults(bundle); err != nil {
		t.Fatal(err)
	}
	defer SetBundledDefaults(nil)

	path := filepath.Join(t.TempDir(), "profiles.json")
	store := &Store{Path: path, Version: storeVersion, Profiles: []Profile{{
		Name: "managed", Description: "drifted", Harness: HarnessMock, Model: "old",
		Taste: 1, Intelligence: 2, Cost: 3, ExtraArgs: []string{"--old"},
		Command: []string{"old"}, Env: map[string]string{"OLD": "1"}, TimeoutSec: 1,
		Default: true, Managed: true, Disabled: true,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := got.Get("managed")
	if !ok {
		t.Fatal("managed profile disappeared")
	}
	want := Profile{
		Name: "managed", Description: "bundled", Harness: HarnessMock, Model: "new",
		Taste: 8, Intelligence: 9, Cost: 7, ExtraArgs: []string{"--new"},
		Command: []string{"new"}, Env: map[string]string{"NEW": "1"}, TimeoutSec: 42,
		SafeMode: true, DisableSubagents: true, MaxConcurrent: 3, MaxCallsPerRun: 4,
		Default: true, Managed: true, Disabled: true,
	}
	if !reflect.DeepEqual(*p, want) {
		t.Fatalf("refreshed profile = %#v, want %#v", *p, want)
	}
}

func TestLoadLeavesUnmanagedProfilesAlone(t *testing.T) {
	if err := SetBundledDefaults([]byte(`{"profiles":[{"name":"custom","description":"bundled","harness":"mock","taste":9,"intelligence":9,"cost":9,"managed":true}]}`)); err != nil {
		t.Fatal(err)
	}
	defer SetBundledDefaults(nil)

	path := filepath.Join(t.TempDir(), "profiles.json")
	want := Profile{Name: "custom", Description: "mine", Harness: HarnessMock, Taste: 5, Intelligence: 6, Cost: 7}
	store := &Store{Path: path, Version: storeVersion, Profiles: []Profile{want}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Profiles, []Profile{want}) {
		t.Fatalf("unmanaged profiles changed: %#v", got.Profiles)
	}
}

func TestLoadManagedNoOpDoesNotRewriteStore(t *testing.T) {
	bundle := []byte(`{"profiles":[{"name":"managed","description":"bundled","harness":"mock","taste":5,"intelligence":6,"cost":7,"managed":true}]}`)
	if err := SetBundledDefaults(bundle); err != nil {
		t.Fatal(err)
	}
	defer SetBundledDefaults(nil)

	path := filepath.Join(t.TempDir(), "profiles.json")
	store := &Store{Path: path, Version: storeVersion, Profiles: []Profile{{
		Name: "managed", Description: "bundled", Harness: HarnessMock,
		Taste: 5, Intelligence: 6, Cost: 7, Managed: true,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("unchanged store was rewritten: modtime = %v, want %v", info.ModTime(), old)
	}
}

func TestLoadDoesNotAddDeletedBundledProfiles(t *testing.T) {
	if err := SetBundledDefaults([]byte(`{"profiles":[{"name":"deleted","description":"bundled","harness":"mock","taste":5,"intelligence":5,"cost":5,"managed":true}]}`)); err != nil {
		t.Fatal(err)
	}
	defer SetBundledDefaults(nil)

	path := filepath.Join(t.TempDir(), "profiles.json")
	store := &Store{Path: path, Version: storeVersion, Profiles: []Profile{{
		Name: "other", Description: "mine", Harness: HarnessMock,
		Taste: 5, Intelligence: 5, Cost: 5,
	}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Get("deleted"); ok {
		t.Fatal("Load resurrected a deleted bundled profile")
	}
}

func TestApplyBundledPreferencesAnswerCombinations(t *testing.T) {
	bundle := []byte(`{"profiles":[{"name":"bundled","description":"release","harness":"mock","model":"new","taste":9,"intelligence":8,"cost":7,"managed":true}]}`)
	if err := SetBundledDefaults(bundle); err != nil {
		t.Fatal(err)
	}
	defer SetBundledDefaults(nil)

	for _, tt := range []struct {
		name             string
		replace, managed bool
		wantDescription  string
		wantModel        string
		wantManaged      bool
	}{
		{name: "decline both", wantDescription: "local", wantModel: "old"},
		{name: "replace once", replace: true, wantDescription: "release", wantModel: "new"},
		{name: "manage without immediate replace", managed: true, wantDescription: "local", wantModel: "old", wantManaged: true},
		{name: "replace and manage", replace: true, managed: true, wantDescription: "release", wantModel: "new", wantManaged: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &Store{Profiles: []Profile{
				{Name: "bundled", Description: "local", Harness: HarnessMock, Model: "old", Taste: 1, Intelligence: 2, Cost: 3, Default: true, Disabled: true, Managed: true},
				{Name: "custom", Description: "mine", Harness: HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Managed: true},
			}}
			if collisions := store.ApplyBundledPreferences(tt.replace, tt.managed); collisions != 1 {
				t.Fatalf("collisions = %d, want 1", collisions)
			}
			got, _ := store.Get("bundled")
			if got.Description != tt.wantDescription || got.Model != tt.wantModel || got.Managed != tt.wantManaged {
				t.Fatalf("bundled profile = %#v", got)
			}
			if !got.Default || !got.Disabled {
				t.Fatalf("user flags were not preserved: %#v", got)
			}
			custom, _ := store.Get("custom")
			if !custom.Managed || custom.Description != "mine" {
				t.Fatalf("non-colliding profile changed: %#v", custom)
			}
		})
	}
}
