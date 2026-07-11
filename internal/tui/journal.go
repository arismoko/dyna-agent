package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"dyna-agent/internal/runstore"
)

type inspectMode int

const (
	inspectJournal inspectMode = iota
	inspectTask
	inspectResult
	inspectModeCount
)

func (m inspectMode) label() string {
	switch m {
	case inspectTask:
		return "Task"
	case inspectResult:
		return "Result"
	default:
		return "Journal"
	}
}

func (m *runsModel) inspectorAgentCount() int {
	return len(m.agentOrder) + len(m.legacyAgentOrder)
}

func (m *runsModel) inspectorAgentAt(index int) *agentState {
	if index < 0 {
		return nil
	}
	if index < len(m.agentOrder) {
		return m.agentOrder[index]
	}
	index -= len(m.agentOrder)
	if index < len(m.legacyAgentOrder) {
		return m.legacyAgentOrder[index]
	}
	return nil
}

func (m *runsModel) selectedAgent() *agentState {
	return m.inspectorAgentAt(m.agentSel)
}

func (m *runsModel) selectedAgentID() int {
	if a := m.selectedAgent(); a != nil {
		return a.id
	}
	return 0
}

func (m *runsModel) inspectorAgentIndex(id int) int {
	if id == 0 {
		return -1
	}
	for i, a := range m.agentOrder {
		if a.id == id {
			return i
		}
	}
	for i, a := range m.legacyAgentOrder {
		if a.id == id {
			return len(m.agentOrder) + i
		}
	}
	return -1
}

// reconcileAgentSelection preserves the selected agent by ID while lifecycle
// and completion records arrive independently. It reports whether the actual
// selected ID changed and therefore needs a new per-agent journal tail.
func (m *runsModel) reconcileAgentSelection(preferredID int) bool {
	if n := m.inspectorAgentCount(); n == 0 {
		m.agentSel = 0
		return preferredID != 0
	}
	if index := m.inspectorAgentIndex(preferredID); index >= 0 {
		m.agentSel = index
	} else {
		m.agentSel = clamp(m.agentSel, 0, m.inspectorAgentCount()-1)
	}
	return m.selectedAgentID() != preferredID
}

func (m *runsModel) removeLegacyAgent(id int) {
	for i, a := range m.legacyAgentOrder {
		if a.id != id {
			continue
		}
		copy(m.legacyAgentOrder[i:], m.legacyAgentOrder[i+1:])
		m.legacyAgentOrder = m.legacyAgentOrder[:len(m.legacyAgentOrder)-1]
		return
	}
}

func (m *runsModel) addLegacyAgent(completion *runstore.JournalEntry) *agentState {
	if completion == nil {
		return nil
	}
	if a := m.agents[completion.ID]; a != nil {
		a.completion = completion
		if !a.started {
			populateLegacyAgent(a, completion)
		}
		return a
	}
	a := &agentState{id: completion.ID, completion: completion}
	populateLegacyAgent(a, completion)
	m.agents[a.id] = a
	m.legacyAgentOrder = append(m.legacyAgentOrder, a)
	return a
}

func populateLegacyAgent(a *agentState, completion *runstore.JournalEntry) {
	a.label = completion.Label
	a.profile = completion.Profile
	a.cached = completion.Cached
	a.errMsg = completion.Error
	if completion.Error != "" {
		a.status = "error"
	} else {
		a.status = "ok"
	}
}

func (m *runsModel) resetCompletions() {
	m.journal = nil
	m.completionByID = make(map[int]*runstore.JournalEntry)
	m.completionOrder = nil
	for id, a := range m.agents {
		a.completion = nil
		if !a.started {
			delete(m.agents, id)
		}
	}
	m.legacyAgentOrder = nil
}

func (m *runsModel) applyCompletions(entries []runstore.JournalEntry) {
	for i := range entries {
		completion := entries[i]
		if _, exists := m.completionByID[completion.ID]; !exists {
			m.completionOrder = append(m.completionOrder, completion.ID)
		}
		copyOfCompletion := completion
		m.completionByID[completion.ID] = &copyOfCompletion
		if a := m.agents[completion.ID]; a != nil {
			a.completion = &copyOfCompletion
			if !a.started {
				populateLegacyAgent(a, &copyOfCompletion)
			} else {
				if a.label == "" {
					a.label = copyOfCompletion.Label
				}
				if a.profile == "" {
					a.profile = copyOfCompletion.Profile
				}
			}
		} else {
			m.addLegacyAgent(&copyOfCompletion)
		}
	}
}

func (m *runsModel) resetAgentJournal() {
	m.agentJournal = nil
	m.agentJournalOffset = 0
	m.agentJournalComplete = false
	m.agentJournalMissing = false
	m.agentJournalLoaded = false
	m.journalFollow = true
	m.journalUnseen = 0
	m.inspectOffsets = [inspectModeCount]int{}
}

