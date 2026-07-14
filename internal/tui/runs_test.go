package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dyna-agent/internal/runstore"
)

func TestApplyEventsIncrementallyMatchesOneShot(t *testing.T) {
	events := []runstore.Event{
		{T: "phase", Title: "plan"},
		{T: "agent_start", ID: 1, Label: "planner", Profile: "smart", Phase: "plan"},
		{T: "agent_run", ID: 1},
		{T: "log", Msg: "working"},
		{T: "agent_end", ID: 1, Status: "ok", DurMs: 1250, Preview: "done", Cached: true},
		{T: "phase", Title: "review"},
		{T: "agent_start", ID: 2, Label: "reviewer", Profile: "careful", Phase: "review"},
		{T: "agent_end", ID: 2, Status: "error", DurMs: 500, Error: "failed"},
	}

	oneShot := testRunsModel()
	oneShot.applyEvents(events)
	incremental := testRunsModel()
	for _, batch := range [][]runstore.Event{events[:2], events[2:5], events[5:]} {
		incremental.applyEvents(batch)
	}

	if got, want := incremental.renderDetailBody(), oneShot.renderDetailBody(); got != want {
		t.Fatalf("incremental body differs from one-shot body\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if got := incremental.phaseByName["plan"].done; got != 1 {
		t.Fatalf("plan done count = %d, want 1", got)
	}
	// A duplicate terminal record must not inflate the phase counter.
	incremental.applyEvents([]runstore.Event{{T: "agent_end", ID: 1, Status: "ok"}})
	if got := incremental.phaseByName["plan"].done; got != 1 {
		t.Fatalf("plan done count after duplicate = %d, want 1", got)
	}
}

func TestApplyRefreshRejectsStaleSelection(t *testing.T) {
	m := testRunsModel()
	m.runs = append(m.runs, runstore.Meta{ID: "wf_other", Name: "other", Status: "running", StartedAt: time.Now()})
	staleGeneration := m.generation

	m.sel = 1
	m.resetLoaded("wf_other")
	m.applyRefresh(runsRefreshMsg{
		generation: staleGeneration,
		runID:      "wf_test",
		events:     []runstore.Event{{T: "agent_start", ID: 99, Label: "stale", Phase: "old"}},
		eventNext:  123,
		eventRead:  true,
	}, false)

	if len(m.agents) != 0 || m.eventOffset != 0 {
		t.Fatalf("stale refresh mutated new selection: agents=%d offset=%d", len(m.agents), m.eventOffset)
	}
}

func TestCatalogRefreshPreservesSelectionByID(t *testing.T) {
	m := testRunsModel()
	selectedGeneration := m.generation
	m.applyRefresh(runsRefreshMsg{
		generation: selectedGeneration,
		runID:      "wf_test",
		listLoaded: true,
		runs: []runstore.Meta{
			{ID: "wf_new", Name: "new", Status: "running", StartedAt: time.Now()},
			m.runs[0],
		},
		paused: map[string]bool{},
	}, false)

	if got := m.selectedID(); got != "wf_test" {
		t.Fatalf("selected run = %q, want wf_test", got)
	}
	if m.sel != 1 {
		t.Fatalf("selected index = %d, want 1 after insertion", m.sel)
	}
	if m.generation != selectedGeneration {
		t.Fatalf("stable selection changed generation: got %d want %d", m.generation, selectedGeneration)
	}
}

func TestInitialCatalogLoadQueuesSelectedRunTail(t *testing.T) {
	m := newRunsModel()
	m.setSize(120, 30)
	m.refreshing = true // mirrors Run while initialRefresh is in flight
	cmd := m.applyRefresh(runsRefreshMsg{
		listLoaded: true,
		runs: []runstore.Meta{{
			ID:        "wf_first",
			Name:      "first",
			Status:    "running",
			StartedAt: time.Now(),
		}},
		paused: map[string]bool{},
	}, true)
	if m.selectedID() != "wf_first" || cmd == nil || !m.refreshing {
		t.Fatalf("initial load did not queue tail: selected=%q cmd=%v refreshing=%v", m.selectedID(), cmd != nil, m.refreshing)
	}
}

func TestCatalogRefreshCancelsConfirmationIfTargetDisappears(t *testing.T) {
	m := testRunsModel()
	m.confirm = "delete"
	m.confirmID = "wf_test"
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      "wf_test",
		listLoaded: true,
		runs: []runstore.Meta{{
			ID:        "wf_other",
			Name:      "other",
			Status:    "ok",
			StartedAt: time.Now(),
		}},
		paused: map[string]bool{},
	}, false)
	if m.confirm != "" || m.confirmID != "" {
		t.Fatalf("stale confirmation survived selection replacement: %q %q", m.confirm, m.confirmID)
	}
	if !strings.Contains(m.statusMsg, "canceled") {
		t.Fatalf("missing cancellation status: %q", m.statusMsg)
	}
}

