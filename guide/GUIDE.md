# Dyna workflow guide

`dyna` runs plain JavaScript workflow files that orchestrate model workers
registered as profiles. The script owns deterministic control flow—fan-out,
barriers, pipelines, loops, and conditionals—while each `agent()` call delegates
one bounded task to a real agent CLI.

## When to use Dyna

Run Dyna when the user explicitly asks for Dyna, a workflow, agent fan-out, or
multi-model orchestration, or when an invoked skill or instruction requires it.
It is a good fit for work where independent coverage or verification is the
point: broad audits, parallel review, adversarial checks, judge panels, and
large migrations with isolated implementers.

A workflow can start many paid worker sessions, so do not infer permission for
that scale merely because it could improve an ordinary task. Use the harness's
built-in subagents for small context-local delegation, or describe the fleet and
ask before running it.

The usual shape is hybrid:

1. **Scout inline.** List files, inspect the diff, and discover the actual work
   list with cheap local commands.
2. **Orchestrate one coherent phase.** Fan out review, transformation, or
   verification over that concrete list.
3. **Read the result before escalating.** Let the result determine whether the
   next run should implement, judge, synthesize, or stop.

If a prompt contains Dyna's run-owned work-journal instructions, that session
is already a Dyna worker. It must never load the Dyna skill, call `dyna run`, or
recursively orchestrate Dyna. Its only permitted Dyna command is `dyna journal`.
Native harness subagents remain subject to the selected profile, and a profile
with `disableSubagents` requires the worker to complete the task itself.

## Start with the registered profiles

```bash
dyna profiles list --json
dyna guide --plain
```

Profiles bind a harness and model to a human description and three standardized
stats. All stats are 1–10 and higher is better:

- `cost` is cost efficiency, so 10 means very cheap. Use high-cost-stat workers
  for wide sweeps, first-pass triage, and mechanical breadth. A high cost stat
  is not a capability warning: breadth stages — finders, sweeps, discovery,
  first drafts — default to the cheapest capable profile, and premium profiles
  enter only where their stat is uniquely needed.
- `intelligence` is capability on hard, long, or complex work. Use it for root
  cause analysis and implementation.
- `taste` is judgment, design sensibility, and polish — quality over quantity.
  Use it for code review, verification, judge panels, synthesis, and
  frontend/design work.

Pick by the stage's dominant stat, then use the description to confirm or veto
the choice. A common route is cheap to gather, intelligent to implement, and
tasteful to decide. A high-taste profile is not an implementation workhorse:
broad implementation routes by `intelligence`, and taste-heavy workers write
code only for quality-critical surfaces (frontend/design work) or targeted
remediation of findings a review already confirmed.

The script's `profiles` global is an array snapshot of enabled profiles:

```js
const best = stat => [...profiles].sort((a, b) => b[stat] - a[stat])[0]
const sweeper = best('cost').name
const builder = best('intelligence').name
const judge = best('taste').name
```

Each entry exposes `name`, `description`, `harness`, `model`, `taste`,
`intelligence`, `cost`, `default`, `disableSubagents`, `maxConcurrent`, and
`maxCallsPerRun`. It also exposes a live, read-only `remaining` value: capped
profiles report how many more live calls the run may issue, while unlimited
profiles report `null`. The value decreases when a call is accepted and does
not replenish when that call completes because `maxCallsPerRun` is a lifetime
run budget. Disabled profiles do not appear, and naming one explicitly rejects
the call. Omitting `profile` uses the enabled default profile, or the first
enabled profile when no default is marked.

Profile limits matter before fan-out:

- `maxConcurrent` queues excess live calls to that profile.
- `maxCallsPerRun` counts logical `agent()` calls. The first live call beyond
  the limit rejects immediately and guarantees that the workflow ultimately
  fails. Calls already accepted continue to completion and are journaled
  normally, so successful results remain reusable with `--resume`.
- The run also has a global concurrency limit and lifetime agent-call cap. The
  defaults are `max(2, min(16, CPU cores - 2))` live workers and 1000 calls;
  override them with `--max-concurrent` and `--max-agents`.