func (m *runsModel) setInspectMode(next inspectMode) {
	if next < inspectJournal || next >= inspectModeCount || next == m.inspectMode {
		return
	}
	m.inspectOffsets[m.inspectMode] = m.ivp.YOffset
	m.inspectMode = next
	m.ivp.SetContent(m.renderInspectBody())
	if next == inspectJournal && m.journalFollow {
		m.ivp.GotoBottom()
		m.journalUnseen = 0
	} else {
		m.ivp.SetYOffset(m.inspectOffsets[next])
	}
}

func (m *runsModel) loadInspect(resetScroll bool) {
	y := m.ivp.YOffset
	m.ivp.SetContent(m.renderInspectBody())
	if resetScroll {
		if m.inspectMode == inspectJournal && m.journalFollow {
			m.ivp.GotoBottom()
			m.journalUnseen = 0
		} else {
			m.ivp.GotoTop()
		}
		return
	}
	if m.inspectMode == inspectJournal && m.journalFollow {
		m.ivp.GotoBottom()
		m.journalUnseen = 0
	} else {
		m.ivp.SetYOffset(y)
	}
}

func (m runsModel) renderInspectHeader() string {
	a := m.selectedAgent()
	if a == nil {
		return sTitle.Render("Agent journal")
	}
	w := max(1, m.detailWidth()-4)
	label := a.label
	if label == "" {
		label = fmt.Sprintf("agent #%d", a.id)
	}
	identity := agentStatusIcon(a.status) + " " + sTitle.Render(label)
	if a.profile != "" {
		identity += " " + sProfTag.Render("["+a.profile+"]")
	}
	if a.phase != "" {
		identity += sDim.Render(" · " + a.phase)
	}
	identity = ansi.Truncate(identity, w, "…")

	var tabs strings.Builder
	for mode := inspectJournal; mode < inspectModeCount; mode++ {
		if mode > inspectJournal {
			tabs.WriteString(" ")
		}
		style := sInspectTab
		if mode == m.inspectMode {
			style = sInspectTabActive
		}
		tabs.WriteString(style.Render(mode.label()))
		if mode == inspectJournal && m.journalUnseen > 0 {
			tabs.WriteString(sUnseen.Render(fmt.Sprintf("%d UNSEEN", m.journalUnseen)))
		}
	}
	if m.inspectMode == inspectJournal {
		if m.journalFollow {
			tabs.WriteString("  " + sFollow.Render("● FOLLOW"))
		} else {
			tabs.WriteString("  " + sDim.Render("FOLLOW OFF"))
		}
	}
	return identity + "\n" + ansi.Truncate(tabs.String(), w, "…")
}

func (m runsModel) renderInspectBody() string {
	if m.selectedAgent() == nil {
		if !m.catalogLoaded {
			return sDim.Render("loading agent journals…")
		}
		return sDim.Render("no agents have started yet")
	}
	switch m.inspectMode {
	case inspectTask:
		return m.renderInspectTask()
	case inspectResult:
		return m.renderInspectResult()
	default:
		return m.renderAgentJournal()
	}
}