func TestStaleCatalogCannotUndoLocalPauseState(t *testing.T) {
	m := testRunsModel()
	m.pauseGeneration = 1 // a local resume happened after this snapshot started
	delete(m.paused, "wf_test")
	m.applyRefresh(runsRefreshMsg{
		generation:      m.generation,
		runID:           m.selectedID(),
		listLoaded:      true,
		runs:            append([]runstore.Meta(nil), m.runs...),
		paused:          map[string]bool{"wf_test": true},
		pauseGeneration: 0,
	}, false)
	if m.paused["wf_test"] {
		t.Fatal("stale catalog restored paused state after local resume")
	}
}

func TestRefreshPreservesManualScrollAndFollowsWhenEnabled(t *testing.T) {
	m := testRunsModel()
	for i := 0; i < 80; i++ {
		m.applyEvents([]runstore.Event{{T: "log", Msg: fmt.Sprintf("line %03d", i)}})
	}
	m.rebuildDetail(true)
	if !m.vp.AtBottom() {
		t.Fatal("follow mode did not start at bottom")
	}

	m.follow = false
	m.vp.GotoTop()
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		events:     []runstore.Event{{T: "log", Msg: "manual-scroll append"}},
		eventNext:  10,
		eventRead:  true,
	}, false)
	if !m.vp.AtTop() {
		t.Fatalf("manual scroll moved after append: offset=%d", m.vp.YOffset)
	}

	m.follow = true
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		events:     []runstore.Event{{T: "log", Msg: "follow append"}},
		eventNext:  20,
		eventRead:  true,
	}, false)
	if !m.vp.AtBottom() {
		t.Fatalf("follow mode did not move to bottom: offset=%d", m.vp.YOffset)
	}
}

func TestMissingResultRetriesUntilTerminalMetaIsVisible(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "run_end", TS: time.Now().UnixMilli(), Status: "ok"}})
	if m.metaFinal {
		t.Fatal("run_end event incorrectly marked meta final")
	}

	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		resultRead: true,
	}, false)
	if m.resultLoaded {
		t.Fatal("missing result was marked loaded before final meta became visible")
	}

	m.applyRefresh(runsRefreshMsg{
		generation:  m.generation,
		runID:       m.selectedID(),
		result:      `{"ok":true}`,
		resultFound: true,
		resultRead:  true,
	}, false)
	if !m.resultLoaded || m.result == "" {
		t.Fatal("result was not accepted when it appeared")
	}
}

func TestRunDetailKeepsFullScrollableWorkflowResult(t *testing.T) {
	m := testRunsModel()
	var result strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&result, "panel result line %03d: %s\n", i, strings.Repeat("x", 24))
	}
	m.result = result.String()
	m.resultLoaded = true
	m.rebuildDetail(false)
	if !strings.Contains(m.vp.View(), "panel result line 000") || m.vp.AtBottom() {
		t.Fatalf("long workflow result is not open and scrollable: offset=%d body=%q", m.vp.YOffset, m.vp.View())
	}
	m.vp.GotoBottom()
	if !strings.Contains(m.vp.View(), "panel result line 299") {
		t.Fatal("workflow result was truncated before its final line")
	}
}

func TestStaleRunningCatalogDoesNotRegressRunEnd(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "run_end", TS: time.Now().UnixMilli(), Status: "ok"}})
	ended := m.runs[0].EndedAt
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		listLoaded: true,
		runs: []runstore.Meta{{
			ID:        "wf_test",
			Name:      "test",
			Status:    "running",
			StartedAt: m.runs[0].StartedAt,
		}},
		paused: map[string]bool{},
	}, false)
	if got := m.selectedStatus(); got != "ok" {
		t.Fatalf("status regressed to %q, want ok", got)
	}
	if !m.runs[0].EndedAt.Equal(ended) {
		t.Fatal("stale catalog discarded event-derived end time")
	}
	if m.metaFinal {
		t.Fatal("stale running catalog incorrectly marked meta final")
	}
}

func TestEmptyFileResetClearsCachedDetailAndInspector(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "phase", Title: "old"},
		{T: "agent_start", ID: 1, Label: "old-agent", Phase: "old"},
	})
	m.result = `{"old":true}`
	m.resultLoaded = true
	m.rebuildDetail(false)
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		eventRead:  true,
		eventReset: true,
	}, false)
	if len(m.agents) != 0 || m.result != "" || strings.Contains(m.vp.View(), "old-agent") {
		t.Fatal("empty event reset left stale detail content")
	}

	m.inspecting = true
	m.applyCompletions([]runstore.JournalEntry{{ID: 1, Label: "old-agent", Profile: "test", Prompt: "old prompt"}})
	m.loadInspect(true)
	m.applyRefresh(runsRefreshMsg{
		generation:   m.generation,
		runID:        m.selectedID(),
		journalRead:  true,
		journalReset: true,
	}, false)
	if len(m.journal) != 0 || !strings.Contains(m.ivp.View(), "no agents have started yet") {
		t.Fatal("empty journal reset left stale inspector content")
	}
}