## Script and metadata contract

Scripts are JavaScript, not TypeScript. Top-level `await` is allowed, and the
top-level `return` value becomes the workflow's JSON result. There is no Node.js
`require`, filesystem API, or package import surface; filesystem work belongs
in worker agents. Standard JavaScript values and control flow are available,
and `sleep(ms)` is provided for polling loops.

Put an optional metadata literal first:

```js
export const meta = {
  name: 'review-change',
  description: 'Review a target through independent lenses',
  phases: [{ title: 'Review' }, { title: 'Verify' }],
}
```

`meta` is a convention, not a validated runtime schema. When `--name` is
omitted, the CLI uses a light textual scan of `meta.name` for the display name.
`description` and `phases` document intent; actual progress groups come from
`phase(title)` calls and `agent({phase: ...})` options.

The `args` global is the JSON value parsed from `--args`, or `undefined` when
the flag is omitted. Shell callers pass JSON text:

```bash
dyna run review.js --args '{"target":"internal/engine"}'
```

Arrays stay arrays, objects stay objects, and scalars stay scalars. Dyna does
not parse a JSON string nested inside that value.

## Public JavaScript API

### `agent(prompt, opts?)`

`agent()` starts one independent worker and returns a promise. Without a
schema it resolves to the worker's final text. With a schema it resolves to the
validated JSON value.

Supported options are:

- `profile`: enabled profile name. Omit it for the default profile.
- `label`: progress label. It does not affect execution or resume matching.
- `phase`: explicit progress group. Prefer this inside concurrent callbacks so
  global `phase()` changes cannot interleave groups.
- `schema`: a JSON Schema object. Dyna appends a JSON-only response contract,
  extracts a JSON value from the result, validates it, and retries with the
  validation error up to two times, for three attempts total. A bad schema or
  a third invalid response rejects the call. Bare scalar results must be the
  complete response or a fenced JSON block; object and array results may also
  be extracted from surrounding prose.
- `cwd`: worker working directory. It defaults to the run's `--dir`, which
  defaults to the shell's current directory.
- `timeout`: positive seconds. It overrides a profile timeout. With neither,
  the default is five hours. Explicit script and profile values are clamped to
  a 30-minute minimum.
- `isolation: 'worktree'`: create a detached temporary git worktree at `HEAD`
  and run the worker there. A clean tree is removed; a changed tree is kept and
  its path is logged and stored with the call. Dyna never merges it.

Worktree isolation does not copy uncommitted changes from the source checkout,
because it starts at committed `HEAD`. If a later pipeline stage needs the
changed tree, have the implementer return its absolute `pwd` and pass that as
the reviewer's `cwd`. Without isolation, parallel mutators must have disjoint
scopes or they can overwrite each other.

Every `agent()` call sees only its own prompt, profile configuration, and
working directory. It cannot see caller conversation history or sibling
results unless the script includes them in its prompt.

### `parallel(thunks)`

`parallel([() => promise, ...])` starts all thunks and is an all-results
barrier. It waits until every thunk settles, preserves input order, logs
rejections, and puts `null` in each rejected position. The `parallel()` call
itself does not reject because a child thunk failed.

Use a barrier when the next step genuinely requires the full set, such as
cross-result deduplication, comparison among candidates, or an early exit when
the total finding count is zero. Filter or account for `null` before using the
result.

### `pipeline(items, ...stages)`

`pipeline()` runs every item through its stages independently. Stage callbacks
receive `(previousResult, originalItem, index)`. There is no stage barrier: item
A may enter verification while item B is still transforming. A stage that
throws or returns a rejected promise logs the error, turns that item into
`null`, and skips its remaining stages.

Prefer `pipeline()` for independent multi-stage items. Use `parallel()` between
stages only when a later stage needs cross-item context from all earlier
results; conceptual phase boundaries and ordinary map/filter operations do not
justify the added barrier.

### Progress and globals

- `phase(title)` updates the default progress group for subsequently created
  agents.
