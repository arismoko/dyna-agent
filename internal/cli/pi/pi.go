package pi

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/runstore"
)

//go:embed assets/dyna.ts
var piExtensionTS []byte

const piOrchestrationPrompt = `Dyna is enabled for this Pi launch. Treat these instructions as standing session guidance; they are the complete Dyna reference for this session. The launcher provides the Dyna extension directly, so do not search for or load a separate dyna skill.

Use Dyna workflows by default for code changes. Scout the concrete work list
inline first, then scale the workflow to the request. Work directly only when
the change is clearly small, straightforward, and easy to verify; honor an
explicit user direction not to use Dyna. If these instructions appear inside a Dyna worker
prompt with a run-owned journal, never orchestrate recursively; use only dyna
journal and obey any disableSubagents restriction.

## Routing and launch

Call dyna_profiles first. It returns only enabled profiles; route by the 1-10
stats (cost for breadth, intelligence for hard implementation, taste for review
and synthesis) and respect maxConcurrent and maxCallsPerRun — the first call
beyond a per-profile cap aborts the entire run. Taste is judgment, design
sensibility, and polish — quality over quantity: use taste-heavy workers for
review, judging, synthesis, frontend/design work, and targeted remediation of
confirmed findings, never as bulk implementation workhorses; broad
implementation routes by intelligence. A high cost stat is not a capability
warning: breadth stages — finders, sweeps, discovery, first-pass triage —
default to the cheapest capable profile rather than a premium one. Pick by the
stage's dominant stat and confirm or veto with the description; the common
route is cheap to gather, intelligent to implement, tasteful to decide. Then use write to
create the complete workflow at a unique /tmp/dyna-workflow-*.js path and call
dyna_run with workflow_path; do not put source in the dyna_run call or use shell
commands or CLI documentation for normal discovery. dyna_run always starts in
the background and promptly returns its run id.

## Script contract

Scripts are plain JavaScript, not TypeScript, with no Node.js require, import,
or filesystem surface; filesystem work belongs in workers. Scripts support
top-level await and return a JSON value. Their globals are args and enabled
profiles, plus phase(title), log(message), and sleep(ms). Optional
export const meta = { name: 'review' } names the run. Select profiles inside
the script, for example
const byStat = stat => [...profiles].sort((a, b) => b[stat] - a[stat])[0].name.

agent(prompt, opts) supports profile, label, phase, schema, cwd, timeout seconds,
and isolation: 'worktree'. Each worker sees only its own prompt and working
directory, so include all needed context in the prompt. Schemas get up to three
validation attempts, then the call rejects. Timeouts default to five hours and
explicit values clamp to a 30-minute minimum. Worktree isolation starts from
committed HEAD and never copies uncommitted changes; changed
worktrees are kept for deliberate integration and never merged by Dyna; clean
ones are removed. When a later stage needs a changed tree, have the implementer
run pwd and report the absolute path, then pass it as the next stage's cwd.

parallel is an all-results barrier and converts rejected thunks to null. pipeline
streams each item through stages independently; a throwing stage makes only that
item null and skips its remaining stages. Uncaught agent errors fail the
workflow, so filter and account for nulls; return expected/completed/dropped
counts instead of only results.filter(Boolean), because silent truncation looks
like comprehensive success.

Write this source to the temporary workflow file before calling dyna_run:

` + "```js" + `
const profile = profiles.find(p => p.default) ?? profiles[0];
if (!profile) throw new Error('No enabled Dyna profiles');
const checks = await parallel([
  () => agent('Review correctness.', { profile: profile.name, label: 'correctness' }),
  () => agent('Find missing tests.', { profile: profile.name, label: 'tests' }),
]);
return { checks: checks.filter(Boolean) };
` + "```" + `

For streaming work use pipeline(items, async item => agent(...), async result =>
agent(...)). For typed output pass schema: { type: 'object', required: ['ok'],
properties: { ok: { type: 'boolean' } } }. Use explicit phase options inside
concurrent callbacks; phase(title), log(message), and sleep(ms) are also global.

Shape follows dependencies, not caution: an authorized run's cost is its number
of agent calls, not their arrangement, so never serialize independent calls.
Scout the concrete work list, then make the script's top level
pipeline(workList, ...stages). Consecutive awaits are justified only when the
second prompt uses the first result; reserve parallel barriers for stages that
need all results together. For implementation, partition the change into
disjoint file scopes so no two writers touch the same files, fan out one
implementer per partition with worktree isolation, and stream each partition
into its own review stage. Parallel implementation over a clean partition is
the expected shape, not an elevated risk. A full remediation run chains the
routes end to end: cheap finders sweep in parallel, taste verifiers confirm
each finding, intelligence implementers fix confirmed findings in disjoint
worktrees, taste reviewers judge each diff, and implementers apply the
touch-ups — all streaming through pipeline stages.

## Large implementation runs

Partition first, then fan out. The canonical shape:

` + "```js" + `
export const meta = { name: 'implement-partitioned' }
const byStat = stat => [...profiles].sort((a, b) => b[stat] - a[stat])[0].name
const builder = byStat('intelligence')
const reviewer = byStat('taste')
const IMPLEMENTED = {
  type: 'object',
  required: ['worktree', 'summary'],
  properties: { worktree: { type: 'string' }, summary: { type: 'string' } },
}
const REVIEW = {
  type: 'object',
  required: ['approved', 'findings'],
  properties: {
    approved: { type: 'boolean' },
    findings: { type: 'array', items: { type: 'string' } },
  },
}
const results = await pipeline(
  args.partitions,
  p => agent(
    'Implement this task in this checkout: ' + p.task +
    '. Touch only files under ' + p.scope +
    '. Run focused tests. Before returning, run pwd and report the absolute path.',
    { profile: builder, label: 'implement:' + p.name, phase: 'Implement',
      isolation: 'worktree', schema: IMPLEMENTED },
  ),
  (impl, p) => agent(
    'Review the implementation of "' + p.task + '" in this checkout. ' +
    'Inspect git diff against HEAD, run focused tests, do not edit files. ' +
    'Implementer report: ' + JSON.stringify(impl),
    { profile: reviewer, label: 'review:' + p.name, phase: 'Review',
      cwd: impl.worktree, schema: REVIEW },
  ).then(review => ({ partition: p.name, impl, review })),
)
return { results, dropped: results.filter(r => r === null).length }
` + "```" + `

Dyna keeps each changed worktree and reports its path; integrating them
afterward is the orchestrator's job.

## Quality patterns

For adversarial verification, ask several independent skeptics to refute each
claim (default refuted=true when uncertain) and keep only claims a conservative
majority cannot refute; use distinct lenses such as correctness, security, and
reproducibility because diversity catches more failure modes than identical
prompts. For a judge panel, generate candidates from materially different
angles, score them with independent judges against an explicit schema, and
synthesize from the winner; never let one agent both propose and declare itself
best. For unknown-size discovery, deduplicate against everything already seen
and stop only after two consecutive finder rounds add nothing new, then run a
completeness critic asking what modality, subsystem, or verification is still
missing. If a run samples, caps, or skips work, log it and return coverage
counts.

## Runs, steering, and resume

Use dyna_runs to list, show, wait for, or cancel runs belonging to this Pi
session, and dyna_steer to redirect an active worker in its existing session.
resume reuses successful calls whose profile, exact prompt, and schema match;
labels, phases, cwd, timeout, isolation, and args are not part of that key, so
interpolate anything that must invalidate a call into its prompt. Failed calls
and calls that kept a changed worktree always rerun.
The CLI is an implementation-detail fallback. In interactive Pi, type /dyna to
open the Pi-native Dyna dashboard scoped to this persisted Pi session. It
replaces the editor while open and restores it when closed. A direct dyna tui
invocation remains global unless its optional session filter is supplied.

Dyna gives every worker an append-only agents/<agent-id>/journal.jsonl progress
side channel. Workers journal after orientation, meaningful findings or
decisions, before long operations, after verification, on blockers, and before
finishing. The journal never replaces the worker's final response.`

