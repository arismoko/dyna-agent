package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dyna-agent/internal/profile"
)

func testModel() model {
	m := model{
		runs:  newRunsModel(""),
		profs: newProfilesModel(&profile.Store{}),
		guide: newGuideModel(""),
	}
	m.runs.catalogLoaded = true
	return m
}

// The footer help line's length depends on which tab/mode is active and is
// not itself bounded; at a narrow width it used to wrap onto extra lines
// that bodyHeight's fixed header+footer budget did not reserve, so the
// total rendered view exceeded the terminal height and the header scrolled
// out of view. The whole composed view must always be exactly the window
// height, regardless of width.
func TestViewNeverExceedsWindowHeight(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{30, 24}, {40, 24}, {50, 24}, {60, 10}, {80, 24}, {120, 24},
	} {
		mi, _ := testModel().Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m := mi.(model)
		view := m.View()
		if got := lipgloss.Height(view); got > size.h {
			t.Fatalf("width=%d height=%d: view rendered %d lines, overflowing the window:\n%s", size.w, size.h, got, view)
		}
		lines := strings.Split(view, "\n")
		if !strings.Contains(lines[0], "dyna") {
			t.Fatalf("width=%d height=%d: header scrolled out of the rendered view:\n%s", size.w, size.h, view)
		}
	}
}
