package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"dyna-agent/internal/runstore"
)

type runsModel struct {
	width, height int
	session       string
	runs          []runstore.Meta
	sel           int
	result        string
	vp            viewport.Model
	follow        bool // stick to bottom while running
	paused        map[string]bool
	catalogLoaded bool

	// The event files are append-only. Keep byte offsets and a derived view
	// model so a poll only decodes newly appended records instead of replaying
	// the run's entire history.
	loadedID       string
	eventOffset    int64
	eventsComplete bool
	resultLoaded   bool
	metaFinal      bool // terminal status observed in meta.json, after result write
	phases         []*phaseState
	phaseByName    map[string]*phaseState
	agents         map[int]*agentState
	logs           []string
	omittedLogs    int

	// Inspector: drill into one run's agents to read full prompts/responses.
	inspecting       bool
	journal          []runstore.JournalEntry // final completion ledger
	journalOffset    int64
	journalComplete  bool
	completionByID   map[int]*runstore.JournalEntry
	completionOrder  []int
	agentOrder       []*agentState // lifecycle agent_start order
	legacyAgentOrder []*agentState // completion-only records for old runs
	agentSel         int
	inspectFocus     bool
	inspectMode      inspectMode
	ivp              viewport.Model

	// Only the selected agent's work journal is tailed. Aggregate progress for
	// every other agent arrives through events.jsonl, avoiding journal fanout.
	agentJournal         []runstore.AgentJournalEntry
	agentJournalOffset   int64
	agentJournalComplete bool
	agentJournalMissing  bool
	agentJournalLoaded   bool
	journalFollow        bool
	journalUnseen        int
	inspectOffsets       [inspectModeCount]int

	// Polling happens in tea.Cmd so disk I/O never blocks keyboard handling.
	// generation rejects a response after selection or mode has changed.
	generation      uint64
	refreshing      bool
	refreshQueued   bool
	forceList       bool
	pollsUntilList  int
	pauseGeneration uint64

	confirm      string // pending confirmation: "delete" | "cancel" | "pause"
	confirmID    string
	statusMsg    string
	steering     bool
	steerInput   textinput.Model
	steerRunID   string
	steerAgentID int
}

func newRunsModel(session string) runsModel {
	steerInput := textinput.New()
	steerInput.Prompt = "› "
	steerInput.Placeholder = "Short instruction for this worker"
	steerInput.CharLimit = runstore.MaxSteeringMessageBytes
	return runsModel{
		session:        session,
		vp:             viewport.New(0, 0),
		ivp:            viewport.New(0, 0),
		follow:         true,
		paused:         make(map[string]bool),
		phaseByName:    make(map[string]*phaseState),
		agents:         make(map[int]*agentState),
		completionByID: make(map[int]*runstore.JournalEntry),
		journalFollow:  true,
		steerInput:     steerInput,
	}
}

func (m *runsModel) setSize(w, h int) {
	widthChanged := w != m.width
	detailY, inspectY := m.vp.YOffset, m.ivp.YOffset
	detailAtTop, inspectAtTop := m.vp.AtTop(), m.ivp.AtTop()
	detailAtBottom, inspectAtBottom := m.vp.AtBottom(), m.ivp.AtBottom()
	m.width, m.height = w, h
	m.vp.Width = m.detailWidth() - 4
	m.vp.Height = max(1, h-6) // two header lines + gap + pane border
	m.ivp.Width = m.detailWidth() - 4
	m.ivp.Height = max(1, h-6) // identity + Journal/Task/Result mode bar
	m.steerInput.Width = max(10, m.detailWidth()-10)
	if m.inspecting {
		if widthChanged {
			m.loadInspect(false)
		}
		if m.inspectMode == inspectJournal && m.journalFollow {
			m.ivp.GotoBottom()
			m.journalUnseen = 0
		} else if inspectAtTop {
			m.ivp.GotoTop()
		} else if inspectAtBottom {
			m.ivp.GotoBottom()
		} else {
			m.ivp.SetYOffset(inspectY)
		}
	} else if widthChanged {
		m.rebuildDetail(false)
	}
	if !m.inspecting {
		if m.follow && m.selectedStatus() == "running" {
			m.vp.GotoBottom()
		} else if detailAtTop {
			m.vp.GotoTop()
		} else if detailAtBottom {
			m.vp.GotoBottom()
		} else {
			m.vp.SetYOffset(detailY)
		}
	}
}

func (m *runsModel) listWidth() int   { return clamp(m.width/3, 30, 48) }
func (m *runsModel) detailWidth() int { return m.width - m.listWidth() - 6 }

const runListPolls = 4 // selected data: 400ms; full run catalog: about 2s

type runsRefreshRequest struct {
	generation         uint64
	session            string
	runID              string
	eventOffset        int64
	journalOffset      int64
	agentID            int
	agentJournalOffset int64
	readEvents         bool
	readJournal        bool
	readAgentJournal   bool
	readResult         bool
	includeList        bool
	pauseGeneration    uint64
}