func TestResizeKeepsFollowAndInspectorAnchored(t *testing.T) {
	m := testRunsModel()
	for i := 0; i < 80; i++ {
		m.applyEvents([]runstore.Event{{T: "log", Msg: fmt.Sprintf("line %03d", i)}})
	}
	m.rebuildDetail(true)
	if !m.vp.AtBottom() {
		t.Fatal("detail was not at bottom before resize")
	}
	m.setSize(100, 20)
	if !m.vp.AtBottom() {
		t.Fatalf("follow detail lost bottom anchor on resize: offset=%d", m.vp.YOffset)
	}

	m.inspecting = true
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "reader", Profile: "test"}})
	for i := 0; i < 100; i++ {
		m.agentJournal = append(m.agentJournal, runstore.AgentJournalEntry{TS: time.Now().UnixMilli(), Kind: "progress", Message: fmt.Sprintf("journal line %03d", i)})
	}
	m.loadInspect(true)
	m.ivp.GotoBottom()
	m.setSize(90, 16)
	if !m.ivp.AtBottom() {
		t.Fatalf("inspector lost bottom anchor on resize: offset=%d", m.ivp.YOffset)
	}
}

func TestRunListWindowsSelectionAndResize(t *testing.T) {
	m := testRunsModel()
	m.runs = make([]runstore.Meta, 10)
	for i := range m.runs {
		m.runs[i] = runstore.Meta{ID: fmt.Sprintf("wf_%02d", i), Name: fmt.Sprintf("run-%02d", i), Status: "ok", StartedAt: time.Now()}
	}
	m.sel = 6
	m.setSize(120, 8)

	view := m.viewList(0)
	if !strings.Contains(view, "run-06") || !strings.Contains(view, "4-7 of 10") || strings.Contains(view, "run-02") {
		t.Fatalf("selection below fold was not windowed:\n%s", view)
	}

	m.setSize(120, 6)
	view = m.viewList(0)
	if !strings.Contains(view, "run-06") || !strings.Contains(view, "6-7 of 10") {
		t.Fatalf("resize did not keep selected run visible:\n%s", view)
	}

	m.setSize(120, 0)
	if view = m.viewList(0); !strings.Contains(view, "run-06") || !strings.Contains(view, "7-7 of 10") {
		t.Fatalf("zero-height run list did not clamp safely:\n%s", view)
	}
}

func TestInspectorAgentListWindowsSelectionAndResize(t *testing.T) {
	m := testRunsModel()
	events := make([]runstore.Event, 8)
	for i := range events {
		events[i] = runstore.Event{T: "agent_start", ID: i + 1, Label: fmt.Sprintf("agent-%02d", i+1), Profile: "test"}
	}
	m.applyEvents(events)
	m.inspecting = true
	m.agentSel = 5
	m.setSize(120, 10)

	view := m.viewAgentList()
	if !strings.Contains(view, "agent-06") || !strings.Contains(view, "4-6 of 8") || strings.Contains(view, "agent-03") {
		t.Fatalf("selected agent below fold was not windowed:\n%s", view)
	}

	m.setSize(120, 8)
	view = m.viewAgentList()
	if !strings.Contains(view, "agent-06") || !strings.Contains(view, "5-6 of 8") {
		t.Fatalf("resize did not keep selected agent visible:\n%s", view)
	}

	m.setSize(120, 0)
	if view = m.viewAgentList(); !strings.Contains(view, "agent-06") || !strings.Contains(view, "6-6 of 8") {
		t.Fatalf("zero-height agent list did not clamp safely:\n%s", view)
	}
}

func TestInspectorEnterFocusesPromptAndJKScroll(t *testing.T) {
	m := testRunsModel()
	m.inspecting = true
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "reader", Profile: "test"}})
	for i := 0; i < 100; i++ {
		m.agentJournal = append(m.agentJournal, runstore.AgentJournalEntry{TS: time.Now().UnixMilli(), Kind: "progress", Message: fmt.Sprintf("journal line %03d", i)})
	}
	m.journalFollow = false
	m.loadInspect(true)

	m, _ = m.update(key(tea.KeyEnter))
	if !m.inspectFocus {
		t.Fatal("enter did not focus the prompt pane")
	}
	before := m.ivp.YOffset
	m, _ = m.update(runeKey('j'))
	if m.ivp.YOffset != before+1 {
		t.Fatalf("j scrolled to %d, want %d", m.ivp.YOffset, before+1)
	}
	m, _ = m.update(key(tea.KeyUp))
	if m.ivp.YOffset != before {
		t.Fatalf("up scrolled to %d, want %d", m.ivp.YOffset, before)
	}
	m, _ = m.update(key(tea.KeyEsc))
	if m.inspectFocus || !m.inspecting {
		t.Fatalf("esc focus state = focused:%v inspecting:%v, want agent list", m.inspectFocus, m.inspecting)
	}
}