- `log(message)` emits a narrator line to the event stream and TUI, and to
  stderr unless the run uses `--quiet`. Messages are truncated to 4000 characters.
- `sleep(ms)` returns a promise for pacing a bounded polling loop.
- `args` is the parsed `--args` JSON value or `undefined`.
- `profiles` is the enabled profile snapshot described above.

## Workflow shape: parallel by default

Shape follows dependencies, not caution. Once a run is authorized, its cost is
set by how many `agent()` calls it makes, not by their arrangement; serializing
independent calls spends the same tokens and only adds wall-clock. Express cost
caution as fewer calls or cheaper profiles, never as sequencing.

Name the work list before writing the script. Scout inline until the concrete
items exist—files, modules, findings, tasks—then make the script's top level
`pipeline(workList, ...stages)` over that list. If you cannot enumerate the
items, you are not ready to orchestrate.

Default to `pipeline()`. Two consecutive `await agent()` calls are justified
only when the second prompt interpolates the first result. "The steps are
conceptually separate" and "it feels safer" are not dependencies:

```js
// Smell: independent calls in sequence — nothing here uses `engine`.
const engine = await agent('Audit internal/engine for races.')
const store = await agent('Audit internal/store for races.')

// Same cost, a fraction of the wall-clock:
const [engineReport, storeReport] = await parallel([
  () => agent('Audit internal/engine for races.'),
  () => agent('Audit internal/store for races.'),
])
```

A `parallel()` barrier between stages is justified only when the next stage
needs cross-item context from all earlier results: deduplication across the
full set, comparison among candidates, or an early exit when the total count
is zero. It is not justified by ordinary map/filter transforms, conceptual
phase boundaries, or tidier code. If you wrote

```js
const found = await parallel(finders)                        // barrier
const flat = found.filter(Boolean).flatMap(r => r.findings)  // plain transform
const checked = await parallel(flat.map(f => () => agent(verifyPrompt(f))))
```

and the middle transform has no cross-item dependency, rewrite it as one
`pipeline()` with the transform inside a stage, so a fast finder's findings
enter verification while slow finders are still running.

For implementation work, partitioning is the orchestrator's job. Split the
change into disjoint scopes—per package, module, or file set—so no two writers
ever touch the same files, then fan out one implementer per partition with
`isolation: 'worktree'` (or disjoint `cwd` scopes) and stream each partition
into its own review and verification stages, as in Example 3 below. Parallel
implementation over a clean partition is the expected shape for large changes,
not an elevated risk; the risk lives in a bad partition, so spend the scouting
effort there.

A full remediation run chains the routes end to end: cheap finders sweep the
target in parallel, taste verifiers adversarially confirm each finding as it
arrives, intelligence implementers fix confirmed findings in disjoint
worktrees, taste reviewers judge each diff, and implementers apply the
touch-ups the reviews demand — all streaming through `pipeline()` stages, so
nothing waits for a phase barrier.

## Example 1: parallel structured review

This barrier is intentional: the workflow returns one merged report only after
all independent lenses finish.

```js
export const meta = {
  name: 'parallel-review',
  description: 'Review one target through independent lenses',
  phases: [{ title: 'Review' }],
}

const FINDINGS = {
  type: 'object',
  required: ['findings'],
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        required: ['file', 'line', 'problem', 'evidence', 'severity'],
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          problem: { type: 'string' },
          evidence: { type: 'string' },
          severity: { enum: ['low', 'medium', 'high'] },
        },
      },
    },
  },
}

const target = (args && args.target) || '.'
const reviewer = [...profiles].sort((a, b) => b.taste - a.taste)[0].name
const lenses = ['correctness', 'security', 'concurrency']

phase('Review')
const reports = await parallel(lenses.map(lens => () =>
  agent(
    `Review ${target} for ${lens} defects. Report only concrete, line-cited findings.`,
    { profile: reviewer, label: `review:${lens}`, phase: 'Review', schema: FINDINGS },
  )
))

const completed = reports.filter(Boolean)
log(`${completed.length}/${lenses.length} review lenses completed`)
return {
  complete: completed.length === lenses.length,
  findings: completed.flatMap(report => report.findings),
}
```