type runsRefreshMsg struct {
	generation          uint64
	runID               string
	runDenied           bool
	runs                []runstore.Meta
	listLoaded          bool
	paused              map[string]bool
	events              []runstore.Event
	eventNext           int64
	eventRead           bool
	eventReset          bool
	journal             []runstore.JournalEntry
	journalNext         int64
	journalRead         bool
	journalReset        bool
	agentID             int
	agentJournal        []runstore.AgentJournalEntry
	agentJournalNext    int64
	agentJournalRead    bool
	agentJournalReset   bool
	agentJournalMissing bool
	result              string
	resultFound         bool
	resultRead          bool
	pauseGeneration     uint64
}

type steerResultMsg struct {
	runID   string
	agentID int
	err     error
}

func submitSteeringCmd(session, runID string, agentID int, message string) tea.Cmd {
	return func() tea.Msg {
		if err := authorizeRunSession(runID, session); err != nil {
			return steerResultMsg{runID: runID, agentID: agentID, err: err}
		}
		return steerResultMsg{runID: runID, agentID: agentID, err: runstore.SubmitAgentSteering(runID, agentID, message)}
	}
}

func authorizeRunSession(runID, session string) error {
	if session == "" {
		return nil
	}
	_, err := runstore.RequireSession(runID, session)
	return err
}

type runAction int

const (
	runActionDelete runAction = iota
	runActionCancel
	runActionPause
	runActionUnpause
)

func applyRunAction(session, runID string, action runAction) error {
	if err := authorizeRunSession(runID, session); err != nil {
		return err
	}
	switch action {
	case runActionDelete:
		return runstore.Remove(runID)
	case runActionCancel:
		return runstore.Cancel(runID)
	case runActionPause:
		return runstore.SetPaused(runID, true)
	case runActionUnpause:
		return runstore.SetPaused(runID, false)
	default:
		return fmt.Errorf("unsupported run action")
	}
}

func (m *runsModel) applySteerResult(msg steerResultMsg) {
	if msg.err != nil {
		m.statusMsg = "✗ " + msg.err.Error()
		return
	}
	m.statusMsg = fmt.Sprintf("✓ steering queued for agent %d", msg.agentID)
}

func (m runsModel) initialRefresh() tea.Cmd {
	req := runsRefreshRequest{
		generation:      m.generation,
		session:         m.session,
		includeList:     true,
		pauseGeneration: m.pauseGeneration,
	}
	return func() tea.Msg { return readRunsRefresh(req) }
}

func (m *runsModel) requestRefresh(forceList bool) tea.Cmd {
	if m.refreshing {
		m.refreshQueued = true
		m.forceList = m.forceList || forceList
		return nil
	}

	includeList := forceList || m.pollsUntilList <= 0
	if includeList {
		m.pollsUntilList = runListPolls
	} else {
		m.pollsUntilList--
	}
	req := runsRefreshRequest{
		generation:         m.generation,
		session:            m.session,
		runID:              m.selectedID(),
		eventOffset:        m.eventOffset,
		journalOffset:      m.journalOffset,
		agentID:            m.selectedAgentID(),
		agentJournalOffset: m.agentJournalOffset,
		includeList:        includeList,
		pauseGeneration:    m.pauseGeneration,
	}
	if req.runID != "" {
		req.readEvents = !m.eventsComplete
		req.readResult = m.selectedStatus() != "running" && !m.resultLoaded
		if m.inspecting {
			req.readJournal = !m.journalComplete
			req.readAgentJournal = req.agentID != 0 && !m.agentJournalComplete
		}
	}
	if !req.includeList && !req.readEvents && !req.readJournal && !req.readAgentJournal && !req.readResult {
		return nil
	}
	m.refreshing = true
	return func() tea.Msg { return readRunsRefresh(req) }
}

func readRunsRefresh(req runsRefreshRequest) runsRefreshMsg {
	msg := runsRefreshMsg{generation: req.generation, runID: req.runID, pauseGeneration: req.pauseGeneration}
	if req.includeList {
		var runs []runstore.Meta
		var err error
		if req.session == "" {
			runs, err = runstore.List()
		} else {
			runs, err = runstore.ListSession(req.session)
		}
		if err == nil {
			msg.runs = runs
			msg.listLoaded = true
			msg.paused = pausedRuns(runs)
		}
	}
	if req.runID == "" {
		return msg
	}
	if err := authorizeRunSession(req.runID, req.session); err != nil {
		msg.runDenied = true
		return msg
	}
	if req.readEvents {
		if events, next, err := runstore.ReadEventsFrom(req.runID, req.eventOffset); err == nil {
			msg.events = events
			msg.eventNext = next
			msg.eventRead = true
			msg.eventReset = next < req.eventOffset
		}
	}
	if req.readJournal {
		if entries, next, err := runstore.ReadJournalFrom(req.runID, req.journalOffset); err == nil {
			msg.journal = entries
			msg.journalNext = next
			msg.journalRead = true
			msg.journalReset = next < req.journalOffset
		}
	}
	if req.readAgentJournal {
		msg.agentID = req.agentID
		entries, next, err := runstore.ReadAgentJournalFrom(req.runID, req.agentID, req.agentJournalOffset)
		switch {
		case err == nil:
			msg.agentJournal = entries
			msg.agentJournalNext = next
			msg.agentJournalRead = true
			msg.agentJournalReset = next < req.agentJournalOffset
		case os.IsNotExist(err):
			// A queued/running agent may not have written its first record yet.
			// Treat absence as an empty read so terminal/cached agents can stop
			// polling while active agents continue to be watched.
			msg.agentJournalNext = req.agentJournalOffset
			msg.agentJournalRead = true
			msg.agentJournalMissing = true
		}
	}
	if req.readResult {
		msg.result, msg.resultFound = runstore.ReadResult(req.runID)
		msg.resultRead = true
	}
	return msg
}