func TestInspectorRosterShowsActiveAgentsBeforeCompletion(t *testing.T) {
	m := testRunsModel()
	m.inspecting = true
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		eventRead:  true,
		eventNext:  64,
		events: []runstore.Event{
			{T: "agent_start", ID: 7, Label: "explorer", Profile: "fast", Phase: "research"},
			{T: "agent_run", ID: 7},
			{T: "agent_start", ID: 9, Label: "reviewer", Profile: "careful", Phase: "review"},
		},
	}, false)

	if got := m.inspectorAgentCount(); got != 2 {
		t.Fatalf("active roster size = %d, want 2", got)
	}
	if got := m.inspectorAgentAt(0); got.id != 7 || got.status != "running" {
		t.Fatalf("first active agent = %#v, want running agent 7", got)
	}
	if got := m.inspectorAgentAt(1); got.id != 9 || got.status != "queued" {
		t.Fatalf("second active agent = %#v, want queued agent 9", got)
	}
	view := m.viewAgentList()
	if !strings.Contains(view, "explorer") || !strings.Contains(view, "reviewer") ||
		!strings.Contains(view, "running") || !strings.Contains(view, "queued") || !strings.Contains(view, "waiting for journal") {
		t.Fatalf("active agents missing from inspector before completion:\n%s", view)
	}
}

func TestInspectorMergesLifecycleCompletionAndJournalStreamsByID(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "agent_start", ID: 1, Label: "planner", Profile: "smart", Phase: "plan"},
		{T: "agent_start", ID: 2, Label: "reviewer", Profile: "careful", Phase: "review"},
	})
	m.inspecting = true
	now := time.Now().UnixMilli()
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		eventRead:  true,
		eventNext:  100,
		events: []runstore.Event{
			{T: "agent_run", ID: 1},
			{T: "agent_journal", TS: now, ID: 1, Kind: "progress", Preview: "mapped the dependency graph"},
			{T: "agent_nudge", TS: now, ID: 1, Msg: "journal nudge delivered"},
			{T: "agent_end", ID: 2, Status: "ok"},
		},
		journalRead: true,
		journalNext: 200,
		// Deliberately reversed: completion order must not reorder the roster.
		journal: []runstore.JournalEntry{
			{ID: 2, Label: "reviewer", Profile: "careful", Prompt: "review", Result: "reviewed"},
			{ID: 1, Label: "planner", Profile: "smart", Prompt: "plan", Result: "planned"},
		},
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 300,
		agentJournal: []runstore.AgentJournalEntry{{
			TS: now, Kind: "progress", Message: "mapped the dependency graph", Next: "write the plan", Source: "agent", AgentID: 1,
		}},
	}, false)

	if got := m.inspectorAgentAt(0); got.id != 1 || got.completion == nil || got.completion.Prompt != "plan" {
		t.Fatalf("first roster entry did not join completion 1: %#v", got)
	}
	if got := m.inspectorAgentAt(1); got.id != 2 || got.completion == nil || got.completion.Prompt != "review" {
		t.Fatalf("second roster entry did not join completion 2: %#v", got)
	}
	if got := m.agents[1]; got.journalCount != 1 || got.journalPreview != "mapped the dependency graph" || got.nudgeMsg == "" {
		t.Fatalf("aggregate journal/nudge state not merged: %#v", got)
	}
	if len(m.agentJournal) != 1 || m.eventOffset != 100 || m.journalOffset != 200 || m.agentJournalOffset != 300 {
		t.Fatalf("independent tails not committed: events=%d completions=%d agent=%d entries=%d", m.eventOffset, m.journalOffset, m.agentJournalOffset, len(m.agentJournal))
	}
	if view := m.viewAgentList(); !strings.Contains(view, "1 entry") || !strings.Contains(view, "NUDGED") {
		t.Fatalf("journal progress missing from roster:\n%s", view)
	}
	// A selected journal can be observed just before its aggregate event. The
	// two streams must converge on one count instead of double-counting it.
	m.applyRefresh(runsRefreshMsg{
		generation:       m.generation,
		runID:            m.selectedID(),
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 350,
		agentJournal: []runstore.AgentJournalEntry{{
			TS: now, Kind: "progress", Message: "drafted the plan", Source: "agent", AgentID: 1,
		}},
	}, false)
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		eventRead:  true,
		eventNext:  150,
		events:     []runstore.Event{{T: "agent_journal", TS: now, ID: 1, Kind: "progress", Preview: "drafted the plan"}},
	}, false)
	if got := agentJournalSummary(m.agents[1]); !strings.HasPrefix(got, "2 entries") {
		t.Fatalf("journal streams double-counted progress: %q", got)
	}
	m.inspecting = false
	if body := m.renderDetailBody(); !strings.Contains(body, "drafted the plan") || !strings.Contains(body, "2 entries") {
		t.Fatalf("aggregate journal preview missing from run detail:\n%s", body)
	}
}