func (m runsModel) renderAgentJournal() string {
	a := m.selectedAgent()
	w := max(20, m.detailWidth()-6)
	if len(m.agentJournal) == 0 {
		switch {
		case a.completion != nil && a.completion.Cached:
			return sWarnS.Render("⚡ cached result") + "\n\n" + sDim.Render("No live work journal was produced for this resumed agent call.")
		case agentDone(a.status):
			return sDim.Render("no work journal was recorded for this agent")
		case m.agentJournalLoaded || m.agentJournalMissing:
			return sDim.Render("waiting for the first journal entry…")
		default:
			return sDim.Render("loading agent journal…")
		}
	}

	var b strings.Builder
	lastPhase := ""
	for i, entry := range m.agentJournal {
		if entry.Phase != "" && entry.Phase != lastPhase {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(sPhase.Render("▮ "+entry.Phase) + "\n")
			lastPhase = entry.Phase
		}
		kind := strings.TrimSpace(entry.Kind)
		if kind == "" {
			kind = "note"
		}
		meta := journalTime(entry.TS)
		if entry.Source != "" {
			meta += " · " + entry.Source
		}
		b.WriteString(sJournalKind.Render(strings.ToUpper(kind)) + sDim.Render("  "+meta) + "\n")
		message := strings.TrimSpace(entry.Message)
		if message == "" {
			message = "(empty entry)"
		}
		b.WriteString(wrap(message, w) + "\n")
		if next := strings.TrimSpace(entry.Next); next != "" {
			b.WriteString(sNext.Render("next → ") + wrap(next, max(20, w-7)) + "\n")
		}
		if i < len(m.agentJournal)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m runsModel) renderInspectTask() string {
	a := m.selectedAgent()
	prompt := ""
	if a.completion != nil {
		prompt = a.completion.Prompt
	}
	if prompt == "" {
		for i := len(m.agentJournal) - 1; i >= 0; i-- {
			if m.agentJournal[i].Prompt != "" {
				prompt = m.agentJournal[i].Prompt
				break
			}
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return sDim.Render("task prompt is not available yet")
	}
	return sPhase.Render("▮ Task prompt") + "\n\n" + wrap(prompt, max(20, m.detailWidth()-6))
}

func (m runsModel) renderInspectResult() string {
	a := m.selectedAgent()
	if a.completion == nil {
		if agentDone(a.status) {
			return sDim.Render("final response was not recorded")
		}
		return sDim.Render("waiting for the final response…")
	}
	e := a.completion
	w := max(20, m.detailWidth()-6)
	var b strings.Builder
	if e.Cached {
		b.WriteString(sWarnS.Render("⚡ cached completion") + "\n\n")
	}
	if e.Error != "" {
		b.WriteString(sErrS.Render("Error") + "\n" + wrap(e.Error, w) + "\n\n")
	}
	if e.Dir != "" {
		b.WriteString(sWarnS.Render("kept worktree: "+e.Dir) + "\n\n")
	}
	b.WriteString(sPhase.Render("▮ Final response") + "\n\n")
	b.WriteString(wrap(formatResult(e.Result), w))
	return b.String()
}

func journalTime(ts int64) string {
	if ts <= 0 {
		return "time unknown"
	}
	return time.UnixMilli(ts).Format("15:04:05")
}

func journalFreshness(ts int64) string {
	if ts <= 0 {
		return ""
	}
	age := time.Since(time.UnixMilli(ts))
	if age < 0 || age < 5*time.Second {
		return "now"
	}
	if age < time.Minute {
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
	return time.UnixMilli(ts).Format("Jan 02")
}

func nudgeLabel(a *agentState) string {
	if nudgeUnavailable(a) {
		return "NUDGE UNAVAILABLE"
	}
	if a != nil && strings.EqualFold(strings.TrimSpace(a.nudgeStatus), "ignored") {
		return "NO JOURNAL ENTRY"
	}
	return "NUDGED"
}

func agentJournalSummary(a *agentState) string {
	if a == nil {
		return ""
	}
	count := journalEntryCount(a)
	if count > 0 {
		label := "entries"
		if count == 1 {
			label = "entry"
		}
		summary := fmt.Sprintf("%d %s", count, label)
		if fresh := journalFreshness(a.journalTS); fresh != "" {
			summary += " · " + fresh
		}
		return summary
	}
	if a.cached {
		return "cached · no live journal"
	}
	if agentDone(a.status) {
		return "no journal"
	}
	return "waiting for journal"
}

func journalEntryCount(a *agentState) int {
	if a == nil {
		return 0
	}
	return max(a.journalCount, a.journalTailCount)
}

// applyAgentJournalSummary keeps the full timeline in entries while deriving
// the compact roster/detail summary from agent-authored progress only. System
// lifecycle records remain useful for metadata and timeline context, but must
// not turn "start" or "complete" into fake progress updates.
func applyAgentJournalSummary(a *agentState, entries []runstore.AgentJournalEntry) {
	if a == nil {
		return
	}
	count := 0
	var latest *runstore.AgentJournalEntry
	for i := range entries {
		entry := &entries[i]
		// The system-authored start record is authoritative fallback metadata
		// for legacy or sparse lifecycle events.
		if a.label == "" && entry.Label != "" {
			a.label = entry.Label
		}
		if a.profile == "" && entry.Profile != "" {
			a.profile = entry.Profile
		}
		if a.phase == "" && entry.Phase != "" {
			a.phase = entry.Phase
		}
		if !agentAuthoredProgressEntry(*entry) {
			continue
		}
		count++
		latest = entry
	}
	a.journalTailCount = count
	if latest != nil {
		a.journalKind = latest.Kind
		a.journalPreview = latest.Message
		a.journalTS = latest.TS
	}
}

func agentAuthoredProgressEntry(entry runstore.AgentJournalEntry) bool {
	if entry.TS <= 0 || strings.TrimSpace(entry.Kind) == "" || strings.TrimSpace(entry.Message) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.Kind)) {
	case "start", "nudge", "complete", "error":
		return false
	}
	source := strings.ToLower(strings.TrimSpace(entry.Source))
	return source == "agent" || source == "" // tolerate valid hand-written legacy entries
}

func nudgeUnavailable(a *agentState) bool {
	return a != nil && (strings.EqualFold(strings.TrimSpace(a.nudgeStatus), "unavailable") ||
		strings.Contains(strings.ToLower(a.nudgeMsg), "unavailable"))
}
