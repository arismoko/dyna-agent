package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dyna-agent/internal/runstore"
)

type runsModel struct {
	width, height int
	runs          []runstore.Meta
	sel           int
	events        []runstore.Event
	result        string
	vp            viewport.Model
	follow        bool // stick to bottom while running

	// Inspector: drill into one run's agents to read full prompts/responses.
	inspecting bool
	journal    []runstore.JournalEntry
	agentSel   int
	ivp        viewport.Model

	confirm   string // pending confirmation: "delete" | "cancel"
	statusMsg string
}

func newRunsModel() runsModel {
	return runsModel{vp: viewport.New(0, 0), ivp: viewport.New(0, 0), follow: true}
}

func (m *runsModel) setSize(w, h int) {
	m.width, m.height = w, h
	m.vp.Width = m.detailWidth() - 4
	m.vp.Height = h - 2
	m.ivp.Width = m.detailWidth() - 4
	m.ivp.Height = h - 2
	if m.inspecting {
		m.loadInspect(false)
	}
}

func (m *runsModel) listWidth() int   { return clamp(m.width/3, 30, 48) }
func (m *runsModel) detailWidth() int { return m.width - m.listWidth() - 6 }

func (m *runsModel) refresh() {
	runs, err := runstore.List()
	if err != nil {
		return
	}
	m.runs = runs
	if m.sel >= len(m.runs) {
		m.sel = clamp(len(m.runs)-1, 0, 1<<30)
	}
	m.loadSelected()
	if m.inspecting && m.sel < len(m.runs) && m.runs[m.sel].Status == "running" {
		m.reloadJournal()
	}
}

func (m *runsModel) reloadJournal() {
	if m.sel < 0 || m.sel >= len(m.runs) {
		return
	}
	m.journal, _ = runstore.ReadJournal(m.runs[m.sel].ID)
	if m.agentSel >= len(m.journal) {
		m.agentSel = clamp(len(m.journal)-1, 0, 1<<30)
	}
	m.loadInspect(false)
}

// loadInspect renders the selected agent's full prompt + response into the
// inspector viewport. resetScroll jumps back to the top (agent switched).
func (m *runsModel) loadInspect(resetScroll bool) {
	w := m.detailWidth() - 4
	if m.agentSel < 0 || m.agentSel >= len(m.journal) {
		m.ivp.SetContent(sDim.Render("no agent calls recorded yet"))
		return
	}
	e := m.journal[m.agentSel]
	var b strings.Builder
	title := sTitle.Render(e.Label) + "  " + sProfTag.Render("["+e.Profile+"]")
	if e.Cached {
		title += sWarnS.Render("  ⚡cached")
	}
	b.WriteString(title + "\n\n")
	if e.Error != "" {
		b.WriteString(sErrS.Render("Error") + "\n" + wrap(e.Error, w) + "\n\n")
	}
	if e.Dir != "" {
		b.WriteString(sWarnS.Render("kept worktree: "+e.Dir) + "\n\n")
	}
	b.WriteString(sPhase.Render("▮ Prompt") + "\n")
	b.WriteString(wrap(e.Prompt, w) + "\n\n")
	b.WriteString(sPhase.Render("▮ Response") + "\n")
	b.WriteString(wrap(formatResult(e.Result), w) + "\n")
	m.ivp.SetContent(b.String())
	if resetScroll {
		m.ivp.GotoTop()
	}
}