func TestSelectedJournalSummaryCountsOnlyAgentProgress(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1}})
	m.inspecting = true
	now := time.Now()
	startTS := now.Add(-time.Minute).UnixMilli()
	findingTS := now.Add(-12 * time.Second).UnixMilli()
	completeTS := now.UnixMilli()
	m.applyRefresh(runsRefreshMsg{
		generation:       m.generation,
		runID:            m.selectedID(),
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 500,
		agentJournal: []runstore.AgentJournalEntry{
			{TS: startTS, Kind: "start", Message: "Agent started", Source: "system", AgentID: 1, Label: "journal-reader", Profile: "careful", Phase: "research", Prompt: "find the cause"},
			{TS: findingTS, Kind: "finding", Message: "the tail offset was stale", Next: "fix the guard", Source: "agent", AgentID: 1},
			{TS: completeTS, Kind: "complete", Message: "Agent completed the task.", Source: "system", AgentID: 1},
		},
	}, false)

	a := m.agents[1]
	if a.journalTailCount != 1 || journalEntryCount(a) != 1 {
		t.Fatalf("system records inflated progress count: tail=%d summary=%d", a.journalTailCount, journalEntryCount(a))
	}
	if a.journalKind != "finding" || a.journalPreview != "the tail offset was stale" || a.journalTS != findingTS {
		t.Fatalf("latest progress came from a system record: kind=%q preview=%q ts=%d want finding ts=%d", a.journalKind, a.journalPreview, a.journalTS, findingTS)
	}
	if a.label != "journal-reader" || a.profile != "careful" || a.phase != "research" {
		t.Fatalf("start metadata was not retained: label=%q profile=%q phase=%q", a.label, a.profile, a.phase)
	}
	row := m.viewAgentList()
	if !strings.Contains(row, "1 entry") || !strings.Contains(row, journalFreshness(findingTS)) {
		t.Fatalf("row count/freshness did not come from finding:\n%s", row)
	}
	timeline := m.ivp.View()
	if !strings.Contains(timeline, "Agent started") || !strings.Contains(timeline, "the tail offset was stale") || !strings.Contains(timeline, "Agent completed the task") {
		t.Fatalf("system records disappeared from timeline:\n%s", timeline)
	}
}

func TestNudgeUnavailableBadgeUsesEventStatus(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "agent_start", ID: 1, Label: "worker"},
		{T: "agent_nudge", ID: 1, Status: "unavailable", Msg: "live reminder could not be delivered"},
	})
	m.inspecting = true
	if view := m.viewAgentList(); !strings.Contains(view, "NUDGE UNAVAILABLE") {
		t.Fatalf("unavailable event status did not select unavailable badge:\n%s", view)
	}
}

func TestIgnoredJournalReminderHasDistinctBadge(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "agent_start", ID: 1, Label: "worker"},
		{T: "agent_nudge", ID: 1, Status: "sent", Msg: "reminder sent"},
		{T: "agent_nudge", ID: 1, Status: "ignored", Msg: "worker still wrote no entry"},
	})
	m.inspecting = true
	view := m.viewAgentList()
	if !strings.Contains(view, "NO JOURNAL ENTRY") || strings.Contains(view, "NUDGE UNAVAILABLE") {
		t.Fatalf("ignored reminder was mislabeled:\n%s", view)
	}
}

func TestInspectorRejectsStaleSelectedAgentJournal(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "agent_start", ID: 1, Label: "one"},
		{T: "agent_start", ID: 2, Label: "two"},
	})
	m.inspecting = true
	staleGeneration := m.generation
	m.agentSel = 1
	m.generation++
	m.resetAgentJournal()
	m.applyRefresh(runsRefreshMsg{
		generation:       staleGeneration,
		runID:            m.selectedID(),
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 99,
		agentJournal: []runstore.AgentJournalEntry{{
			TS: time.Now().UnixMilli(), Kind: "progress", Message: "stale agent one update",
		}},
	}, false)

	if m.selectedAgentID() != 2 || len(m.agentJournal) != 0 || m.agentJournalOffset != 0 {
		t.Fatalf("stale selected-agent result applied: selected=%d entries=%d offset=%d", m.selectedAgentID(), len(m.agentJournal), m.agentJournalOffset)
	}
}