func pausedRuns(runs []runstore.Meta) map[string]bool {
	paused := make(map[string]bool)
	for _, r := range runs {
		if r.Status == "running" && runstore.IsPaused(r.ID) {
			paused[r.ID] = true
		}
	}
	return paused
}

func (m *runsModel) refreshPaused(runs []runstore.Meta) {
	m.paused = pausedRuns(runs)
}

// applyRefresh applies only data for the selection/mode generation that made
// the request. It returns a coalesced follow-up command when input requested a
// refresh while this one was still in flight.
func (m *runsModel) applyRefresh(msg runsRefreshMsg, active bool) tea.Cmd {
	m.refreshing = false
	detailDirty := false
	stickBottom := false

	if msg.listLoaded {
		m.catalogLoaded = true
		m.pollsUntilList = runListPolls
		oldID := m.selectedID()
		oldMeta, hadOld := m.selectedMeta()
		oldIndex := m.sel
		m.runs = msg.runs
		if msg.pauseGeneration == m.pauseGeneration {
			m.paused = msg.paused
		}
		m.sel = indexRun(m.runs, oldID)
		if m.sel < 0 {
			m.sel = clamp(oldIndex, 0, max(0, len(m.runs)-1))
		}
		newID := m.selectedID()
		metaFinal := newID != "" && m.selectedStatus() != "running"
		// A catalog snapshot can race the final run_end append. Terminal state
		// is monotonic, so never replace the event-derived status with an older
		// "running" meta snapshot.
		if newID == oldID && hadOld && oldMeta.Status != "running" && m.selectedStatus() == "running" {
			m.runs[m.sel].Status = oldMeta.Status
			m.runs[m.sel].EndedAt = oldMeta.EndedAt
			m.runs[m.sel].Error = oldMeta.Error
		}
		if newID != oldID {
			m.resetLoaded(newID)
			if active && newID != "" {
				m.refreshQueued = true
			}
		} else if newMeta, ok := m.selectedMeta(); ok && (!hadOld || !sameViewMeta(oldMeta, newMeta)) {
			detailDirty = true
		}
		if m.confirm != "" && m.confirmID != newID {
			m.confirm = ""
			m.confirmID = ""
			m.statusMsg = "confirmation canceled: selection changed"
		}
		m.metaFinal = metaFinal
	}

	current := msg.generation == m.generation && msg.runID != "" && msg.runID == m.selectedID()
	if current && msg.runDenied {
		deniedIndex := indexRun(m.runs, msg.runID)
		if deniedIndex >= 0 {
			m.runs = append(m.runs[:deniedIndex], m.runs[deniedIndex+1:]...)
			m.sel = clamp(deniedIndex, 0, max(0, len(m.runs)-1))
		}
		m.resetLoaded(m.selectedID())
		m.confirm = ""
		m.confirmID = ""
		m.statusMsg = "run is outside this dashboard session"
		if active {
			m.refreshQueued = true
			m.forceList = true
		}
		current = false
	}
	selectedAgentID := m.selectedAgentID()
	rosterDirty := false
	inspectDirty := false
	if current && msg.eventRead {
		if msg.eventReset {
			m.clearEvents()
			m.result, m.resultLoaded = "", false
			detailDirty = true
			rosterDirty = m.inspecting
		}
		m.eventOffset = msg.eventNext
		if len(msg.events) > 0 {
			m.applyEvents(msg.events)
			detailDirty = true
			stickBottom = true
			rosterDirty = m.inspecting
		}
		if m.selectedStatus() != "running" {
			m.eventsComplete = true
		}
	}
	if current && msg.resultRead {
		m.result = msg.result
		m.resultLoaded = msg.resultFound || m.metaFinal
		if msg.resultFound {
			detailDirty = true
			stickBottom = true
		}
	}
	if current && msg.journalRead && m.inspecting {
		if msg.journalReset {
			m.resetCompletions()
			rosterDirty = true
		}
		m.journalOffset = msg.journalNext
		m.journal = append(m.journal, msg.journal...)
		if len(msg.journal) > 0 {
			m.applyCompletions(msg.journal)
			rosterDirty = true
			inspectDirty = true
		}
		if m.selectedStatus() != "running" {
			m.journalComplete = true
		}
	}

	selectionChanged := false
	if current && m.inspecting && rosterDirty {
		selectionChanged = m.reconcileAgentSelection(selectedAgentID)
		if selectionChanged {
			m.resetAgentJournal()
			m.generation++
			m.refreshQueued = true
		}
		inspectDirty = true
	}

	agentCurrent := current && !selectionChanged && m.inspecting && msg.agentJournalRead &&
		msg.agentID != 0 && msg.agentID == m.selectedAgentID()
	if agentCurrent {
		if msg.agentJournalReset {
			m.resetAgentJournal()
			m.agentJournalOffset = msg.agentJournalNext
		}
		m.agentJournalMissing = msg.agentJournalMissing
		m.agentJournalLoaded = true
		m.agentJournalOffset = msg.agentJournalNext
		if len(msg.agentJournal) > 0 {
			m.agentJournal = append(m.agentJournal, msg.agentJournal...)
			m.agentJournalMissing = false
			if m.inspectMode == inspectJournal && m.journalFollow {
				m.journalUnseen = 0
			} else {
				m.journalUnseen += len(msg.agentJournal)
			}
			if a := m.selectedAgent(); a != nil {
				applyAgentJournalSummary(a, m.agentJournal)
			}
		}
		if a := m.selectedAgent(); a != nil && agentDone(a.status) {
			m.agentJournalComplete = true
		}
		inspectDirty = true
	}
	if current && m.inspecting && inspectDirty {
		m.loadInspect(false)
	}

	if detailDirty && !m.inspecting {
		m.rebuildDetail(stickBottom)
	}

	if !active {
		m.refreshQueued = false
		m.forceList = false
		return nil
	}
	if m.refreshQueued {
		force := m.forceList
		m.refreshQueued = false
		m.forceList = false
		return m.requestRefresh(force)
	}
	return nil
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

func (m *runsModel) selectedMeta() (runstore.Meta, bool) {
	if m.sel < 0 || m.sel >= len(m.runs) {
		return runstore.Meta{}, false
	}
	return m.runs[m.sel], true
}

func (m *runsModel) selectedID() string {
	r, ok := m.selectedMeta()
	if !ok {
		return ""
	}
	return r.ID
}

func (m *runsModel) selectedStatus() string {
	r, ok := m.selectedMeta()
	if !ok {
		return ""
	}
	return r.Status
}

func indexRun(runs []runstore.Meta, id string) int {
	if id == "" {
		return -1
	}
	for i := range runs {
		if runs[i].ID == id {
			return i
		}
	}
	return -1
}

func sameViewMeta(a, b runstore.Meta) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Status == b.Status &&
		a.StartedAt.Equal(b.StartedAt) && a.EndedAt.Equal(b.EndedAt) && a.Error == b.Error
}

