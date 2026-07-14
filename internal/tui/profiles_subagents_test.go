package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestProfileFormManualEditClearsManaged(t *testing.T) {
	p := profile.Profile{
		Name: "managed", Description: "bundled", Harness: profile.HarnessMock,
		Taste: 5, Intelligence: 5, Cost: 5, Managed: true,
	}
	form := newForm(p, p.Name)
	form.setFocus(1)
	form.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := form.toProfile(); got.Managed {
		t.Fatalf("manual edit kept managed enabled: %#v", got)
	}

	form.setFocus(11)
	form.update(tea.KeyMsg{Type: tea.KeyRight})
	if got := form.toProfile(); !got.Managed {
		t.Fatalf("explicit managed toggle did not win: %#v", got)
	}
	if view := form.view(100, 40); !strings.Contains(view, "managed") {
		t.Fatalf("form does not render managed control:\n%s", view)
	}

	list := newProfilesModel(&profile.Store{Profiles: []profile.Profile{p}})
	list.setSize(100, 40)
	if view := list.view(); strings.Count(view, "managed") < 2 {
		t.Fatalf("profiles list does not render managed indicator:\n%s", view)
	}
}

func TestProfileFormRenameClearsManaged(t *testing.T) {
	p := profile.Profile{
		Name: "managed", Description: "bundled", Harness: profile.HarnessMock,
		Taste: 5, Intelligence: 5, Cost: 5, Managed: true,
	}
	form := newForm(p, p.Name)
	form.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := form.toProfile(); got.Managed {
		t.Fatalf("renamed profile kept managed enabled: %#v", got)
	}
}

func TestProfilesDeleteFinalItemKeepsValidSelection(t *testing.T) {
	store := &profile.Store{
		Path: t.TempDir() + "/profiles.json",
		Profiles: []profile.Profile{{
			Name: "only", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5,
		}},
	}
	m := newProfilesModel(store)
	m.setSize(100, 40)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(store.Profiles) != 0 || m.sel != 0 {
		t.Fatalf("after deleting final profile: len = %d, sel = %d", len(store.Profiles), m.sel)
	}
	if view := m.view(); !strings.Contains(view, "none yet") {
		t.Fatalf("empty profiles view did not render safely:\n%s", view)
	}
}

func TestProfilesListWindowsSelectionAndResize(t *testing.T) {
	profiles := make([]profile.Profile, 10)
	for i := range profiles {
		profiles[i] = profile.Profile{Name: fmt.Sprintf("profile-%02d", i), Harness: profile.HarnessMock}
	}
	m := newProfilesModel(&profile.Store{Profiles: profiles})
	m.sel = 6
	m.setSize(100, 8)

	view := m.view()
	if !strings.Contains(view, "profile-06") || !strings.Contains(view, "4-7 of 10") || strings.Contains(view, "profile-02") {
		t.Fatalf("selection below fold was not windowed:\n%s", view)
	}

	m.setSize(100, 6)
	view = m.view()
	if !strings.Contains(view, "profile-06") || !strings.Contains(view, "6-7 of 10") {
		t.Fatalf("resize did not keep selected profile visible:\n%s", view)
	}

	m.setSize(100, 0)
	if view = m.view(); !strings.Contains(view, "profile-06") || !strings.Contains(view, "7-7 of 10") {
		t.Fatalf("zero-height profile list did not clamp safely:\n%s", view)
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
