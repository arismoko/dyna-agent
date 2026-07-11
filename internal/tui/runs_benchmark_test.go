package tui

import (
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
)

func benchmarkRunsModel(agentCount int) runsModel {
	m := newRunsModel()
	m.setSize(140, 48)
	m.runs = []runstore.Meta{{
		ID:        "wf_benchmark",
		Name:      "benchmark",
		Status:    "running",
		StartedAt: time.Now().Add(-time.Minute),
	}}
	m.resetLoaded("wf_benchmark")
	events := []runstore.Event{{T: "phase", Title: "work"}}
	for i := 0; i < agentCount; i++ {
		events = append(events,
			runstore.Event{T: "agent_start", ID: i + 1, Label: fmt.Sprintf("agent-%d", i+1), Profile: "benchmark", Phase: "work"},
			runstore.Event{T: "agent_journal", TS: time.Now().UnixMilli(), ID: i + 1, Kind: "progress", Preview: "completed benchmark investigation"},
			runstore.Event{T: "agent_end", ID: i + 1, Status: "ok", DurMs: 1000, Preview: "completed benchmark work"},
		)
	}
	m.applyEvents(events)
	m.rebuildDetail(true)
	return m
}

func BenchmarkInspectorIdleView(b *testing.B) {
	for _, agents := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("agents=%d", agents), func(b *testing.B) {
			m := benchmarkRunsModel(agents)
			m.inspecting = true
			m.inspectFocus = true
			m.agentJournal = []runstore.AgentJournalEntry{{
				TS: time.Now().UnixMilli(), Kind: "progress", Message: "journal-first benchmark entry", Source: "agent", AgentID: 1,
			}}
			m.loadInspect(true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.view(i)
			}
		})
	}
}

func BenchmarkRunsIdleView(b *testing.B) {
	for _, agents := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("agents=%d", agents), func(b *testing.B) {
			m := benchmarkRunsModel(agents)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.view(i)
			}
		})
	}
}

func BenchmarkRunsAppendEvent(b *testing.B) {
	for _, agents := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("agents=%d", agents), func(b *testing.B) {
			m := benchmarkRunsModel(agents)
			logs := make([]runstore.Event, maxVisibleLogs)
			for i := range logs {
				logs[i] = runstore.Event{T: "log", Msg: "existing benchmark event"}
			}
			m.applyEvents(logs)
			m.rebuildDetail(true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.applyRefresh(runsRefreshMsg{
					generation: m.generation,
					runID:      m.selectedID(),
					events:     []runstore.Event{{T: "log", Msg: "new benchmark event"}},
					eventNext:  int64(i + 1),
					eventRead:  true,
				}, false)
			}
		})
	}
}
