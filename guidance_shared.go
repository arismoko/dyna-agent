package main

const sharedProfileRoutingGuidance = `## Profile routing

Route by the enabled profiles' 1-10 stats. A high ` + "`cost`" + ` score means a
profile is cheap enough for breadth, so finders, sweeps, discovery, and
first-pass triage default to the cheapest capable profile. Use
` + "`intelligence`" + ` for hard implementation. ` + "`taste`" + ` is quality over quantity:
use it for judgment, design sensibility, review, judging, synthesis,
frontend/design work, and targeted remediation of confirmed findings. Never
use taste-heavy profiles as bulk implementation workhorses. Pick by the
stage's dominant stat and confirm the choice against the profile description;
the common route is cheap to gather, intelligent to implement, and tasteful
to decide.

Disabled profiles are absent. Respect ` + "`maxConcurrent`" + ` and
` + "`maxCallsPerRun`" + `; the first call beyond a per-profile call cap aborts the
entire run.

`

const sharedScriptContractGuidance = `## Script contract

Scripts are plain JavaScript with top-level ` + "`await`" + ` and return a JSON value.
Their globals include parsed ` + "`args`" + ` and enabled ` + "`profiles`" + `. An optional
` + "`export const meta`" + ` documents the run, and ` + "`meta.name`" + ` supplies its
default display name.

` + "`agent(prompt, opts)`" + ` starts one independent worker. Supported options are
` + "`profile`" + `, ` + "`label`" + `, ` + "`phase`" + `, ` + "`schema`" + `, ` + "`cwd`" + `, ` + "`timeout`" + ` in seconds, and
` + "`isolation: 'worktree'`" + `. Each worker sees only its prompt and working
directory, so include all needed context. Schemas get up to three validation
attempts, then the call rejects. Calls default to five hours; positive script
timeouts override profile timeouts, and all explicit or profile values have a
30-minute minimum. Worktree isolation starts from committed HEAD. It removes a
clean tree and keeps and logs a changed tree; Dyna never merges it.

` + "`parallel(thunks)`" + ` is an all-results barrier. Rejected thunks are logged and
become ` + "`null`" + `. ` + "`pipeline(items, ...stages)`" + ` streams each item through its
stages independently; a throwing stage makes that item ` + "`null`" + ` and skips its
remaining stages. Prefer ` + "`pipeline`" + ` unless a later step truly needs all earlier
results together. Use explicit ` + "`phase`" + ` options inside concurrent callbacks;
` + "`phase(title)`" + ` groups progress, ` + "`log(message)`" + ` reports it, and ` + "`sleep(ms)`" + `
paces polling.

Uncaught ` + "`agent()`" + ` errors fail the workflow; only ` + "`parallel`" + ` and ` + "`pipeline`" + `
convert their contained failures to ` + "`null`" + `. Filter and account for those
values, and return expected, completed, and dropped counts when coverage
matters instead of silently claiming comprehensive success.

`

const sharedWorkflowShapeGuidance = `## Workflow shape

Shape follows dependencies, not caution: an authorized run's cost is its
number of ` + "`agent()`" + ` calls, not their arrangement, so serializing independent
calls saves nothing and wastes wall-clock time. Scout until the concrete work
items exist, then make the script's top level
` + "`pipeline(workList, ...stages)`" + `. Two consecutive ` + "`await agent()`" + ` calls are
justified only when the second prompt interpolates the first result; reserve
` + "`parallel()`" + ` barriers for stages that need all prior results together, such
as deduplication, cross-candidate judging, or a zero-count early exit.

For implementation, partition the change into disjoint scopes so no two
writers touch the same files, then fan out one implementer per partition with
worktree isolation and stream each partition into its own review and verify
stages. Parallel implementation over a clean partition is the expected shape.
A full remediation run chains the routes end to end: cheap finders sweep in
parallel, taste verifiers confirm each finding, intelligence implementers fix
confirmed findings in disjoint worktrees, taste reviewers judge each diff,
and implementers apply the touch-ups.

`

const sharedQualityPatternsGuidance = `## Quality patterns

For adversarial verification, ask several independent skeptics to refute each
claim, defaulting to refuted when uncertain, and keep only claims a
conservative majority cannot refute. Give them distinct lenses such as
correctness, security, and reproducibility because diversity catches more
failure modes than identical prompts.

For a judge panel, generate candidates from materially different angles,
score them with independent judges against an explicit schema, and synthesize
from the winner; never let one worker both propose and declare itself best.

For unknown-size discovery, deduplicate against everything already seen and
stop only after two consecutive finder rounds add nothing new, then run a
completeness critic asking which modality, subsystem, or verification is
still missing. If a run samples, caps, or skips work, log it and return
coverage counts.

`