func (m *runsModel) resetLoaded(id string) {
	m.generation++
	m.loadedID = id
	m.eventOffset = 0
	m.eventsComplete = false
	m.result, m.resultLoaded = "", false
	m.metaFinal = id != "" && m.selectedStatus() != "running"
	m.journal = nil
	m.journalOffset = 0
	m.journalComplete = false
	m.completionByID = make(map[int]*runstore.JournalEntry)
	m.completionOrder = nil
	m.agentSel = 0
	m.inspectFocus = false
	m.inspectMode = inspectJournal
	m.resetAgentJournal()
	m.clearEvents()
	if id == "" {
		m.vp.SetContent(sDim.Render("select a run"))
	} else {
		m.vp.SetContent(sDim.Render("loading run…"))
	}
	m.vp.GotoTop()
	m.ivp.SetContent(sDim.Render("loading agent journal…"))
	m.ivp.GotoTop()
}

func (m *runsModel) clearEvents() {
	m.phases = nil
	m.phaseByName = make(map[string]*phaseState)
	m.agents = make(map[int]*agentState)
	m.agentOrder = nil
	m.legacyAgentOrder = nil
	m.logs = nil
	m.omittedLogs = 0
	// A legacy run may have a completion ledger but no lifecycle events. Keep
	// those records inspectable after an events-file reset.
	for _, id := range m.completionOrder {
		if completion := m.completionByID[id]; completion != nil {
			m.addLegacyAgent(completion)
		}
	}
}

func (m *runsModel) ensurePhase(name string) *phaseState {
	if ph := m.phaseByName[name]; ph != nil {
		return ph
	}
	ph := &phaseState{name: name}
	m.phaseByName[name] = ph
	m.phases = append(m.phases, ph)
	return ph
}

const maxVisibleLogs = 500