Run it with:

```bash
dyna run parallel-review.js --args '{"target":"internal/engine"}'
```

## Example 2: streaming transform and verify

Each transformed item enters verification immediately, without waiting for
the other transforms.

```js
export const meta = {
  name: 'transform-verify',
  description: 'Transform independent text items and verify each result',
  phases: [{ title: 'Transform' }, { title: 'Verify' }],
}

const TRANSFORMED = {
  type: 'object',
  required: ['id', 'output'],
  properties: {
    id: { type: 'string' },
    output: { type: 'string' },
  },
}
const VERDICT = {
  type: 'object',
  required: ['pass', 'issues'],
  properties: {
    pass: { type: 'boolean' },
    issues: { type: 'array', items: { type: 'string' } },
  },
}

const builder = [...profiles].sort((a, b) => b.intelligence - a.intelligence)[0].name
const judge = [...profiles].sort((a, b) => b.taste - a.taste)[0].name

const results = await pipeline(
  args.items,
  item => agent(
    `Apply this rule: ${args.rule}\nInput: ${JSON.stringify(item)}`,
    { profile: builder, label: `transform:${item.id}`, phase: 'Transform', schema: TRANSFORMED },
  ),
  (changed, original) => agent(
    `Verify the transformation obeys the rule without losing meaning.\n` +
    `Rule: ${args.rule}\nOriginal: ${JSON.stringify(original)}\n` +
    `Transformed: ${JSON.stringify(changed)}`,
    { profile: judge, label: `verify:${original.id}`, phase: 'Verify', schema: VERDICT },
  ).then(verdict => ({ original, changed, verdict })),
)

return { results, dropped: results.filter(x => x === null).length }
```

```bash
dyna run transform-verify.js --args \
  '{"rule":"make concise without changing facts","items":[{"id":"a","text":"..."},{"id":"b","text":"..."}]}'
```

## Example 3: isolated implementation followed by review

Each implementation gets its own worktree. A changed worktree survives long
enough for the next stage because the implementer returns its absolute path.

```js
export const meta = {
  name: 'isolated-implement-review',
  description: 'Implement independent tasks in worktrees and review each diff',
  phases: [{ title: 'Implement' }, { title: 'Review' }],
}

const IMPLEMENTED = {
  type: 'object',
  required: ['worktree', 'summary', 'tests'],
  properties: {
    worktree: { type: 'string' },
    summary: { type: 'string' },
    tests: { type: 'array', items: { type: 'string' } },
  },
}
const REVIEW = {
  type: 'object',
  required: ['approved', 'findings'],
  properties: {
    approved: { type: 'boolean' },
    findings: { type: 'array', items: { type: 'string' } },
  },
}

const builder = [...profiles].sort((a, b) => b.intelligence - a.intelligence)[0].name
const reviewer = [...profiles].sort((a, b) => b.taste - a.taste)[0].name

const results = await pipeline(
  args.tasks,
  task => agent(
    `Implement this task and run focused tests: ${task}. ` +
    `Before returning, run pwd and report that absolute worktree path.`,
    {
      profile: builder,
      label: `implement:${task}`,
      phase: 'Implement',
      cwd: args.repo,
      isolation: 'worktree',
      schema: IMPLEMENTED,
    },
  ),
  (implementation, task) => agent(
    `Review the implementation of "${task}" in this checkout. ` +
    `Inspect git diff against HEAD, run focused tests, and do not edit files.\n` +
    `Implementer report: ${JSON.stringify(implementation)}`,
    {
      profile: reviewer,
      label: `review:${task}`,
      phase: 'Review',
      cwd: implementation.worktree,
      schema: REVIEW,
    },
  ).then(review => ({ task, implementation, review })),
)

return { results, worktreesNeedManualIntegration: true }
```

```bash
dyna run isolated-implement-review.js --args \
  '{"repo":"/absolute/path/to/repo","tasks":["fix parser edge case","add retry metric"]}'
```