func TestInspectorModesFollowAndUnseenKeys(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "reader", Profile: "test"}})
	m.applyCompletions([]runstore.JournalEntry{{ID: 1, Label: "reader", Profile: "test", Prompt: "inspect this task", Result: "final answer"}})
	m.inspecting = true
	m.agentJournal = make([]runstore.AgentJournalEntry, 80)
	for i := range m.agentJournal {
		m.agentJournal[i] = runstore.AgentJournalEntry{TS: time.Now().UnixMilli(), Kind: "progress", Message: fmt.Sprintf("journal line %03d", i)}
	}
	m.loadInspect(true)

	m, _ = m.update(key(tea.KeyRight))
	if !m.inspectFocus {
		t.Fatal("right did not focus journal detail")
	}
	m, _ = m.update(key(tea.KeyRight))
	if m.inspectMode != inspectTask || !strings.Contains(m.ivp.View(), "inspect this task") {
		t.Fatalf("right did not switch to Task mode: mode=%v body=%q", m.inspectMode, m.ivp.View())
	}
	m, _ = m.update(key(tea.KeyRight))
	if m.inspectMode != inspectResult || !strings.Contains(m.ivp.View(), "final answer") {
		t.Fatalf("right did not switch to Result mode: mode=%v body=%q", m.inspectMode, m.ivp.View())
	}
	m, _ = m.update(key(tea.KeyLeft))
	m, _ = m.update(key(tea.KeyLeft))
	if m.inspectMode != inspectJournal {
		t.Fatalf("left did not return to Journal mode: %v", m.inspectMode)
	}
	m, _ = m.update(runeKey('g'))
	if m.journalFollow || !m.ivp.AtTop() {
		t.Fatalf("manual top did not disable follow: follow=%v offset=%d", m.journalFollow, m.ivp.YOffset)
	}
	m.applyRefresh(runsRefreshMsg{
		generation:       m.generation,
		runID:            m.selectedID(),
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 400,
		agentJournal: []runstore.AgentJournalEntry{{
			TS: time.Now().UnixMilli(), Kind: "progress", Message: "new unseen update",
		}},
	}, false)
	if !m.ivp.AtTop() || m.journalUnseen != 1 || !strings.Contains(m.renderInspectHeader(), "1 UNSEEN") {
		t.Fatalf("follow-off append moved or hid unseen state: offset=%d unseen=%d header=%q", m.ivp.YOffset, m.journalUnseen, m.renderInspectHeader())
	}
	m, _ = m.update(runeKey('f'))
	if !m.journalFollow || m.journalUnseen != 0 || !m.ivp.AtBottom() {
		t.Fatalf("f did not restore follow at bottom: follow=%v unseen=%d offset=%d", m.journalFollow, m.journalUnseen, m.ivp.YOffset)
	}
	m, _ = m.update(key(tea.KeyUp))
	if m.journalFollow {
		t.Fatal("manual upward scroll did not disable follow")
	}
}

func TestJournalUpdatesStayUnseenWhileReadingTask(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "reader"}})
	m.applyCompletions([]runstore.JournalEntry{{ID: 1, Prompt: "read this task", Result: "done"}})
	m.inspecting = true
	m.inspectFocus = true
	m.setInspectMode(inspectTask)
	m.journalFollow = true
	m.applyRefresh(runsRefreshMsg{
		generation:       m.generation,
		runID:            m.selectedID(),
		agentID:          1,
		agentJournalRead: true,
		agentJournalNext: 50,
		agentJournal: []runstore.AgentJournalEntry{{
			TS: time.Now().UnixMilli(), Kind: "finding", Message: "arrived while task was open", Source: "agent",
		}},
	}, false)
	if m.inspectMode != inspectTask || m.journalUnseen != 1 || !strings.Contains(m.renderInspectHeader(), "1 UNSEEN") {
		t.Fatalf("off-tab journal update was silently consumed: mode=%v unseen=%d header=%q", m.inspectMode, m.journalUnseen, m.renderInspectHeader())
	}
	m.setInspectMode(inspectJournal)
	if m.journalUnseen != 0 || !m.ivp.AtBottom() {
		t.Fatalf("opening followed Journal did not acknowledge updates: unseen=%d offset=%d", m.journalUnseen, m.ivp.YOffset)
	}
}

func TestJournalFollowSurvivesResizeFromFittingToScrollable(t *testing.T) {
	m := testRunsModel()
	m.setSize(180, 20)
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "reader"}})
	m.inspecting = true
	m.inspectFocus = true
	m.journalFollow = true
	m.agentJournal = []runstore.AgentJournalEntry{{
		TS: time.Now().UnixMilli(), Kind: "update", Message: strings.Repeat("wrapped progress ", 30), Source: "agent",
	}}
	m.loadInspect(true)
	m.setSize(50, 20)
	if !m.journalFollow || !m.ivp.AtBottom() {
		t.Fatalf("resize left FOLLOW away from bottom: follow=%v offset=%d", m.journalFollow, m.ivp.YOffset)
	}
}

func TestWrapAndTruncatePreserveUnicodeGraphemes(t *testing.T) {
	wrapped := wrap(strings.Repeat("🙂界", 12), 20)
	if !utf8.ValidString(wrapped) {
		t.Fatalf("wrap produced invalid UTF-8: %q", wrapped)
	}
	for _, line := range strings.Split(wrapped, "\n") {
		if width := ansi.StringWidth(line); width > 20 {
			t.Fatalf("wrapped line width = %d, want <= 20: %q", width, line)
		}
	}
	truncated := truncLine("🙂界🙂界", 5)
	if !utf8.ValidString(truncated) || ansi.StringWidth(truncated) > 8 {
		t.Fatalf("truncate damaged unicode: %q width=%d", truncated, ansi.StringWidth(truncated))
	}
}

