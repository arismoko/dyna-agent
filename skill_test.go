package main

import (
	"strings"
	"testing"
)

func TestSkillDocumentsAgentJournalContract(t *testing.T) {
	required := []string{
		"runs/<run-id>/agents/<agent-id>/journal.jsonl",
		"completed-call/resume ledger",
		"Dyna prepends the",
		"Reinforce them in every",
		`dyna journal "message" --kind update|finding|decision|verification|blocker`,
		"once after orientation",
		"before a long operation",
		"before finishing",
		"one or two sentences plus an optional next step",
		"not chain-of-thought",
		"read-only exploration",
		"only allowed write",
		"five minutes without a valid agent-authored entry",
		"exact same",
		"resumable built-in session",
		"fresh worker for a journal nudge",
		"grants write access only to its agent journal directory",
		"explicit read-only modes are not auto-bypassed",
		"fast resumable worker that finishes",
		"original result is preserved",
		"Non-resumable/custom sessions are only marked",
		"progress side channel",
		"not the worker's final response or schema output",
		"30-minute minimum timeout",
		"shorter script or profile values are clamped",
		"watch them appear live",
	}
	for _, contract := range required {
		if !strings.Contains(skillBody, contract) {
			t.Errorf("skillBody is missing journal contract %q", contract)
		}
	}
}