func (m *runsModel) applyEvents(events []runstore.Event) {
	var logBatch []string
	logCount := 0
	for _, e := range events {
		switch e.T {
		case "phase":
			m.ensurePhase(e.Title)
		case "agent_start":
			a := m.agents[e.ID]
			if a != nil && a.started {
				continue
			}
			if a == nil {
				a = &agentState{id: e.ID}
				m.agents[e.ID] = a
			} else {
				m.removeLegacyAgent(e.ID)
			}
			a.label = e.Label
			a.profile = e.Profile
			a.phase = e.Phase
			a.status = "queued"
			a.preview = e.Preview
			a.started = true
			m.agentOrder = append(m.agentOrder, a)
			ph := m.ensurePhase(a.phase)
			ph.agents = append(ph.agents, a)
		case "agent_run":
			if a := m.agents[e.ID]; a != nil {
				a.status = "running"
			}
		case "agent_end":
			if a := m.agents[e.ID]; a != nil {
				if !agentDone(a.status) && agentDone(e.Status) {
					m.ensurePhase(a.phase).done++
				}
				a.status = e.Status
				a.durMs = e.DurMs
				if e.Preview != "" {
					a.preview = e.Preview
				}
				a.errMsg = e.Error
				a.cached = e.Cached
			}
		case "agent_journal":
			if a := m.agents[e.ID]; a != nil {
				a.journalCount++
				a.journalKind = e.Kind
				a.journalPreview = e.Preview
				a.journalTS = e.TS
			}
		case "agent_nudge":
			if a := m.agents[e.ID]; a != nil {
				a.nudgeMsg = e.Msg
				a.nudgeStatus = e.Status
				a.nudgeTS = e.TS
			}
		case "agent_steer":
			if a := m.agents[e.ID]; a != nil {
				a.steerPreview = e.Preview
				a.steerTS = e.TS
			}
		case "log":
			logCount++
			if len(logBatch) < maxVisibleLogs {
				logBatch = append(logBatch, e.Msg)
			} else {
				logBatch[(logCount-1)%maxVisibleLogs] = e.Msg
			}
		case "run_end":
			if r, ok := m.selectedMeta(); ok && r.ID == m.loadedID {
				m.runs[m.sel].Status = e.Status
				m.runs[m.sel].Error = e.Error
				if e.TS > 0 {
					m.runs[m.sel].EndedAt = time.UnixMilli(e.TS)
				}
			}
		}
	}
	m.appendLogs(logBatch, logCount)
}

func (m *runsModel) appendLogs(batch []string, total int) {
	if total == 0 {
		return
	}
	if total >= maxVisibleLogs {
		start := total % maxVisibleLogs
		ordered := make([]string, 0, maxVisibleLogs)
		ordered = append(ordered, batch[start:]...)
		ordered = append(ordered, batch[:start]...)
		m.omittedLogs += len(m.logs) + total - maxVisibleLogs
		m.logs = ordered
		return
	}
	m.logs = append(m.logs, batch...)
	if excess := len(m.logs) - maxVisibleLogs; excess > 0 {
		m.omittedLogs += excess
		copy(m.logs, m.logs[excess:])
		m.logs = m.logs[:maxVisibleLogs]
	}
}

