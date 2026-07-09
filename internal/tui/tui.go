// Package tui is the dyna dashboard: live workflow runs, worker profile
// configuration, and the scripting guide.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dyna-agent/internal/profile"
)

type tab int

const (
	tabRuns tab = iota
	tabProfiles
	tabGuide
)

var tabNames = []string{"Workflows", "Profiles", "Guide"}

type tickMsg time.Time

type model struct {
	tab    tab
	width  int
	height int
	frame  int

	runs  runsModel
	profs profilesModel
	guide guideModel
}

// Run starts the dashboard.
func Run(guideMD string) error {
	store, err := profile.Load(profile.DefaultPath())
	if err != nil {
		return err
	}
	m := model{
		runs:  newRunsModel(),
		profs: newProfilesModel(store),
		guide: newGuideModel(guideMD),
	}
	m.runs.refresh()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func tick() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.runs.setSize(msg.Width, m.bodyHeight())
		m.profs.setSize(msg.Width, m.bodyHeight())
		m.guide.setSize(msg.Width, m.bodyHeight())
		return m, nil

	case tickMsg:
		m.frame++
		if m.tab == tabRuns {
			m.runs.refresh()
		}
		return m, tick()

	case tea.KeyMsg:
		// While the profile form is open it owns the keyboard.
		if m.tab == tabProfiles && m.profs.editing {
			var cmd tea.Cmd
			m.profs, cmd = m.profs.update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			// Inside the run inspector, q backs out instead of quitting.
			if m.tab == tabRuns && m.runs.inspecting {
				break
			}
			return m, tea.Quit
		case "1":
			m.tab = tabRuns
			return m, nil
		case "2":
			m.tab = tabProfiles
			return m, nil
		case "3":
			m.tab = tabGuide
			return m, nil
		case "tab":
			m.tab = (m.tab + 1) % 3
			return m, nil
		}
		var cmd tea.Cmd
		switch m.tab {
		case tabRuns:
			m.runs, cmd = m.runs.update(msg)
		case tabProfiles:
			m.profs, cmd = m.profs.update(msg)
		case tabGuide:
			m.guide, cmd = m.guide.update(msg)
		}
		return m, cmd
	}
	if m.tab == tabGuide {
		var cmd tea.Cmd
		m.guide, cmd = m.guide.update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) bodyHeight() int {
	h := m.height - 3 // header + footer
	if h < 4 {
		h = 4
	}
	return h
}

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := m.viewHeader()
	var body, help string
	switch m.tab {
	case tabRuns:
		body = m.runs.view(m.frame)
		if m.runs.inspecting {
			help = helpLine("↑/↓", "agent", "pgup/pgdn/space", "scroll", "g/G", "top/bottom", "esc", "back")
		} else {
			help = helpLine("↑/↓", "select", "enter", "inspect", "p", "pause", "x", "cancel", "d", "delete", "1-3", "view", "q", "quit")
		}
	case tabProfiles:
		if m.profs.editing {
			body = m.profs.view()
			help = helpLine("↑/↓/tab", "field", "←/→", "adjust", "ctrl+s", "save", "esc", "cancel")
		} else {
			body = m.profs.view()
			help = helpLine("↑/↓", "select", "a", "add", "e", "edit", "d", "delete", "s", "set default", "q", "quit")
		}
	case tabGuide:
		body = m.guide.view()
		help = helpLine("↑/↓/pgup/pgdn", "scroll", "tab/1-3", "switch view", "q", "quit")
	}
	footer := lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(help)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m model) viewHeader() string {
	logo := sLogo.Render("⬡ dyna")
	tabs := " "
	for i, name := range tabNames {
		label := itoa(i+1) + " " + name
		st := sTab
		if tab(i) == m.tab {
			st = sTabActive
		}
		tabs += st.Render(label) + " "
	}
	running := ""
	if n := m.runs.runningCount(); n > 0 {
		running = sOK.Render(spinnerFrames[m.frame%len(spinnerFrames)] + " " +
			lipgloss.NewStyle().Bold(true).Render("") + itoa(n) + " running")
	}
	left := logo + tabs
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(running) - 1
	if gap < 1 {
		gap = 1
	}
	return left + lipgloss.NewStyle().Width(gap).Render("") + running
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// shared helpers ------------------------------------------------------------

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fmtDur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return d.Round(time.Second / 10).String()
	}
	return d.Round(time.Second).String()
}
