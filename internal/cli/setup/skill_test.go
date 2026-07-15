package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentFacingGuidanceDocumentsCompactRuntimeContract(t *testing.T) {
	required := []string{
		"user explicitly asks for Dyna",
		"do not infer that\n  scale",
		"Scout inline first",
		"recursively orchestrate Dyna",
		"use only `dyna journal`",
		"dyna profiles list --json",
		"maxConcurrent",
		"maxCallsPerRun",
		"dyna guide",
		"export const meta",
		"agent(prompt, opts)",
		"workflow(nameOrRef, args)",
		"profile`, `label`, `phase`, `schema`, `cwd`, `timeout`",
		"isolation: 'worktree'",
		"at most\n   three attempts",
		"30-minute minimum",
		"all-results barrier",
		"streams each item through",
		"throwing stage makes that item `null`",
		"Shape follows dependencies, not caution",
		"pipeline(workList, ...stages)",
		"one implementer per partition",
		"expected shape, not an elevated",
		"quality over quantity",
		"taste-max profile",
		"cheapest capable profile",
		"full remediation run chains the",
		"dyna runs wait <id>",
		"--resume <id>",
		"matching profile, prompt, and schema",
		"Uncaught `agent()` errors fail the workflow",
		"agents/<agent-id>/journal.jsonl",
		"completed-call/resume\nledger",
		"after orientation",
		"before long operations",
		"before finishing",
		"progress\nside channel",
		"never replaces\nthe worker's final response or schema output",
		"never starts a replacement",
	}
	for name, body := range map[string]string{"skill": skillBody, "guidance": guidanceBody} {
		for _, contract := range required {
			if !strings.Contains(body, contract) {
				t.Errorf("%s body is missing contract %q", name, contract)
			}
		}
	}
	if skillBody != agentFacingGuidance || guidanceBody != agentFacingGuidance {
		t.Fatal("skill and managed guidance must share the canonical agent-facing contract")
	}
}

func TestAgentFacingDocsExcludeUnsupportedWorkflowConcepts(t *testing.T) {
	forbidden := []string{
		// "/workflows" (Claude Code's own slash command) and "workflow(name" are
		// deliberately not forbidden here: dyna's own workflow(nameOrRef, args)
		// primitive and its default registry path (.../dyna/workflows) are now
		// legitimately documented. "Date.now"/"Math.random" are also allowed:
		// the resume non-determinism warning legitimately names them as the
		// APIs that break --resume cache hits.
		"ultracode", "<task-notification>", "StructuredOutput", "budget.remaining",
		"agentType", "opts.effort", "saved workflow", "nested workflow",
		"4096 items",
	}
	for _, term := range forbidden {
		if strings.Contains(agentFacingGuidance, term) {
			t.Errorf("guidance contains unsupported workflow concept %q", term)
		}
	}
}

func TestPiOnlyToolsDoNotLeakIntoPortableGuidance(t *testing.T) {
	for _, tool := range []string{"dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer"} {
		if strings.Contains(agentFacingGuidance, tool) || strings.Contains(skillBody, tool) || strings.Contains(guidanceBody, tool) {
			t.Errorf("portable guidance contains Pi-only tool %q", tool)
		}
	}
}

func TestPiSkillIsManualOnlyForModelInvocation(t *testing.T) {
	dir := t.TempDir()
	piTarget := harnessTarget{name: "pi", path: func() string { return filepath.Join(dir, "pi", "SKILL.md") }}
	if err := installSkill(piTarget); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, piTarget.path()); !strings.Contains(got, "disable-model-invocation: true") {
		t.Fatalf("Pi skill is model-visible:\n%s", got)
	}

	portableTarget := harnessTarget{name: "codex", path: func() string { return filepath.Join(dir, "codex", "SKILL.md") }}
	if err := installSkill(portableTarget); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, portableTarget.path()); strings.Contains(got, "disable-model-invocation") {
		t.Fatalf("portable skill unexpectedly disabled model invocation:\n%s", got)
	}
}