Dyna keeps changed worktrees and reports their paths, but it does not cherry-pick,
merge, or delete them. The orchestrator or user must inspect and integrate them.

## Quality patterns

### Adversarial verification

Ask independent skeptics to refute a claim and keep it only when a conservative
majority cannot refute it. Failed votes remain missing votes, not implicit
support:

```js
const votes = await parallel([1, 2, 3].map(i => () =>
  agent(
    `Independently try to refute this claim using repository evidence. ` +
    `Default to refuted=true if uncertain. Claim: ${claim}`,
    { profile: judge, label: `refute:${i}`, schema: VERDICT },
  )
))
const survives = votes.filter(Boolean).filter(v => !v.refuted).length >= 2
```

Use distinct lenses—correctness, security, reproducibility, performance—when
the claim can fail in different ways. Diversity catches more failure modes than
three identical prompts.

### Judge panel

Generate several independent candidates from materially different angles,
score them with independent judges against an explicit schema, then give a
synthesizer the candidates and scores. Preserve the winner's strengths and
graft in specific ideas from runners-up; do not ask one agent to both propose
and declare itself best.

### Completeness and convergence

For unknown-size discovery, deduplicate against everything already `seen` and
continue until two consecutive finder rounds produce nothing new. Deduplicating
only against confirmed findings lets rejected claims reappear forever. Finish
with a completeness critic that asks which search modality, subsystem, claim,
or verification step is still missing; feed concrete omissions into the next
round.

If the workflow samples, caps, or skips work, use `log()` and return coverage
counts. Silent truncation looks like comprehensive success.

## Failure and result behavior

| Situation | Runtime behavior |
| --- | --- |
| Direct, uncaught `agent()` rejection | The workflow fails. |
| Rejected thunk inside `parallel()` | The position becomes `null`; the failure is logged. |
| Throwing/rejected pipeline stage | That item becomes `null`; later stages for it are skipped. |
| Invalid schema output | Dyna asks again twice, then rejects after the third invalid result. |
| Non-cancellation harness/API failure | Dyna may make one bounded continuation in the exact same resumable session; it never substitutes a fresh worker as recovery. |
| Agent timeout | The call rejects as canceled/timed out; timeout recovery does not extend the deadline. |
| Profile `maxCallsPerRun` exceeded | The excess call rejects and the run fails even inside `parallel()` or `pipeline()`, but accepted calls drain and journal normally before failure is surfaced. |
| Global `--max-agents` exceeded | That `agent()` call rejects; containment follows the direct/parallel/pipeline rules above. |
| Worktree setup failure | The call rejects; no worker starts. |

Do not return only `results.filter(Boolean)` and call the run complete. Preserve
or calculate expected/completed/dropped counts so the caller can distinguish a
clean result from degraded coverage.

## Running, detaching, and inspecting

```bash
dyna run <script.js> [--args '<json>'] [--name NAME] [--dir PATH]
         [--json] [--quiet] [--max-concurrent N] [--max-agents N]
         [--detach] [--resume <run-id>]

dyna runs list [--json] [--session <id>]
dyna runs show <run-id> [--json]
dyna runs wait <run-id> [--timeout SECONDS]
dyna runs steer <run-id> <agent-id> "message"
dyna runs pause <run-id> | dyna runs unpause <run-id>
dyna runs cancel <run-id>
dyna runs remove <run-id>... | dyna runs clear
dyna tui [--session <id>]
```

Foreground `dyna run` streams progress to stderr and prints the workflow result
JSON to stdout. `--json` wraps it in a `{runId,status,result}` envelope.

`--detach` starts the same command in a background process, redirects its output
to the run's `daemon.log`, and prints the preassigned run id immediately. Use
`dyna runs wait <id>` to block for the final result; its `--timeout` limits only
how long the waiter waits and does not cancel the workflow.

`pause` prevents new workers from launching while current workers finish.
`cancel` stops the workflow and its in-flight worker process groups. `steer`
gracefully interrupts an active resumable worker and continues that exact
session with the message; unsupported sessions reject instead of launching a
replacement.

