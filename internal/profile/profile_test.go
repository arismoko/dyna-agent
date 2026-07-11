package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
