package pi

import (
	"strings"
	"testing"

	"dyna-agent/internal/cli/guidance"
)

func TestPiOrchestrationPromptSharesGuidanceContent(t *testing.T) {
	for name, shared := range map[string]string{
		"profile routing":  guidance.ProfileRouting,
		"script contract":  guidance.ScriptContract,
		"workflow shape":   guidance.WorkflowShape,
		"quality patterns": guidance.QualityPatterns,
	} {
		if !strings.Contains(piOrchestrationPrompt, shared) {
			t.Errorf("%s is not present in the Pi orchestration prompt", name)
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
		"as bulk implementation workhorses",
		"cheapest capable profile",
		"/tmp/dyna-workflow-*.js path, then call",
		"dyna_run with workflow_path",
		"validation\nattempts, then the call rejects",
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