`dyna pi` launches the built-in root agent preset `dyna-orchestrator` with the
bundled extension and a full, self-contained tool-native Dyna prompt appended
directly to the root system prompt — the same routing, workflow-shape,
example, and quality-pattern guidance as this guide, phrased for the
extension's native tools — while preserving every other Pi skill. Its sessions default to the `dyna-orchestrator` display
name and show `agent:dyna-orchestrator` in the footer. At session start the
preset activates every registered Pi tool, including all built-ins, the four
native Dyna tools, and tools from other extensions; explicit `--tools`/`-t`,
`--exclude-tools`/`-xt`, `--no-tools`/`-nt`, and `--no-builtin-tools`/`-nbt`
controls override that default. A separately installed Dyna Pi skill uses
`disable-model-invocation: true`, which retains manual
`/skill:dyna` use in plain Pi without duplicating the launch prompt in model
discovery.

The model calls `dyna_profiles` to route work, uses `write` to create complete
workflow JavaScript at a unique `/tmp/dyna-workflow-*.js` path, then passes that
path to `dyna_run`. Every `dyna_run` invocation is detached and promptly returns
its run ID, so `dyna_runs` and `dyna_steer` remain available while it runs. The
extension invokes the exact Dyna binary without a shell, privately consumes
bounded workflow input, and rejects show/wait/cancel/resume/steer requests for
runs outside the persisted Pi session. Type `/dyna` to open the Pi-native Dyna
dashboard scoped to that persisted Pi session. It replaces the editor while
open and detaches the chat scrollback from the renderer so off-screen updates
cannot force full-screen repaints; Pi keeps running underneath and still
reacts when a workflow completes, and closing the dashboard restores and
redraws the conversation with everything that happened meanwhile. A direct
`dyna tui` remains a global cross-session dashboard unless `--session <id>` is
supplied explicitly.

Pi's bundled `openai-codex/gpt-5.6-terra` metadata already reports the correct
372K context window. Pi 0.80.7 exposes context usage and manual compaction to
extensions, but it has no public session-local API for changing the automatic
compaction threshold, so Dyna leaves model metadata and global settings alone.

## Journals and live progress

Each run persists under the Dyna data directory:

```text
runs/<run-id>/
├── script.js
├── meta.json
├── events.jsonl
├── journal.jsonl
├── agents/<agent-id>/journal.jsonl
├── daemon.log                 # detached runs
└── result.json                # successful returned value
```

The root `journal.jsonl` is Dyna's completed-call/resume ledger: it stores call
keys, prompts, results, errors, cache status, and retained worktree paths. Each
agent journal is the worker's live progress side channel and starts before the
worker process. It does not alter the `agent()` result or satisfy a schema.

Dyna prepends the journal and no-recursion contract to every worker prompt.
Reinforce this cadence in substantial tasks: write once after orientation, on
meaningful findings, decisions, verification, or blockers, before a long
operation, and before finishing.

```bash
dyna journal "found the ownership boundary" \
  --kind finding --next "trace both callers"
```

Kinds are `update`, `finding`, `decision`, `verification`, and `blocker`.
Messages should be one or two sentences plus an optional next step, recording
outcomes rather than chain-of-thought or command transcripts. The CLI supplies
the timestamp and appends one complete JSONL record safely.

Read-only exploration still treats the journal as its only allowed write. For
an explicitly read-only Codex profile, Dyna replaces the read-only sandbox flag
with a custom permission profile that keeps the workspace read-only and grants
write access only to that worker's run-owned agent directory. Other provider or
custom read-only modes are not automatically bypassed.

After five minutes without an agent-authored entry, a safely resumable built-in
session is gracefully interrupted and continued in the exact same session with
a write-now-and-continue reminder. A fast resumable worker that finishes with no
entry may get one bounded immediate reminder in that same session while its
original successful result is preserved. Non-resumable/custom sessions are
marked quiet or missing-entry; Dyna never starts a replacement worker solely to
obtain a journal entry.

## Resume semantics

