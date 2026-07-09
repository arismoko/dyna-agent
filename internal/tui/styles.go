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

	sLogo      = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTab       = lipgloss.NewStyle().Padding(0, 2).Foreground(cDim)
	sTabActive = lipgloss.NewStyle().Padding(0, 2).Bold(true).
			Foreground(cAccent).Underline(true)

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
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// statBar renders "▰▰▰▰▱" for v out of 5 in the given color.
func statBar(v int, color lipgloss.AdaptiveColor) string {
	if v < 0 {
		v = 0
	}
	if v > 5 {
		v = 5
	}
	on := lipgloss.NewStyle().Foreground(color)
	off := lipgloss.NewStyle().Foreground(cDim)
	s := ""
	for i := 0; i < 5; i++ {
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