func TestInspectorLongResultCanBeSelectedFocusedAndLineScrolled(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{
		{T: "agent_start", ID: 1, Label: "short-result"},
		{T: "agent_start", ID: 2, Label: "long-result"},
	})
	longResult := "result line 000\n"
	for i := 1; i < 100; i++ {
		longResult += fmt.Sprintf("result line %03d\n", i)
	}
	m.applyCompletions([]runstore.JournalEntry{
		{ID: 1, Label: "short-result", Prompt: "short task", Result: "done"},
		{ID: 2, Label: "long-result", Prompt: "long task", Result: longResult},
	})
	m.inspecting = true
	m.loadInspect(true)

	// Select the second agent from the roster, focus its detail, then move
	// Journal -> Task -> Result using the same path available to a user.
	m, _ = m.update(runeKey('j'))
	if m.selectedAgentID() != 2 {
		t.Fatalf("selected agent = %d, want long-result agent 2", m.selectedAgentID())
	}
	m, _ = m.update(key(tea.KeyEnter))
	if !m.inspectFocus {
		t.Fatal("enter did not focus selected agent detail")
	}
	m, _ = m.update(key(tea.KeyRight))
	m, _ = m.update(key(tea.KeyRight))
	if m.inspectMode != inspectResult || !strings.Contains(m.ivp.View(), "result line 000") {
		t.Fatalf("selected completion did not open in Result mode: mode=%v body=%q", m.inspectMode, m.ivp.View())
	}
	if m.ivp.AtBottom() {
		t.Fatal("long result did not create a scrollable viewport")
	}

	before := m.ivp.YOffset
	m, _ = m.update(runeKey('j'))
	if m.ivp.YOffset != before+1 {
		t.Fatalf("j scrolled Result to %d, want %d", m.ivp.YOffset, before+1)
	}
	m, _ = m.update(runeKey('k'))
	if m.ivp.YOffset != before {
		t.Fatalf("k scrolled Result to %d, want %d", m.ivp.YOffset, before)
	}
	m, _ = m.update(key(tea.KeyDown))
	if m.ivp.YOffset != before+1 {
		t.Fatalf("down arrow scrolled Result to %d, want %d", m.ivp.YOffset, before+1)
	}
	m, _ = m.update(key(tea.KeyUp))
	if m.ivp.YOffset != before {
		t.Fatalf("up arrow scrolled Result to %d, want %d", m.ivp.YOffset, before)
	}
}

func TestInspectorKeepsTailingLifecycleEventsLive(t *testing.T) {
	m := testRunsModel()
	m.applyEvents([]runstore.Event{{T: "agent_start", ID: 1, Label: "first", Phase: "work"}})
	m.inspecting = true
	m.applyRefresh(runsRefreshMsg{
		generation: m.generation,
		runID:      m.selectedID(),
		eventRead:  true,
		eventNext:  80,
		events: []runstore.Event{
			{T: "agent_journal", TS: time.Now().UnixMilli(), ID: 1, Kind: "decision", Preview: "keep the small design"},
			{T: "agent_start", ID: 2, Label: "second", Profile: "reviewer", Phase: "review"},
			{T: "agent_run", ID: 2},
		},
	}, false)

	if m.inspectorAgentCount() != 2 || m.inspectorAgentAt(1).id != 2 || m.inspectorAgentAt(1).status != "running" {
		t.Fatalf("live inspector missed lifecycle append: count=%d second=%#v", m.inspectorAgentCount(), m.inspectorAgentAt(1))
	}
	if m.agents[1].journalCount != 1 || m.agents[1].journalPreview != "keep the small design" {
		t.Fatalf("live inspector missed aggregate journal event: %#v", m.agents[1])
	}
}

func TestInspectorLegacyCachedCompletionHasGracefulFallback(t *testing.T) {
	m := testRunsModel()
	m.inspecting = true
	m.applyCompletions([]runstore.JournalEntry{{
		ID: 5, Label: "legacy-worker", Profile: "cached", Prompt: "legacy task", Result: "legacy result", Cached: true,
	}})
	m.reconcileAgentSelection(0)
	m.agentJournalLoaded = true
	m.loadInspect(true)

	if m.inspectorAgentCount() != 1 || m.selectedAgentID() != 5 {
		t.Fatalf("completion-only legacy roster missing: count=%d selected=%d", m.inspectorAgentCount(), m.selectedAgentID())
	}
	if body := m.ivp.View(); !strings.Contains(body, "cached result") || !strings.Contains(body, "No live work journal") {
		t.Fatalf("cached no-journal fallback missing:\n%s", body)
	}
	if view := m.viewAgentList(); !strings.Contains(view, "legacy-worker") || !strings.Contains(view, "cached · no live journal") {
		t.Fatalf("legacy cached row missing:\n%s", view)
	}
	m.setInspectMode(inspectTask)
	if !strings.Contains(m.ivp.View(), "legacy task") {
		t.Fatalf("legacy task not exposed: %q", m.ivp.View())
	}
	m.setInspectMode(inspectResult)
	if !strings.Contains(m.ivp.View(), "legacy result") {
		t.Fatalf("legacy result not exposed: %q", m.ivp.View())
	}
}