func agentDone(status string) bool { return status == "ok" || status == "error" }

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
	if m.steering {
		switch msg.String() {
		case "esc":
			m.steering = false
			m.steerInput.Blur()
			m.statusMsg = "steering canceled"
			return m, nil
		case "enter":
			message := strings.TrimSpace(m.steerInput.Value())
			if message == "" {
				m.statusMsg = "✗ steering message must not be empty"
				return m, nil
			}
			runID, agentID := m.steerRunID, m.steerAgentID
			m.steering = false
			m.steerInput.Blur()
			m.statusMsg = "sending steering…"
			return m, submitSteeringCmd(m.session, runID, agentID, message)
		default:
			var cmd tea.Cmd
			m.steerInput, cmd = m.steerInput.Update(msg)
			return m, cmd
		}
	}
	if m.inspecting {
		if m.inspectFocus {
			switch msg.String() {
			case "esc", "backspace":
				m.inspectFocus = false
			case "q":
				m.inspecting = false
				m.inspectFocus = false
				m.generation++
				return m, m.requestRefresh(false)
			case "up", "k":
				if m.inspectMode == inspectJournal {
					m.journalFollow = false
				}
				m.ivp.ScrollUp(1)
			case "down", "j":
				m.ivp.ScrollDown(1)
			case "left", "h":
				m.setInspectMode(m.inspectMode - 1)
			case "right", "l":
				m.setInspectMode(m.inspectMode + 1)
			case "f":
				m.journalFollow = !m.journalFollow
				if m.journalFollow {
					m.journalUnseen = 0
					if m.inspectMode == inspectJournal {
						m.ivp.GotoBottom()
					}
				}
			case "g":
				if m.inspectMode == inspectJournal {
					m.journalFollow = false
				}
				m.ivp.GotoTop()
			case "G":
				m.ivp.GotoBottom()
			case "pgup", "u":
				if m.inspectMode == inspectJournal {
					m.journalFollow = false
				}
				m.ivp.HalfViewUp()
			case "pgdown", "d", " ":
				m.ivp.HalfViewDown()
			}
			return m, nil
		}

		switch msg.String() {
		case "esc", "backspace", "q":
			m.inspecting = false
			m.generation++
			return m, m.requestRefresh(false)
		case "up", "k":
			if m.agentSel > 0 {
				m.agentSel--
				m.generation++
				m.resetAgentJournal()
				m.loadInspect(true)
				return m, m.requestRefresh(false)
			}
		case "down", "j":
			if m.agentSel < m.inspectorAgentCount()-1 {
				m.agentSel++
				m.generation++
				m.resetAgentJournal()
				m.loadInspect(true)
				return m, m.requestRefresh(false)
			}
		case "enter", "right":
			if m.inspectorAgentCount() > 0 {
				m.inspectFocus = true
				if m.inspectMode == inspectJournal && m.journalFollow {
					m.ivp.GotoBottom()
				}
			}
		case "s":
			if a := m.selectedAgent(); a != nil && a.status == "running" && m.selectedStatus() == "running" {
				m.steering = true
				m.steerRunID = m.selectedID()
				m.steerAgentID = a.id
				m.steerInput.Reset()
				m.statusMsg = ""
				return m, m.steerInput.Focus()
			}
		}
		return m, nil
	}

	if m.confirm != "" {
		forceRefresh := false
		if m.confirmID != "" && m.confirmID == m.selectedID() && (msg.String() == "y" || msg.String() == "Y") {
			id := m.confirmID
			var err error
			switch m.confirm {
			case "delete":
				err = applyRunAction(m.session, id, runActionDelete)
			case "cancel":
				err = applyRunAction(m.session, id, runActionCancel)
			case "pause":
				err = applyRunAction(m.session, id, runActionPause)
			}
			if err != nil {
				m.statusMsg = "✗ " + err.Error()
			} else {
				switch m.confirm {
				case "delete":
					m.statusMsg = "✓ deleted " + id
				case "cancel":
					m.statusMsg = "✓ cancel requested for " + id
				case "pause":
					m.paused[id] = true
					m.pauseGeneration++
					m.statusMsg = "⏸ paused " + id
				}
				forceRefresh = true
			}
		}
		m.confirm = ""
		m.confirmID = ""
		if forceRefresh {
			return m, m.requestRefresh(true)
		}
		return m, nil
	}

	switch msg.String() {
	case "up", "k":
		if m.sel > 0 {
			m.sel--
			m.follow = true
			m.resetLoaded(m.selectedID())
			return m, m.requestRefresh(false)
		}
	case "down", "j":
		if m.sel < len(m.runs)-1 {
			m.sel++
			m.follow = true
			m.resetLoaded(m.selectedID())
			return m, m.requestRefresh(false)
		}
	case "d":
		if m.sel < len(m.runs) {
			m.confirm = "delete"
			m.confirmID = m.selectedID()
			m.statusMsg = ""
		}
	case "x":
		if m.sel < len(m.runs) && m.runs[m.sel].Status == "running" {
			m.confirm = "cancel"
			m.confirmID = m.selectedID()
			m.statusMsg = ""
		}
	case "p":
		if m.sel < len(m.runs) && m.runs[m.sel].Status == "running" {
			id := m.runs[m.sel].ID
			if m.paused[id] {
				if err := applyRunAction(m.session, id, runActionUnpause); err != nil {
					m.statusMsg = "✗ " + err.Error()
				} else {
					delete(m.paused, id)
					m.pauseGeneration++
					m.statusMsg = "▶ resumed " + id
				}
			} else {
				m.confirm = "pause"
				m.confirmID = id
				m.statusMsg = ""
			}
		}
	case "enter", "right":
		if m.sel < len(m.runs) {
			m.inspecting = true
			m.inspectFocus = false
			m.inspectMode = inspectJournal
			m.agentSel = clamp(m.agentSel, 0, max(0, m.inspectorAgentCount()-1))
			m.resetAgentJournal()
			m.generation++
			m.loadInspect(true)
			return m, m.requestRefresh(false)
		}
	case "r":
		return m, m.requestRefresh(true)
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
		rightStyle := sPaneL
		if m.inspectFocus {
			rightStyle = sPaneR
		}
		content := m.renderInspectHeader()
		if m.steering {
			content += "\n\n" + sTitle.Render(fmt.Sprintf("Steer agent %d", m.steerAgentID)) + "\n" + m.steerInput.View()
		} else if m.statusMsg != "" {
			content += "\n" + sDim.Render(m.statusMsg)
		}
		content += "\n\n" + m.ivp.View()
		right := rightStyle.Width(m.detailWidth()).Height(max(0, m.height-2)).Render(content)
		return lipgloss.JoinHorizontal(lipgloss.Top, m.viewAgentList(), right)
	}
	left := m.viewList(frame)
	right := m.viewDetailPane(frame)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// viewAgentList is the inspector's left pane. Lifecycle starts define the
