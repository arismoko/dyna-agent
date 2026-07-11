package tui

import "github.com/charmbracelet/lipgloss"

var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"}
	cAccent2 = lipgloss.AdaptiveColor{Light: "#0087AF", Dark: "#5FD7FF"}
	cOK      = lipgloss.AdaptiveColor{Light: "#00875F", Dark: "#3AD787"}
	cErr     = lipgloss.AdaptiveColor{Light: "#D70000", Dark: "#FF6B6B"}
	cWarn    = lipgloss.AdaptiveColor{Light: "#AF8700", Dark: "#F5C542"}
	cDim     = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	cText    = lipgloss.AdaptiveColor{Light: "#1C1C1C", Dark: "#DADADA"}
	cTaste   = lipgloss.AdaptiveColor{Light: "#AF00AF", Dark: "#E980E9"}
	cIntel   = lipgloss.AdaptiveColor{Light: "#005FD7", Dark: "#6CA9FF"}
	cCost    = lipgloss.AdaptiveColor{Light: "#00875F", Dark: "#3AD787"}
	// Header pill backgrounds: dark enough that fixed white text reads on
	// both light and dark terminals.
	cAccentBg  = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#4B47C9"}
	cAccent2Bg = lipgloss.AdaptiveColor{Light: "#0087AF", Dark: "#005F87"}

	// Header styles avoid theme-dependent dim colors: colored backgrounds
	// with fixed white text read on any terminal theme.
	sLogo = lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).Background(cAccentBg).Padding(0, 1)
	sTab       = lipgloss.NewStyle().Padding(0, 2)
	sTabActive = lipgloss.NewStyle().Padding(0, 2).Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent2Bg)

	sPaneL = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim).Padding(0, 1)
	sPaneR = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1)

	sSel     = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Background(lipgloss.AdaptiveColor{Light: "#EEEDFF", Dark: "#2B2A45"})
	sItem    = lipgloss.NewStyle().Foreground(cText)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sOK      = lipgloss.NewStyle().Foreground(cOK)
	sErrS    = lipgloss.NewStyle().Foreground(cErr)
	sWarnS   = lipgloss.NewStyle().Foreground(cWarn)
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sPhase   = lipgloss.NewStyle().Bold(true).Foreground(cAccent2)
	sBadge   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1C1C1C"}).Background(cAccent).Padding(0, 1).Bold(true)
	sHelp    = lipgloss.NewStyle().Foreground(cDim)
	sHelpKey = lipgloss.NewStyle().Foreground(cAccent2)
	sProfTag = lipgloss.NewStyle().Foreground(cAccent2)

	// Journal inspector: compact mode tabs and live-state badges stay legible
	// on both light and dark terminals without overwhelming the entry text.
	sInspectTab       = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sInspectTabActive = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).Background(cAccentBg).Padding(0, 1)
	sJournalKind = lipgloss.NewStyle().Bold(true).Foreground(cAccent2)
	sNext        = lipgloss.NewStyle().Bold(true).Foreground(cTaste)
	sFollow      = lipgloss.NewStyle().Bold(true).Foreground(cOK)
	sUnseen      = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).Background(cWarn).Padding(0, 1)
	sNudge = lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).Background(cTaste).Padding(0, 1)
	sNudgeUnavailable = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).Background(cErr).Padding(0, 1)
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// statBar renders "▰▰▰▰▰▰▱▱▱▱" for v out of 10 in the given color.
func statBar(v int, color lipgloss.AdaptiveColor) string {
	if v < 0 {
		v = 0
	}
	if v > 10 {
		v = 10
	}
	on := lipgloss.NewStyle().Foreground(color)
	off := lipgloss.NewStyle().Foreground(cDim)
	s := ""
	for i := 0; i < 10; i++ {
		if i < v {
			s += on.Render("▰")
		} else {
			s += off.Render("▱")
		}
	}
	return s
}

func helpLine(pairs ...string) string {
	out := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			out += sHelp.Render("  •  ")
		}
		out += sHelpKey.Render(pairs[i]) + sHelp.Render(" "+pairs[i+1])
	}
	return out
}
