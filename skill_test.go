package main

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
		"ultracode", "<task-notification>", "/workflows", "StructuredOutput",
		"workflow(name", "budget.remaining",
		"agentType", "opts.effort", "saved workflow", "nested workflow",
		"4096 items",
	}
	for name, body := range map[string]string{
		"guidance": agentFacingGuidance,
		"guide":    guideMD,
	} {
		for _, term := range forbidden {
			if strings.Contains(body, term) {
				t.Errorf("%s contains unsupported workflow concept %q", name, term)
			}
		}
	}
}

func TestGuideDocumentsRuntimeContractAndRunnableExamples(t *testing.T) {
	required := []string{
		"## When to use Dyna",
		"## Public JavaScript API",
		"meta` is a convention, not a validated runtime schema",
		"three attempts total",
		"no stage barrier",
		"## Workflow shape: parallel by default",
		"Shape follows dependencies, not caution",
		"Name the work list before writing the script",
		"partitioning is the orchestrator's job",
		"Serializing independent work",
		"Expressing cost caution as sequencing",
		"full remediation run chains the",
		"quality over quantity",
		"not an implementation workhorse",
		"Routing bulk implementation to a taste-max profile",
		"Neglecting cheap profiles",
		"## Example 1: parallel structured review",
		"## Example 2: streaming transform and verify",
		"## Example 3: isolated implementation followed by review",
		"## Quality patterns",
		"### Adversarial verification",
		"### Judge panel",
		"### Completeness and convergence",
		"## Failure and result behavior",
		"## Journals and live progress",
		"## Resume semantics",
		"profile name, exact prompt, and serialized schema",
		"This is\nkey matching, not source-line or longest-prefix matching",
		"## Common mistakes",
	}
	for _, contract := range required {
		if !strings.Contains(guideMD, contract) {
			t.Errorf("guide is missing contract or example %q", contract)
		}
	}
}

func TestPiOrchestrationPromptIsFullAndSelfContained(t *testing.T) {
	for _, required := range []string{
		"Dyna is enabled for this Pi launch",
		"complete Dyna reference for this session",
		"do not search for or load a separate dyna skill",
		"Use Dyna workflows by default for code changes",
		"Work directly only when",
		"Call dyna_profiles first",
		"quality over quantity",
		"never as bulk implementation workhorses",
		"cheapest capable profile",
		"/tmp/dyna-workflow-*.js path and call",
		"dyna_run with workflow_path",
		"validation attempts, then the call rejects",
		"committed HEAD",
		"profiles.find(p => p.default) ?? profiles[0]",
		"profile: profile.name",
		"pipeline(items",
		"schema: { type: 'object'",
		"isolation: 'worktree'",
		"Shape follows dependencies, not caution",
		"pipeline(workList, ...stages)",
		"one\nimplementer per partition with worktree isolation",
		"implement-partitioned",
		"byStat('intelligence')",
		"cwd: impl.worktree",
		"two consecutive finder rounds add nothing new",
		"not part of that key",
		"full remediation run chains the",
		"Use dyna_runs to list, show, wait for, or cancel",
		"dyna_steer",
		"type /dyna",
		"dashboard scoped to this persisted Pi",
		"direct dyna tui",
		"replaces the editor while open and restores it when closed",
	} {
		if !strings.Contains(piOrchestrationPrompt, required) {
			t.Errorf("Pi orchestration prompt is missing %q", required)
		}
	}
	for _, forbidden := range []string{"dyna guide", "profile: 'reviewer'", "dyna run workflow.js", "dyna profiles list --json", "Use Dyna only when the user explicitly requests", "inline JavaScript to dyna_run", "dyna_run with detach true"} {
		if strings.Contains(piOrchestrationPrompt, forbidden) {
			t.Errorf("Pi orchestration prompt contains forbidden fallback %q", forbidden)
		}
	}
	if len(piOrchestrationPrompt) > 16000 {
		t.Fatalf("Pi orchestration prompt outgrew its full-reference budget: %d bytes", len(piOrchestrationPrompt))
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