const (
	piDefaultProvider     = "openai-codex"
	piDefaultModel        = "gpt-5.6-terra"
	piDefaultThinking     = "xhigh"
	piRootAgent           = "dyna-orchestrator"
	piCodexAuthEnv        = "DYNA_PI_CODEX_AUTH"
	piActivateAllToolsEnv = "DYNA_PI_ACTIVATE_ALL_TOOLS"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "pi [-- pi-args...]",
		Short:              "Launch pi with dyna workflows wired in (extension, instructions, session-dashboard /dyna)",
		DisableFlagParsing: true,
		RunE:               runPi,
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func runPi(c *cobra.Command, args []string) error {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		return fmt.Errorf("pi is not installed (npm install -g @earendil-works/pi-coding-agent)")
	}

	extPath, err := provisionPiExtension()
	if err != nil {
		return fmt.Errorf("provision pi extension: %w", err)
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	args = piNormalizeArgs(args)
	piArgs := []string{"--extension", extPath, "--append-system-prompt", piOrchestrationPrompt}
	piArgs = append(piArgs, piDefaultArgs(args)...)
	piArgs = append(piArgs, args...)
	cmd := exec.Command(piPath, piArgs...)
	// Each Pi session has a persisted UUID. The extension reads that UUID from
	// SessionManager and passes it only to child Dyna runs, so resumed sessions
	// retain their run ownership across separate `dyna pi` processes.
	cmd.Env = unsetEnv(os.Environ(), runstore.SessionEnv)
	if piHasExplicitToolControl(args) {
		cmd.Env = unsetEnv(cmd.Env, piActivateAllToolsEnv)
	} else {
		cmd.Env = setEnv(cmd.Env, piActivateAllToolsEnv, "1")
	}
	if !piHasFlag(args, "--api-key") {
		cmd.Env = setEnv(cmd.Env, piCodexAuthEnv, "1")
	} else {
		cmd.Env = setEnv(cmd.Env, piCodexAuthEnv, "0")
	}
	if exe, err := os.Executable(); err == nil {
		cmd.Env = setEnv(cmd.Env, "DYNA_BIN", exe)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runPiProcess(cmd)
}

func piNormalizeArgs(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		name, value, equals := strings.Cut(arg, "=")
		if equals {
			switch name {
			case "--provider", "--model", "--models", "--thinking", "--api-key", "--name", "-n", "--tools", "-t", "--exclude-tools", "-xt":
				normalized = append(normalized, name, value)
				continue
			}
		}
		normalized = append(normalized, arg)
	}
	return normalized
}

func piDefaultArgs(args []string) []string {
	defaults := make([]string, 0, 8)
	if !piHasFlag(args, "--provider") && !piHasFlag(args, "--model") && !piHasFlag(args, "--models") {
		defaults = append(defaults, "--provider", piDefaultProvider, "--model", piDefaultModel)
	}
	if !piHasFlag(args, "--thinking") && !piModelHasThinking(args) && !piModelScopeHasThinking(args) {
		defaults = append(defaults, "--thinking", piDefaultThinking)
	}
	if !piHasFlag(args, "--name") && !piHasFlag(args, "-n") {
		defaults = append(defaults, "--name", piRootAgent)
	}
	return defaults
}

func piHasExplicitToolControl(args []string) bool {
	for _, name := range []string{"--tools", "-t", "--exclude-tools", "-xt", "--no-tools", "-nt", "--no-builtin-tools", "-nbt"} {
		if piHasFlag(args, name) {
			return true
		}
	}
	return false
}

func piHasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func piFlagValue(args []string, name string) string {
	value := ""
	for i, arg := range args {
		if strings.HasPrefix(arg, name+"=") {
			value = strings.TrimPrefix(arg, name+"=")
		}
		if arg == name && i+1 < len(args) {
			value = args[i+1]
		}
	}
	return value
}

func piModelScopeHasThinking(args []string) bool {
	for _, model := range strings.Split(piFlagValue(args, "--models"), ",") {
		if piThinkingSuffix(model) {
			return true
		}
	}
	return false
}

func piModelHasThinking(args []string) bool {
	return piThinkingSuffix(piFlagValue(args, "--model"))
}

func piThinkingSuffix(model string) bool {
	if i := strings.LastIndex(model, ":"); i >= 0 {
		model = model[i+1:]
	} else {
		return false
	}
	switch model {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func provisionPiExtension() (string, error) {
	dir := filepath.Join(runstore.DataDir(), "pi-extension")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dyna.ts")
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, piExtensionTS) {
		if err := os.WriteFile(path, piExtensionTS, 0o644); err != nil {
			return "", err
		}
	}
	return path, nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}