// stable roster; the final completion ledger is joined by ID and never gets to
// reorder active agents.
func (m runsModel) viewAgentList() string {
	w := m.listWidth()
	var b strings.Builder
	name := ""
	if m.sel < len(m.runs) {
		name = m.runs[m.sel].Name
	}
	b.WriteString(sTitle.Render("Agent journals") + sDim.Render(" · "+name) + "\n")
	if m.inspectorAgentCount() == 0 {
		b.WriteString(sDim.Render("\nwaiting for agents to start…"))
	}
	total := m.inspectorAgentCount()
	availableLines := max(1, m.height-3) // pane interior minus title
	maxRows := max(1, availableLines/2)
	overflow := total > maxRows
	if overflow {
		maxRows = max(1, (availableLines-1)/2) // reserve the range indicator
	}
	start, end := visibleRange(total, m.agentSel, maxRows)
	for i := start; i < end; i++ {
		a := m.inspectorAgentAt(i)
		icon := agentStatusIcon(a.status)
		if a.cached {
			icon = sWarnS.Render("⚡")
		}
		label := a.label
		if label == "" {
			label = fmt.Sprintf("agent #%d", a.id)
		}
		profile := ""
		if a.profile != "" {
			profile = "  " + sProfTag.Render(ansi.Truncate(a.profile, max(1, w/3), "…"))
		}
		label = ansi.Truncate(label, max(1, w-4-lipgloss.Width(profile)), "…")
		row := icon + " " + label
		if i == m.agentSel {
			row = icon + " " + sSel.Render(label)
		}
		row += profile
		b.WriteString(row + "\n")

		meta := a.status + " · " + agentJournalSummary(a)
		if a.steerTS > 0 {
			meta += " · steered"
		}
		if a.phase != "" {
			meta += " · " + a.phase
		}
		badgeText := ""
		if a.nudgeMsg != "" || a.nudgeStatus != "" {
			badge := sNudge
			if nudgeUnavailable(a) {
				badge = sNudgeUnavailable
			}
			badgeText = " " + badge.Render(nudgeLabel(a))
		}
		metaWidth := max(1, w-5-lipgloss.Width(badgeText))
		meta = ansi.Truncate(meta, metaWidth, "…")
		b.WriteString(sDim.Render("   "+meta) + badgeText)
		b.WriteString("\n")
	}
	if overflow {
		b.WriteString(sDim.Render(overflowLabel(start, end, total)) + "\n")
	}
	pane := sPaneL
	if !m.inspectFocus {
		pane = sPaneR
	}
	return pane.Width(w).Height(max(0, m.height-2)).Render(b.String())
}

