package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillDocumentsAgentJournalContract(t *testing.T) {
	required := []string{
		"If your instructions include a run-owned dyna journal",
		"do not use this skill",
		"only permitted dyna command is `dyna journal`",
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
		"defaults to\n   5 hours",
		"30-minute minimum; shorter values are clamped",
		"watch them appear live",
	}
	for _, contract := range required {
		if !strings.Contains(skillBody, contract) {
			t.Errorf("skillBody is missing journal contract %q", contract)
		}
	}
}

func TestGuidanceInstallUninstallPreservesUserContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	userContent := "# My instructions\n\nKeep this line.\n"
	if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	target := harnessTarget{guidancePath: func() string { return path }}

	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{userContent, guidanceMarkBegin, guidanceMarkEnd, "# Multi-model workflows with dyna", "use only `dyna journal`"} {
		if !strings.Contains(string(first), required) {
			t.Fatalf("installed guidance is missing %q:\n%s", required, first)
		}
	}

	stale := string(first)
	bodyStart := strings.Index(stale, guidanceMarkBegin) + len(guidanceMarkBegin)
	bodyEnd := strings.Index(stale, guidanceMarkEnd)
	stale = stale[:bodyStart] + "\nstale managed content\n" + stale[bodyEnd:]
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) || strings.Count(string(second), guidanceMarkBegin) != 1 || strings.Contains(string(second), "stale managed content") {
		t.Fatalf("guidance install did not replace its marker block in place:\n%s", second)
	}
	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	third, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(third) != string(second) {
		t.Fatalf("guidance install was not idempotent:\n%s", third)
	}

	removed, err := uninstallGuidance(target)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("guidance block was not reported removed")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != userContent {
		t.Fatalf("uninstall changed surrounding user content: got %q, want %q", after, userContent)
	}
}

func TestGuidanceUninstallRemovesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	target := harnessTarget{guidancePath: func() string { return path }}
	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	if _, err := uninstallGuidance(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("guidance-only file still exists: %v", err)
	}
}

func TestGuidanceMalformedBlockPreservesUserContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	content := "# My instructions\n\n" + guidanceMarkBegin + "\nkeep this user-authored suffix\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	target := harnessTarget{guidancePath: func() string { return path }}

	if err := installGuidance(target); err == nil {
		t.Fatal("install accepted a managed begin marker without an end marker")
	}
	if removed, err := uninstallGuidance(target); err == nil || removed {
		t.Fatalf("uninstall malformed block = (%v, %v), want (false, error)", removed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != content {
		t.Fatalf("malformed block handling changed user content: got %q, want %q", after, content)
	}
}

func TestSkillUninstallAlsoRemovesGuidance(t *testing.T) {
	dir := t.TempDir()
	target := harnessTarget{
		path:         func() string { return filepath.Join(dir, "skills", "dyna", "SKILL.md") },
		guidancePath: func() string { return filepath.Join(dir, "AGENTS.md") },
	}
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	removed, err := uninstallSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("skill uninstall reported nothing removed")
	}
	for _, path := range []string{target.path(), target.guidancePath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed file %s still exists: %v", path, err)
		}
	}
}