func TestGuidanceCommandRequiresExplicitPiTarget(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", "")
	for _, dir := range []string{filepath.Join(homeDir, ".codex"), filepath.Join(homeDir, ".pi")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cmd := guidanceCmd()
	cmd.SetArgs([]string{"install"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".codex", "AGENTS.md")); err != nil {
		t.Fatalf("detected Codex guidance was not installed: %v", err)
	}
	piPath := filepath.Join(homeDir, ".pi", "agent", "AGENTS.md")
	if _, err := os.Stat(piPath); !os.IsNotExist(err) {
		t.Fatalf("no-argument guidance install wrote Pi guidance: %v", err)
	}

	explicit := guidanceCmd()
	explicit.SetArgs([]string{"install", "pi"})
	if err := explicit.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, piPath); !strings.Contains(got, guidanceMarkBegin) {
		t.Fatalf("explicit Pi guidance was not installed:\n%s", got)
	}
}

func TestPiTargetUsesPiAgentDirectory(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PI_CODING_AGENT_DIR", "")

	var pi harnessTarget
	for _, target := range skillTargets() {
		if target.name == "pi" {
			pi = target
			break
		}
	}
	agentDir := filepath.Join(homeDir, ".pi", "agent")
	if got, want := pi.path(), filepath.Join(agentDir, "skills", "dyna", "SKILL.md"); got != want {
		t.Fatalf("pi skill path = %q, want %q", got, want)
	}
	if got, want := pi.guidancePath(), filepath.Join(agentDir, "AGENTS.md"); got != want {
		t.Fatalf("pi guidance path = %q, want %q", got, want)
	}
}

func TestPiTargetHonorsCustomAgentDirectory(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	for _, target := range skillTargets() {
		if target.name != "pi" {
			continue
		}
		if got, want := target.path(), filepath.Join(agentDir, "skills", "dyna", "SKILL.md"); got != want {
			t.Fatalf("pi skill path = %q, want %q", got, want)
		}
		if got, want := target.guidancePath(), filepath.Join(agentDir, "AGENTS.md"); got != want {
			t.Fatalf("pi guidance path = %q, want %q", got, want)
		}
		return
	}
	t.Fatal("pi target not found")
}

func TestPiTargetExpandsTildeInCustomAgentDirectory(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PI_CODING_AGENT_DIR", "~/.custom-pi")

	for _, target := range skillTargets() {
		if target.name != "pi" {
			continue
		}
		if got, want := target.guidancePath(), filepath.Join(homeDir, ".custom-pi", "AGENTS.md"); got != want {
			t.Fatalf("pi guidance path = %q, want %q", got, want)
		}
		return
	}
	t.Fatal("pi target not found")
}

func TestGuidanceInstallMigratesLegacyPath(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "agent", "AGENTS.md")
	legacy := filepath.Join(dir, "AGENTS.md")
	target := harnessTarget{
		path:               func() string { return filepath.Join(dir, "agent", "skills", "dyna", "SKILL.md") },
		guidancePath:       func() string { return current },
		legacyGuidancePath: func() string { return legacy },
	}
	userContent := "# User guidance\n\nKeep this.\n"
	if err := os.WriteFile(legacy, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertManagedBlock(legacy, guidanceMarkBegin, guidanceMarkEnd, guidanceBody); err != nil {
		t.Fatal(err)
	}
	if err := installGuidance(target); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, current); !strings.Contains(got, guidanceMarkBegin) {
		t.Fatalf("current guidance was not installed:\n%s", got)
	}
	if got := readFile(t, legacy); got != userContent {
		t.Fatalf("legacy user guidance changed: got %q, want %q", got, userContent)
	}
	if err := upsertManagedBlock(legacy, guidanceMarkBegin, guidanceMarkEnd, guidanceBody); err != nil {
		t.Fatal(err)
	}
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, legacy); got != userContent {
		t.Fatalf("skill install did not migrate legacy guidance: got %q, want %q", got, userContent)
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