func (m runsModel) viewList(frame int) string {
	w := m.listWidth()
	var b strings.Builder
	b.WriteString(sTitle.Render("Runs") + "\n")
	if len(m.runs) == 0 {
		if m.catalogLoaded {
			b.WriteString(sDim.Render("\nno runs yet\n\nstart one:\n  dyna run script.js\n  dyna demo"))
		} else {
			b.WriteString(sDim.Render("\nloading runs…"))
		}
	}
	footerLines := 0
	if m.confirm != "" && m.sel < len(m.runs) {
		footerLines = 3
	} else if m.statusMsg != "" {
		footerLines = 2
	}
	maxRows := max(1, m.height-3-footerLines) // pane interior minus title/footer
	overflow := len(m.runs) > maxRows
	if overflow {
		maxRows = max(1, maxRows-1) // reserve the range indicator
	}
	start, end := visibleRange(len(m.runs), m.sel, maxRows)
	for i := start; i < end; i++ {
		r := m.runs[i]
		status := r.Status
		if status == "running" && m.paused[r.ID] {
			status = "paused"
		}
		icon := statusIcon(status, frame)
		name := ansi.Truncate(r.Name, max(1, w-14), "…")
		meta := sDim.Render("  " + r.StartedAt.Format("Jan 02 15:04"))
		row := icon + " " + name + meta
		if i == m.sel {
			row = icon + " " + sSel.Render(name) + meta
		}
		b.WriteString(row + "\n")
	}
	if overflow {
		b.WriteString(sDim.Render(overflowLabel(start, end, len(m.runs))) + "\n")
	}
	if m.confirm != "" && m.sel < len(m.runs) {
		warn := map[string]string{
			"delete": "delete %s? its journal and result are gone forever",
			"cancel": "cancel %s? all in-flight workers will be KILLED",
			"pause":  "pause %s? running workers finish, no new ones start",
		}[m.confirm]
		b.WriteString("\n" + sErrS.Render("⚠ "+fmt.Sprintf(warn, m.runs[m.sel].Name)) + "\n" + sErrS.Render("  confirm? (y/n)"))
	} else if m.statusMsg != "" {
		b.WriteString("\n" + sDim.Render(m.statusMsg))
	}
	return sPaneL.Width(w).Height(max(0, m.height-2)).Render(b.String())
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

func (m runsModel) viewDetailPane(frame int) string {
	header := m.renderDetailHeader(frame)
	content := header
	if header != "" {
		content += "\n\n"
	}
	content += m.vp.View()
	return sPaneR.Width(m.detailWidth()).Height(max(0, m.height-2)).Render(content)
}

// agentState tracks one agent's lifecycle assembled from events.
type agentState struct {
	id               int
	label            string
	profile          string
	phase            string
	status           string // queued|running|ok|error
	durMs            int64
	preview          string
	errMsg           string
	cached           bool
	started          bool
	completion       *runstore.JournalEntry
	journalCount     int
	journalTailCount int
	journalKind      string
	journalPreview   string
	journalTS        int64
	nudgeMsg         string
	nudgeStatus      string
	nudgeTS          int64
	steerPreview     string
	steerTS          int64
}

type phaseState struct {
	name   string
	agents []*agentState
	done   int
}

func (m runsModel) renderDetailHeader(frame int) string {
	r, ok := m.selectedMeta()
	if !ok {
		return ""
	}
	w := max(1, m.detailWidth()-4)
	title := ansi.Truncate(sTitle.Render(r.Name)+"  "+sDim.Render(r.ID), w, "…")
	statusName := r.Status
	if statusName == "running" && m.paused[r.ID] {
		statusName = "paused"
	}
	status := statusIcon(statusName, frame) + " " + statusName
	dur := ""
	if !r.EndedAt.IsZero() {
		dur = "  " + sDim.Render(r.EndedAt.Sub(r.StartedAt).Round(time.Second).String())
	} else if r.Status == "running" {
		dur = "  " + sDim.Render(time.Since(r.StartedAt).Round(time.Second).String())
	}
	return title + "\n" + ansi.Truncate(status+dur, w, "…")
}

func (m *runsModel) rebuildDetail(stickBottom bool) {
	y := m.vp.YOffset
	m.vp.SetContent(m.renderDetailBody())
	if stickBottom && m.follow {
		m.vp.GotoBottom()
	} else {
		m.vp.SetYOffset(y)
	}
}

func (m *runsModel) renderDetailBody() string {
	r, ok := m.selectedMeta()
	if !ok {
		return sDim.Render("select a run")
	}
	w := m.detailWidth() - 4
	var b strings.Builder

	for _, ph := range m.phases {
		name := ph.name
		if name == "" {
			name = "(no phase)"
		}
		b.WriteString(sPhase.Render("▮ "+name) + sDim.Render(fmt.Sprintf("  %d/%d", ph.done, len(ph.agents))) + "\n")
		for _, a := range ph.agents {
			icon := agentStatusIcon(a.status)
			line := fmt.Sprintf("  %s %s", icon, a.label)
			if a.profile != "" {
				line += " " + sProfTag.Render("["+a.profile+"]")
			}
			line += " " + sDim.Render(a.status)
			if a.cached {
				line += sWarnS.Render("  ⚡cached")
			} else if a.durMs > 0 {
				line += sDim.Render("  " + fmtDur(a.durMs))
			}
			b.WriteString(line + "\n")
			if a.status == "error" && a.errMsg != "" {
				b.WriteString(sErrS.Render("      "+truncLine(a.errMsg, w-8)) + "\n")
			} else if journalEntryCount(a) > 0 {
				kind := strings.TrimSpace(a.journalKind)
				if kind == "" {
					kind = "note"
				}
				progress := strings.ToUpper(kind)
				if a.journalPreview != "" {
					progress += "  " + truncLine(a.journalPreview, max(8, w-26))
				}
				progress += "  ·  " + agentJournalSummary(a)
				b.WriteString("      " + sJournalKind.Render(progress) + "\n")
			} else if a.preview != "" && a.status == "ok" {
				b.WriteString(sDim.Render("      ↳ "+truncLine(a.preview, w-10)) + "\n")
			}
			if a.nudgeMsg != "" || a.nudgeStatus != "" {
				badge := sNudge
				if nudgeUnavailable(a) {
					badge = sNudgeUnavailable
				}
				b.WriteString("      " + badge.Render(nudgeLabel(a)) + "\n")
			}
			if a.steerTS > 0 {
				b.WriteString("      " + sJournalKind.Render("STEER  "+truncLine(a.steerPreview, max(8, w-20))) + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(m.logs) > 0 {
		b.WriteString(sTitle.Render("Log") + "\n")
		if m.omittedLogs > 0 {
			b.WriteString(sDim.Render(fmt.Sprintf("… %d earlier log lines omitted from the dashboard\n", m.omittedLogs)))
		}
		for _, l := range m.logs {
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
		b.WriteString(wrap(res, w) + "\n")
	}
	return b.String()
}

func agentStatusIcon(status string) string {
	switch status {
	case "running":
		return sWarnS.Render("●")
	case "ok":
		return sOK.Render("✓")
	case "error":
		return sErrS.Render("✗")
	default:
		return sDim.Render("◌")
	}
}

func truncLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n < 8 {
		n = 8
	}
	return ansi.Truncate(s, n, "…")
}

func wrap(s string, w int) string {
	if w < 20 {
		w = 20
	}
	return ansi.Hardwrap(s, w, true)
}
