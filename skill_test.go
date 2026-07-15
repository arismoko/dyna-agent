package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentFacingGuidanceDocumentsCompactRuntimeContract(t *testing.T) {
	required := []string{
		"Dyna orchestration engaged — ready to fan out the fleet.",
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
		"attempts, then the call rejects",
		"30-minute minimum",
		"all-results barrier",
		"streams each item through",
		"throwing stage makes that item `null`",
		"Shape follows dependencies, not caution",
		"pipeline(workList, ...stages)",
		"one implementer per partition",
		"expected shape",
		"taste-heavy profile",
		"cheapest capable profile",
		"full remediation run chains the routes",
		"adversarial verification",
		"judge panel",
		"two consecutive finder rounds",
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
	for _, contract := range required {
		if !strings.Contains(skillBody, contract) {
			t.Errorf("skill body is missing contract %q", contract)
		}
	}
	if skillBody != agentFacingGuidance {
		t.Fatal("skill body must use the canonical agent-facing contract")
	}
	for name, shared := range map[string]string{
		"profile routing":  sharedProfileRoutingGuidance,
		"script contract":  sharedScriptContractGuidance,
		"workflow shape":   sharedWorkflowShapeGuidance,
		"quality patterns": sharedQualityPatternsGuidance,
	} {
		if !strings.Contains(agentFacingGuidance, shared) || !strings.Contains(piOrchestrationPrompt, shared) {
			t.Errorf("%s is not shared by the skill and Pi prompt", name)
		}
	}
}

func TestSkillFrontmatterDrivesExplicitDiscovery(t *testing.T) {
	for name, frontmatter := range map[string]string{"portable": skillFrontmatter, "pi": piSkillFrontmatter} {
		for _, required := range []string{
			"name: load-dyna-orchestrator",
			"user explicitly requests Dyna",
			"agent fan-out",
			"parallel review, audits, judge panels, adversarial verification, or isolated migrations",
			"do not infer that scale merely because it could help",
			"ordinary subagents for small context-local delegation",
			"describe the proposed fleet and ask",
		} {
			if !strings.Contains(frontmatter, required) {
				t.Errorf("%s frontmatter is missing %q", name, required)
			}
		}
	}
	if !strings.Contains(piSkillFrontmatter, "disable-model-invocation: true") {
		t.Fatal("Pi frontmatter no longer disables model invocation")
	}
}

func TestAgentFacingDocsExcludeUnsupportedWorkflowConcepts(t *testing.T) {
	forbidden := []string{
		"ultracode", "<task-notification>", "/workflows", "StructuredOutput",
		"workflow(name", "budget.remaining", "Date.now", "Math.random",
		"agentType", "opts.effort", "saved workflow", "nested workflow",
		"4096 items",
	}
	for name, body := range map[string]string{
		"skill": agentFacingGuidance,
		"guide": guideMD,
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
		"bulk implementation workhorses",
		"cheapest capable profile",
		"/tmp/dyna-workflow-*.js path",
		"dyna_run with workflow_path",
		"attempts, then the call rejects",
		"committed HEAD",
		"profiles.find(p => p.default) ?? profiles[0]",
		"profile: profile.name",
		"pipeline(items",
		"schema: { type: 'object'",
		"isolation: 'worktree'",
		"Shape follows dependencies, not caution",
		"pipeline(workList, ...stages)",
		"one implementer per partition with\nworktree isolation",
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
		if strings.Contains(agentFacingGuidance, tool) || strings.Contains(skillBody, tool) {
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

func TestSkillTargetsUseRenamedDirectoriesAndTrackLegacyDirs(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PI_CODING_AGENT_DIR", "")

	wantBases := map[string]string{
		"claude-code": filepath.Join(homeDir, ".claude", "skills"),
		"codex":       filepath.Join(homeDir, ".codex", "skills"),
		"opencode":    filepath.Join(homeDir, ".config", "opencode", "skills"),
		"pi":          filepath.Join(homeDir, ".pi", "agent", "skills"),
	}
	for _, target := range skillTargets() {
		base := wantBases[target.name]
		if got, want := target.path(), filepath.Join(base, "load-dyna-orchestrator", "SKILL.md"); got != want {
			t.Errorf("%s skill path = %q, want %q", target.name, got, want)
		}
		if got, want := target.legacySkillDir(), filepath.Join(base, "dyna"); got != want {
			t.Errorf("%s legacy skill dir = %q, want %q", target.name, got, want)
		}
	}
}

func TestGuidanceCommandOnlyUninstallsRetiredBlocks(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", "")
	path := filepath.Join(homeDir, ".codex", "AGENTS.md")
	userContent := "# User instructions\n\nKeep this.\n"
	writeRetiredGuidance(t, path, userContent, "stale guidance")

	cmd := guidanceCmd()
	if commands := cmd.Commands(); len(commands) != 1 || commands[0].Name() != "uninstall" {
		t.Fatalf("guidance subcommands = %v, want uninstall only", cmd.Commands())
	}
	cmd.SetArgs([]string{"uninstall", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != userContent {
		t.Fatalf("guidance cleanup changed user content: got %q, want %q", got, userContent)
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
	if got, want := pi.path(), filepath.Join(agentDir, "skills", "load-dyna-orchestrator", "SKILL.md"); got != want {
		t.Fatalf("pi skill path = %q, want %q", got, want)
	}
	if got, want := pi.legacySkillDir(), filepath.Join(agentDir, "skills", "dyna"); got != want {
		t.Fatalf("pi legacy skill dir = %q, want %q", got, want)
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
		if got, want := target.path(), filepath.Join(agentDir, "skills", "load-dyna-orchestrator", "SKILL.md"); got != want {
			t.Fatalf("pi skill path = %q, want %q", got, want)
		}
		if got, want := target.legacySkillDir(), filepath.Join(agentDir, "skills", "dyna"); got != want {
			t.Fatalf("pi legacy skill dir = %q, want %q", got, want)
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

func TestSkillInstallMigratesLegacySkillAndGuidance(t *testing.T) {
	dir := t.TempDir()
	currentGuidance := filepath.Join(dir, "agent", "AGENTS.md")
	legacyGuidance := filepath.Join(dir, "AGENTS.md")
	legacySkillDir := filepath.Join(dir, "agent", "skills", "dyna")
	target := harnessTarget{
		name:               "codex",
		path:               func() string { return filepath.Join(dir, "agent", "skills", "load-dyna-orchestrator", "SKILL.md") },
		legacySkillDir:     func() string { return legacySkillDir },
		guidancePath:       func() string { return currentGuidance },
		legacyGuidancePath: func() string { return legacyGuidance },
	}
	userContent := "# User guidance\n\nKeep this.\n"
	if err := os.MkdirAll(legacySkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySkillDir, "SKILL.md"), []byte("old skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRetiredGuidance(t, currentGuidance, userContent, "current guidance")
	writeRetiredGuidance(t, legacyGuidance, userContent, "legacy guidance")

	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacySkillDir); !os.IsNotExist(err) {
		t.Fatalf("legacy skill directory still exists: %v", err)
	}
	if got := readFile(t, target.path()); !strings.Contains(got, "name: load-dyna-orchestrator") {
		t.Fatalf("new skill was not installed:\n%s", got)
	}
	for _, path := range []string{currentGuidance, legacyGuidance} {
		if got := readFile(t, path); got != userContent {
			t.Fatalf("skill install changed user guidance at %s: got %q, want %q", path, got, userContent)
		}
	}
}

func TestGuidanceUninstallPreservesUserContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	userContent := "# My instructions\n\nKeep this line.\n"
	writeRetiredGuidance(t, path, userContent, "stale managed content")
	target := harnessTarget{guidancePath: func() string { return path }}

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
	writeRetiredGuidance(t, path, "", "retired guidance")
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
	skillPath := filepath.Join(filepath.Dir(path), "skills", "load-dyna-orchestrator", "SKILL.md")
	target := harnessTarget{
		path:         func() string { return skillPath },
		guidancePath: func() string { return path },
	}

	if removed, err := uninstallGuidance(target); err == nil || removed {
		t.Fatalf("uninstall malformed block = (%v, %v), want (false, error)", removed, err)
	}
	if err := installSkill(target); err == nil {
		t.Fatal("skill install accepted a malformed retired guidance block")
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Fatalf("skill was written despite failed guidance cleanup: %v", err)
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
	legacySkillDir := filepath.Join(dir, "skills", "dyna")
	target := harnessTarget{
		name:           "codex",
		path:           func() string { return filepath.Join(dir, "skills", "load-dyna-orchestrator", "SKILL.md") },
		legacySkillDir: func() string { return legacySkillDir },
		guidancePath:   func() string { return filepath.Join(dir, "AGENTS.md") },
	}
	if err := installSkill(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacySkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySkillDir, "SKILL.md"), []byte("old skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRetiredGuidance(t, target.guidancePath(), "", "retired guidance")
	removed, err := uninstallSkill(target)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("skill uninstall reported nothing removed")
	}
	for _, path := range []string{target.path(), legacySkillDir, target.guidancePath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed file %s still exists: %v", path, err)
		}
	}
}

func writeRetiredGuidance(t *testing.T, path, userContent, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := userContent
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += guidanceMarkBegin + "\n" + body + "\n" + guidanceMarkEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
