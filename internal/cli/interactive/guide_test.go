package interactive

import (
	"strings"
	"testing"
)

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

func TestGuideExcludesUnsupportedWorkflowConcepts(t *testing.T) {
	for _, term := range []string{
		"ultracode", "<task-notification>", "/workflows", "StructuredOutput",
		"workflow(name", "budget.remaining", "Date.now", "Math.random",
		"agentType", "opts.effort", "saved workflow", "nested workflow",
		"4096 items",
	} {
		if strings.Contains(guideMD, term) {
			t.Errorf("guide contains unsupported workflow concept %q", term)
		}
	}
}