func formatResult(v any) string {
	switch x := v.(type) {
	case nil:
		return "(none)"
	case string:
		return x
	default:
		b, err := json.MarshalIndent(x, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

func (m *runsModel) loadSelected() {
	if m.sel < 0 || m.sel >= len(m.runs) {
		m.events, m.result = nil, ""
		return
	}
	id := m.runs[m.sel].ID
	m.events, _ = runstore.ReadEvents(id)
	m.result, _ = runstore.ReadResult(id)
	m.vp.SetContent(m.renderDetail())
	if m.follow && m.runs[m.sel].Status == "running" {
		m.vp.GotoBottom()
	}
}

func (m *runsModel) runningCount() int {
	n := 0
	for _, r := range m.runs {
		if r.Status == "running" {
			n++
		}
	}
	return n
}

func (m runsModel) update(msg tea.KeyMsg) (runsModel, tea.Cmd) {
	if m.inspecting {
		switch msg.String() {
		case "esc", "backspace", "q":
			m.inspecting = false
		case "up", "k":
			if m.agentSel > 0 {
				m.agentSel--
				m.loadInspect(true)
			}
		case "down", "j":
			if m.agentSel < len(m.journal)-1 {
				m.agentSel++
				m.loadInspect(true)
			}
		case "g":
			m.ivp.GotoTop()
		case "G":
			m.ivp.GotoBottom()
		case "pgup", "u":
			m.ivp.HalfViewUp()
		case "pgdown", "d", " ":
			m.ivp.HalfViewDown()
		}
		return m, nil
	}
	if m.confirm != "" {
		if m.sel < len(m.runs) && (msg.String() == "y" || msg.String() == "Y") {
			id := m.runs[m.sel].ID
			var err error
			if m.confirm == "delete" {
				err = runstore.Remove(id)
			} else {
				err = runstore.Cancel(id)
			}
			if err != nil {
				m.statusMsg = "✗ " + err.Error()
			} else {
				m.statusMsg = "✓ " + m.confirm + "d " + id
			}
			m.refresh()
		}
		m.confirm = ""
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.sel > 0 {
			m.sel--
			m.follow = true
			m.loadSelected()
		}
	case "down", "j":
		if m.sel < len(m.runs)-1 {
			m.sel++
			m.follow = true
			m.loadSelected()
		}
	case "d":
		if m.sel < len(m.runs) {
			m.confirm = "delete"
			m.statusMsg = ""
		}
	case "x":
		if m.sel < len(m.runs) && m.runs[m.sel].Status == "running" {
			m.confirm = "cancel"
			m.statusMsg = ""
		}
	case "p":
		if m.sel < len(m.runs) && m.runs[m.sel].Status == "running" {
			id := m.runs[m.sel].ID
			paused := !runstore.IsPaused(id)
			runstore.SetPaused(id, paused)
			if paused {
				m.statusMsg = "⏸ paused " + id
			} else {
				m.statusMsg = "▶ resumed " + id
			}
		}
	case "enter":
		if m.sel < len(m.runs) {
			m.inspecting = true
			m.agentSel = 0
			m.reloadJournal()
			m.loadInspect(true)
		}
	case "r":
		m.refresh()
	case "g":
		m.vp.GotoTop()
		m.follow = false
	case "G":
		m.vp.GotoBottom()
		m.follow = true
	case "pgup":
		m.vp.HalfViewUp()
		m.follow = false
	case "pgdown":
		m.vp.HalfViewDown()
	}
	return m, nil
}

func (m runsModel) view(frame int) string {
	if m.inspecting {
		right := sPaneR.Width(m.detailWidth()).Height(m.height - 2).Render(m.ivp.View())
		return lipgloss.JoinHorizontal(lipgloss.Top, m.viewAgentList(), right)
	}
	left := m.viewList(frame)
	right := m.viewDetailPane(frame)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// viewAgentList is the inspector's left pane: every agent call in the run.
func (m runsModel) viewAgentList() string {
	w := m.listWidth()
	var b strings.Builder
	name := ""
	if m.sel < len(m.runs) {
		name = m.runs[m.sel].Name
	}
	b.WriteString(sTitle.Render("Agents") + sDim.Render(" · "+name) + "\n")
	if len(m.journal) == 0 {
		b.WriteString(sDim.Render("\nno agent calls recorded yet"))
	}
	maxRows := m.height - 4
	start := 0
	if m.agentSel >= maxRows {
		start = m.agentSel - maxRows + 1
	}
	for i := start; i < len(m.journal) && i-start < maxRows; i++ {
		e := m.journal[i]
		icon := sOK.Render("✓")
		if e.Error != "" {
			icon = sErrS.Render("✗")
		} else if e.Cached {
			icon = sWarnS.Render("⚡")
		}
		label := e.Label
		if lipgloss.Width(label) > w-8 {
			label = label[:w-9] + "…"
		}
		row := icon + " " + label
		if i == m.agentSel {
			row = icon + " " + sSel.Render(label)
		}
		row += "  " + sDim.Render(e.Profile)
		b.WriteString(row + "\n")
	}
	return sPaneL.Width(w).Height(m.height - 2).Render(b.String())
}

func (m runsModel) viewList(frame int) string {
	w := m.listWidth()
	var b strings.Builder
	b.WriteString(sTitle.Render("Runs") + "\n")
	if len(m.runs) == 0 {
		b.WriteString(sDim.Render("\nno runs yet\n\nstart one:\n  dyna run script.js\n  dyna demo"))
	}
	maxRows := m.height - 4
	start := 0
	if m.sel >= maxRows {
		start = m.sel - maxRows + 1
	}
	for i := start; i < len(m.runs) && i-start < maxRows; i++ {
		r := m.runs[i]
		status := r.Status
		if status == "running" && runstore.IsPaused(r.ID) {
			status = "paused"
		}
		icon := statusIcon(status, frame)
		name := r.Name
		if lipgloss.Width(name) > w-14 {
			name = name[:w-15] + "…"
		}
		meta := sDim.Render("  " + r.StartedAt.Format("Jan 02 15:04"))
		row := icon + " " + name + meta
		if i == m.sel {
			row = icon + " " + sSel.Render(name) + meta
		}
		b.WriteString(row + "\n")
	}
	if m.confirm != "" && m.sel < len(m.runs) {
		verb := m.confirm
		b.WriteString("\n" + sErrS.Render(verb+" "+m.runs[m.sel].Name+"? (y/n)"))
	} else if m.statusMsg != "" {
		b.WriteString("\n" + sDim.Render(m.statusMsg))
	}
	return sPaneL.Width(w).Height(m.height - 2).Render(b.String())
}

func statusIcon(status string, frame int) string {
	switch status {
	case "running":
		return sWarnS.Render(spinnerFrames[frame%len(spinnerFrames)])
	case "paused":
		return sWarnS.Render("⏸")
	case "ok":
		return sOK.Render("✓")
	case "error":
		return sErrS.Render("✗")
	case "canceled":
		return sDim.Render("◼")
	}
	return sDim.Render("·")
}

func (m *runsModel) viewDetailPane(frame int) string {
	m.vp.SetContent(m.renderDetailFrame(frame))
	return sPaneR.Width(m.detailWidth()).Height(m.height - 2).Render(m.vp.View())
}

func (m *runsModel) renderDetail() string { return m.renderDetailFrame(0) }

// agentState tracks one agent's lifecycle assembled from events.
type agentState struct {
	id      int
	label   string
	profile string
	phase   string
	status  string // queued|running|ok|error
	durMs   int64
	preview string
	errMsg  string
	cached  bool
}

func (m *runsModel) renderDetailFrame(frame int) string {
	if m.sel < 0 || m.sel >= len(m.runs) {
		return sDim.Render("select a run")
	}
	r := m.runs[m.sel]
	w := m.detailWidth() - 4

	var b strings.Builder
	title := sTitle.Render(r.Name) + "  " + sDim.Render(r.ID)
	b.WriteString(title + "\n")
	status := statusIcon(r.Status, frame) + " " + r.Status
	dur := ""
	if !r.EndedAt.IsZero() {
		dur = "  " + sDim.Render(r.EndedAt.Sub(r.StartedAt).Round(time.Second).String())
	} else if r.Status == "running" {
		dur = "  " + sDim.Render(time.Since(r.StartedAt).Round(time.Second).String())
	}
	b.WriteString(status + dur + "\n\n")

	// Assemble phases → agents, plus the log stream.
	phaseOrder := []string{}
	agentsByPhase := map[string][]*agentState{}
	agents := map[int]*agentState{}
	var logs []string
	for _, e := range m.events {
		switch e.T {
		case "phase":
			if _, seen := agentsByPhase[e.Title]; !seen {
				agentsByPhase[e.Title] = nil
				phaseOrder = append(phaseOrder, e.Title)
			}
		case "agent_start":
			a := &agentState{id: e.ID, label: e.Label, profile: e.Profile, phase: e.Phase, status: "queued", preview: e.Preview}
			agents[e.ID] = a
			if _, seen := agentsByPhase[a.phase]; !seen {
				phaseOrder = append(phaseOrder, a.phase)
			}
			agentsByPhase[a.phase] = append(agentsByPhase[a.phase], a)
		case "agent_run":
			if a := agents[e.ID]; a != nil {
				a.status = "running"
			}
		case "agent_end":
			if a := agents[e.ID]; a != nil {
				a.status = e.Status
				a.durMs = e.DurMs
				if e.Preview != "" {
					a.preview = e.Preview
				}
				a.errMsg = e.Error
				a.cached = e.Cached
			}
		case "log":
			logs = append(logs, e.Msg)
		}
	}

	for _, ph := range phaseOrder {
		name := ph
		if name == "" {
			name = "(no phase)"
		}
		as := agentsByPhase[ph]
		done := 0
		for _, a := range as {
			if a.status == "ok" || a.status == "error" {
				done++
			}
		}
		b.WriteString(sPhase.Render("▮ "+name) + sDim.Render(fmt.Sprintf("  %d/%d", done, len(as))) + "\n")
		for _, a := range as {
			icon := statusIcon(map[string]string{"queued": "queued", "running": "running", "ok": "ok", "error": "error"}[a.status], frame)
			if a.status == "queued" {
				icon = sDim.Render("◌")
			}
			line := fmt.Sprintf("  %s %s %s", icon, a.label, sProfTag.Render("["+a.profile+"]"))
			if a.cached {
				line += sWarnS.Render("  ⚡cached")
			} else if a.durMs > 0 {
				line += sDim.Render("  " + fmtDur(a.durMs))
			}
			b.WriteString(line + "\n")
			if a.status == "error" && a.errMsg != "" {
				b.WriteString(sErrS.Render("      "+truncLine(a.errMsg, w-8)) + "\n")
			} else if a.preview != "" && a.status == "ok" {
				b.WriteString(sDim.Render("      ↳ "+truncLine(a.preview, w-10)) + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(logs) > 0 {
		b.WriteString(sTitle.Render("Log") + "\n")
		for _, l := range logs {
			b.WriteString(sDim.Render("› ") + truncLine(l, w-4) + "\n")
		}
		b.WriteString("\n")
	}

	if r.Error != "" {
		b.WriteString(sErrS.Render("Error: "+r.Error) + "\n\n")
	}
	if m.result != "" {
		b.WriteString(sTitle.Render("Result") + "\n")
		res := strings.TrimSpace(m.result)
		if len(res) > 4000 {
			res = res[:4000] + "…"
		}
		b.WriteString(wrap(res, w) + "\n")
	}
	return b.String()
}

func truncLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n < 8 {
		n = 8
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func wrap(s string, w int) string {
	if w < 20 {
		w = 20
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for len(line) > w {
			out = append(out, line[:w])
			line = line[w:]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
