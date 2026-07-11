package tui

import (
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestProfileFormPreservesDisableSubagents(t *testing.T) {
	p := profile.Profile{
		Name: "solo", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5,
		DisableSubagents: true,
	}
	form := newForm(p, p.Name)
	if got := form.toProfile(); !got.DisableSubagents {
		t.Fatalf("form profile = %#v", got)
	}
	if view := form.view(100, 40); !strings.Contains(view, "subagents") || !strings.Contains(view, "block") {
		t.Fatalf("form does not render subagent control:\n%s", view)
	}
	if blankProfile().DisableSubagents {
		t.Fatal("new profiles should allow subagents by default")
	}
}

func TestWizardSavesDisableSubagents(t *testing.T) {
	store := &profile.Store{Path: t.TempDir() + "/profiles.json"}
	w := newWizard()
	w.harness = profile.HarnessMock
	w.name.SetValue("solo")
	w.disableSubagents = true
	closed, saved, _ := w.save(store)
	if !closed || saved == nil || !saved.DisableSubagents {
		t.Fatalf("wizard save = %v, %#v", closed, saved)
	}
	w.step = stepFinal
	if view := w.view(100, 40); !strings.Contains(view, "subagents") || !strings.Contains(view, "block") {
		t.Fatalf("wizard does not render subagent control:\n%s", view)
	}
}