func TestRefreshRequestsAreCoalesced(t *testing.T) {
	m := testRunsModel()
	first := m.requestRefresh(false)
	if first == nil || !m.refreshing {
		t.Fatal("first request did not start")
	}
	if second := m.requestRefresh(true); second != nil || !m.refreshQueued || !m.forceList {
		t.Fatal("overlapping request was not coalesced")
	}
	next := m.applyRefresh(runsRefreshMsg{generation: m.generation, runID: m.selectedID()}, true)
	if next == nil || !m.refreshing || m.refreshQueued {
		t.Fatal("coalesced request did not start after response")
	}
}

func TestReadRunsRefreshTailsRequestedFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, runstore.RunsDir())
	dir := filepath.Join(runstore.RunsDir(), "wf_async")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initial := "{\"t\":\"log\",\"msg\":\"first\"}\n"
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	completion := "{\"id\":1,\"label\":\"worker\",\"profile\":\"test\",\"key\":\"k\",\"prompt\":\"task\",\"result\":\"done\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(completion), 0o644); err != nil {
		t.Fatal(err)
	}
	agentPath, err := runstore.AgentJournalPath("wf_async", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	agentEntry := fmt.Sprintf("{\"ts\":%d,\"kind\":\"progress\",\"message\":\"working\",\"source\":\"agent\",\"agentId\":1}\n", time.Now().UnixMilli())
	if err := os.WriteFile(agentPath, []byte(agentEntry), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := readRunsRefresh(runsRefreshRequest{
		generation:       7,
		runID:            "wf_async",
		agentID:          1,
		readEvents:       true,
		readJournal:      true,
		readAgentJournal: true,
	})
	if !msg.eventRead || len(msg.events) != 1 || msg.events[0].Msg != "first" {
		t.Fatalf("initial refresh = %#v", msg)
	}
	if !msg.journalRead || len(msg.journal) != 1 || msg.journal[0].ID != 1 {
		t.Fatalf("completion ledger was not tailed alongside events: %#v", msg)
	}
	if !msg.agentJournalRead || msg.agentID != 1 || len(msg.agentJournal) != 1 || msg.agentJournal[0].Message != "working" {
		t.Fatalf("selected work journal was not tailed alongside events and completions: %#v", msg)
	}
	appended := "{\"t\":\"log\",\"msg\":\"second\"}\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(appended); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	next := readRunsRefresh(runsRefreshRequest{
		generation:  7,
		runID:       "wf_async",
		eventOffset: msg.eventNext,
		readEvents:  true,
	})
	if !next.eventRead || len(next.events) != 1 || next.events[0].Msg != "second" {
		t.Fatalf("incremental refresh = %#v", next)
	}
}

func TestTicksStopOutsideRunsTab(t *testing.T) {
	m := model{tab: tabProfiles, runs: testRunsModel()}
	updated, cmd := m.Update(tickMsg{generation: m.tickGeneration})
	got := updated.(model)
	if cmd != nil || got.frame != 0 || got.runs.refreshing {
		t.Fatalf("inactive tick changed model: frame=%d refreshing=%v cmd=%v", got.frame, got.runs.refreshing, cmd != nil)
	}

	m.tab = tabRuns
	updated, cmd = m.Update(tickMsg{generation: m.tickGeneration})
	got = updated.(model)
	if cmd == nil || got.frame != 1 || !got.runs.refreshing {
		t.Fatalf("runs tick did not schedule refresh: frame=%d refreshing=%v cmd=%v", got.frame, got.runs.refreshing, cmd != nil)
	}
}

func TestVisibleLogsAreBounded(t *testing.T) {
	m := testRunsModel()
	events := make([]runstore.Event, maxVisibleLogs+25)
	for i := range events {
		events[i] = runstore.Event{T: "log", Msg: fmt.Sprintf("log-%d", i)}
	}
	m.applyEvents(events)
	if len(m.logs) != maxVisibleLogs || m.omittedLogs != 25 {
		t.Fatalf("logs=%d omitted=%d, want %d and 25", len(m.logs), m.omittedLogs, maxVisibleLogs)
	}
	if m.logs[0] != "log-25" {
		t.Fatalf("oldest retained log = %q, want log-25", m.logs[0])
	}
}

func testRunsModel() runsModel {
	m := newRunsModel()
	m.setSize(120, 30)
	m.runs = []runstore.Meta{{
		ID:        "wf_test",
		Name:      "test",
		Status:    "running",
		StartedAt: time.Now().Add(-time.Minute),
	}}
	m.catalogLoaded = true
	m.resetLoaded("wf_test")
	return m
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