Resume is workflow-level replay, separate from same-session harness recovery,
journal reminders, and live steering:

```bash
dyna run workflow.js --resume wf_20260714-120000-deadbeef
```

Dyna loads successful prior ledger entries into queues keyed by the resolved
profile name, exact prompt, and serialized schema. Matching calls replay
immediately in occurrence order without consuming a live worker slot. This is
key matching, not source-line or longest-prefix matching.

Labels, phases, `cwd`, timeout, isolation, and the overall `args` value are not
part of the key. If an argument should invalidate work, interpolate it into the
prompt or schema, or select a different profile. Failed calls always rerun.
Calls that retained a changed worktree have a stored directory and are not
cached; a successful isolated call that made no changes has no retained
directory and can be reused.

Workflow loading warns when the script uses `Date.now()`, `new Date()`, or
`Math.random()`, because prompts or schemas derived from those values change
their cache keys on every execution. Pass timestamps and random seeds through
`args` when replayability matters. After a resumed run, Dyna reports how many
prior journaled calls were replayed and warns when the hit rate is suspiciously
low.

Before diagnosing an unexpected resumed result, inspect the previous run's
root journal and `dyna runs show <id> --json`; cached calls can faithfully replay
an earlier empty or semantically weak success.

## Permissions and profile execution

Claude Code and Codex workers run headlessly. Unless a profile enables
`safeMode` or supplies explicit permission arguments, Dyna adds those harnesses'
permission-bypass flags so approval prompts cannot hang. OpenCode, Pi, and
custom profiles run with their configured command and arguments rather than a
universal Dyna sandbox policy.

`disableSubagents` adds a final worker-prompt restriction for every harness and
uses verified native controls for Claude Code and Codex. It prevents the worker
from delegating to child agents; it does not prevent Dyna from launching that
worker. This is a strong policy/configuration guard, not a security boundary
against a worker that can invoke arbitrary CLIs through a shell.

## Common mistakes

- **Inferring expensive scale.** A task that could benefit from ten workers is
  not permission to start ten paid sessions; get explicit opt-in.
- **Recursing from a Dyna worker.** Worker prompts already contain the boundary;
  only the root orchestrator may launch Dyna workflows.
- **Routing bulk implementation to a taste-max profile.** Taste is quality over
  quantity — review, judging, synthesis, design polish, and targeted
  remediation. Broad implementation routes by `intelligence`.
- **Neglecting cheap profiles.** A high cost stat is not a capability warning;
  breadth stages default to the cheapest capable profile, not to whichever
  profile is most impressive.
- **Serializing independent work.** Back-to-back `await agent()` calls are
  justified only when the later prompt uses the earlier result; independent
  calls belong in `parallel()` or `pipeline()`.
- **Expressing cost caution as sequencing.** Cost is the number of calls, not
  their arrangement; shrink the fleet, do not serialize it.
- **Using a barrier for aesthetics.** If verification needs only its own prior
  item, use `pipeline()` and let it stream.
- **Relying on global phase state in concurrent callbacks.** Pass `phase` in
  each `agent()` option.
- **Assuming failures vanished.** `null` means dropped work, not an empty clean
  result; report coverage.
- **Parsing prose by hand.** Use `schema` for data that another stage consumes.
- **Ignoring profile caps.** A call-limit overrun aborts the entire run.
- **Expecting worktree isolation to include dirty source changes.** It starts at
  committed `HEAD`, and Dyna does not merge retained worktrees.
- **Changing only a label before resume.** Labels and phases do not invalidate
  the profile/prompt/schema cache key.
- **Setting a ten-minute timeout.** It is clamped to the 30-minute minimum.
- **Treating the journal as the result.** Workers must still return the final
  text or schema value requested by `agent()`.

Scale the workflow to the user's words. A quick bug hunt needs a few finders
and one verification pass; a thorough audit can justify broader lenses,
multiple adversarial votes, explicit coverage accounting, and a final
completeness critic. Breadth is the cost dial; concurrency is not — a large
implementation fans out over a disjoint partition instead of running one
worker at a time.
