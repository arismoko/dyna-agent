# dyna: dynamic multi-agent workflows for any coding agent

`dyna` lets an agent (you) orchestrate fleets of other model workers
deterministically. You write a plain JavaScript workflow script that fans work
out to **worker profiles** the user has registered, then run it with
`dyna run script.js`. Control flow (loops, conditionals, fan-out) is code;
the intelligence is in the workers.

## The 60-second version

```bash
dyna profiles list --json   # see which workers you may use, with stats
dyna run review.js --args '{"target":"src/"}'
```

```js
export const meta = {
  name: 'review-changes',
  description: 'Review changed files across dimensions, verify each finding',
  phases: [{ title: 'Review' }, { title: 'Verify' }],
}

const DIMENSIONS = [
  { key: 'bugs', prompt: 'Find correctness bugs in ' + args.target },
  { key: 'perf', prompt: 'Find performance issues in ' + args.target },
]

const FINDINGS = {
  type: 'object', required: ['findings'],
  properties: { findings: { type: 'array', items: {
    type: 'object', required: ['title', 'file'],
    properties: { title: {type:'string'}, file: {type:'string'}, detail: {type:'string'} },
  }}},
}
const VERDICT = { type: 'object', required: ['isReal'], properties: {
  isReal: {type:'boolean'}, reason: {type:'string'} }}

const results = await pipeline(
  DIMENSIONS,
  d => agent(d.prompt, { profile: 'sol', label: `review:${d.key}`, phase: 'Review', schema: FINDINGS }),
  review => parallel(review.findings.map(f => () =>
    agent(`Adversarially verify (try to REFUTE): ${f.title} in ${f.file}. ${f.detail || ''}`,
      { profile: 'fable', label: `verify:${f.file}`, phase: 'Verify', schema: VERDICT })
      .then(v => ({ ...f, verdict: v }))
  ))
)
return { confirmed: results.flat().filter(Boolean).filter(f => f.verdict?.isReal) }
```

The script's `return` value is printed as JSON on stdout when the run ends.

## Choosing workers: profiles

Users register profiles (`dyna profiles add`, `dyna profiles init`, or the
TUI's profile wizard). Each has a description plus three standardized stats,
all **1-10, higher is better**:

- **taste**: code quality, judgment, design/frontend sense, review ability
- **intelligence**: raw capability on hard, long, complex tasks
- **cost**: cost efficiency (10 = very cheap to run, 1 = very expensive)

Disabled profiles never appear in your `profiles` global and calling them
fails. Only orchestrate with what you can see.

Read the descriptions: they tell you each worker's strengths and failure
modes (e.g. "tireless workhorse, writes correct but unpolished code, weak
frontend taste"). Match workers to stages:

- High **intelligence**, medium cost: long implementation grinds, hard debugging
- High **taste**: review, verification, judging panels, frontend/design work
- High **cost** stat (cheap): wide fan-outs, sweeps, dedup, first-pass triage

Scripts also get a `profiles` global (the registry as an array), so you can
select dynamically: `profiles.filter(p => p.cost >= 8).map(p => p.name)`.
Omitting `profile` in `agent()` uses the user's default profile.

Profiles may carry user-set limits, visible in `dyna profiles list --json`:
`maxConcurrent` (simultaneous workers of that profile) and `maxCallsPerRun`
(total calls per run). Concurrency limits queue automatically; you don't
need to do anything. Call limits are FATAL: the first call past the cap
**aborts the entire run** (a silently degraded result would still spend
money). Before writing a script, check `maxCallsPerRun` for each profile you
use and size fan-outs within it. Route bulk work to unlimited or cheap
profiles and reserve capped profiles for the few calls that need them.

## Script API

Scripts are plain JavaScript (NOT TypeScript) running in an async context;
use `await` at top level. `export const meta = { name, description, phases }`
at the top is encouraged; `meta.name` labels the run.

- `agent(prompt, opts?) -> Promise<string|object>`: run one worker turn.
  opts: `profile` (registered profile name), `label` (display name),
  `phase` (progress group), `schema` (JSON Schema; output is parsed,
  validated, retried up to 2 times, and returned as an object),
  `cwd` (working directory for the worker), `timeout` (seconds, with a
  30-minute minimum),
  `isolation: 'worktree'` (run the worker in a fresh detached git worktree;
  use when parallel workers mutate files. The tree is auto-removed if
  unchanged, kept and reported via `log` if the worker changed files).
  On a non-cancellation harness failure, dyna makes at most one continuation
  against the exact same worker session; it rejects if recovery is unavailable
  or also fails. `parallel`/`pipeline` absorb rejections to `null`.
- `parallel(thunks) -> Promise<any[]>`: run concurrently, BARRIER: waits for
  all. Failed thunks become `null`; `.filter(Boolean)` before use.
- `pipeline(items, ...stages) -> Promise<any[]>`: each item flows through all
  stages independently, NO barrier between stages. Stage callbacks receive
  `(prevResult, originalItem, index)`. A throwing stage drops the item to `null`.
- `phase(title)`: start a progress group; later `agent()` calls are shown
  under it (or pass `opts.phase` explicitly inside concurrent stages).
- `log(msg)`: narrator line shown to the user and stored in the run.
- `sleep(ms)`: pacing for polling loops.
- `args`: the value of `--args` (JSON), verbatim.
- `profiles`: registered worker profiles: `{name, description, harness, model, taste, intelligence, cost, default}[]`.

Workers are told (when you pass `schema`) to return raw JSON; otherwise you
get their final message as a string. Concurrency is capped (default
min(16, cores - 2)); extra `agent()` calls queue automatically.

**DEFAULT TO `pipeline()`.** Use `parallel()` as a barrier only when a stage
genuinely needs ALL prior results together (dedup across the full set,
early-exit on zero findings, "compare against the other findings" prompts).

## Quality patterns

- **Adversarial verify**: N independent skeptics per finding, each prompted
  to REFUTE; keep only findings a majority can't kill:
  ```js
  const votes = await parallel([1,2,3].map(() => () =>
    agent(`Try to refute: ${claim}. Default refuted=true if uncertain.`,
      { profile: 'fable', schema: VERDICT })))
  const survives = votes.filter(Boolean).filter(v => !v.refuted).length >= 2
  ```
- **Judge panel**: generate N attempts from different angles with different
  workers, score with parallel judges, synthesize from the winner.
- **Loop-until-dry**: for unknown-size discovery, keep spawning finders until
  2 consecutive rounds surface nothing new (dedup against everything *seen*,
  not just confirmed).
- **Cheap-first triage**: sweep with a high-cost-stat (cheap) worker, escalate
  only survivors to the expensive high-taste/intelligence workers.
- **Completeness critic**: a final agent that asks "what's missing?"; its
  answer seeds the next round.

## CLI reference

```bash
dyna profiles list [--json]        # registered workers + stats/descriptions
dyna profiles show <name>
dyna run <script.js> [--args '<json>'] [--name x] [--dir path] [--json] [--quiet]
         [--detach] [--resume <run-id>] [--max-agents N] [--max-concurrent N]
dyna runs list [--json]            # past/active runs
dyna runs show <id> [--json]       # events, result
dyna runs wait <id> [--timeout N]  # block until a run finishes, print result
dyna runs cancel <id>              # stop a running workflow (kills workers)
dyna runs pause <id> / unpause <id> # hold new worker launches / resume
dyna runs remove <id>... | clear   # delete finished runs
dyna journal "message" --kind update|finding|decision|verification|blocker [--next "..."]
dyna guide                         # this document
dyna tui                           # human dashboard (profiles + live runs)
```

`dyna run` streams progress to stderr and prints the workflow's JSON result to
stdout, so you can pipe it. Runs persist under the data dir. The root
`journal.jsonl` remains the completed-call/resume ledger (including worker
prompts and results), while each live worker writes progress to
`agents/<agent-id>/journal.jsonl`; the final workflow value is in
`result.json`.

- **Background runs**: `dyna run --detach script.js` prints the run id
  immediately; continue other work, then `dyna runs wait <id>` for the result.
- **Resume**: after a failure or script edit, `dyna run script.js --resume
  <previous-run-id>` replays agent calls whose (profile, prompt, schema) are
  unchanged instantly from the previous journal (shown as cached); edited or
  new calls run live. Failed calls always re-run. This workflow-level replay
  is separate from the harness's single same-session nudge for transient
  failures.
- Unless a profile opts into safe mode or an explicit read-only sandbox,
  workers run with harness permissions bypassed so headless approval prompts
  cannot hang them. Scope prompts accordingly.

## Agent journals

Dyna automatically prepends journal instructions to every `agent()` prompt
and gives every live worker its own run-owned file:

```text
~/.local/share/dyna/runs/<run-id>/
├── script.js                        # captured workflow
├── meta.json                        # run status and timestamps
├── events.jsonl                     # live run/agent event stream
├── journal.jsonl                    # completed-call/resume ledger
├── agents/<agent-id>/journal.jsonl  # live worker progress
└── result.json                      # final workflow value
```

Workers should use the CLI rather than editing JSONL directly:

```bash
dyna journal "found the ownership boundary" \
  --kind finding --next "trace the two callers"
```

Kinds are `update`, `finding`, `decision`, `verification`, and `blocker`;
`--next` is optional. Dyna timestamps and serializes the append. A normal
agent-authored record has this shape:

```json
{"ts":1783706645123,"kind":"finding","message":"Found the ownership boundary.","next":"Trace the two callers.","source":"agent"}
```

`ts` is Unix milliseconds and is populated by dyna, as is `source:"agent"`.
The worker supplies only `kind`, a non-empty `message`, and optional `next`.
Every record is one complete JSON object terminated by a newline; do not treat
the file as a JSON array or leave a partial final line. Dyna may add system
start, nudge, and completion records with metadata.

As the orchestrator, reinforce this cadence in worker prompts even though dyna
adds the base instructions: every worker writes once after orientation, at
meaningful discoveries, decisions, verification, and blockers, before a long
operation, and before finishing. Notes should be brief (one or two sentences
plus an optional next step) and state progress and outcomes, not
chain-of-thought, hidden reasoning, exhaustive logs, or command transcripts.

The rule includes read-only exploration: the worker treats its run-owned
journal as its only allowed write. For a codex profile that explicitly selects
a read-only sandbox or built-in permission profile, dyna replaces that setting
with a custom permission profile that still extends read-only and grants write
access only to the agent's journal directory. The target workspace remains
read-only. Other provider-managed or custom read-only modes are not bypassed;
if no narrow journal exception is available, the run records a missing entry.

If no valid agent-authored record arrives for five minutes, dyna marks the
worker quiet. A resumable built-in harness is gracefully interrupted so its
session can flush, then continued in the exact same session with a reminder to
write now and keep working; no fresh worker is launched. An explicitly
configured non-resumable or custom session can only be marked quiet, because
dyna cannot safely live-resume it.

If a resumable worker finishes before five minutes without any agent-authored
entry, dyna gives that same session one bounded, immediate reminder to write
before finishing, and preserves the original successful task result. For a
non-resumable worker, it records that this reminder was unavailable instead of
starting a duplicate session.

The journal is a side channel for progress. It does not alter the value
returned by `agent()` and does not satisfy its final `schema`; the worker must
still produce the requested final response separately.

In `dyna tui`, the run inspector centers this side channel: active agents stay
visible, journal entries form a live timeline, and each agent has **Journal**,
**Task**, and **Result** modes. Rows expose lifecycle status, entry count,
freshness, and nudge state. Use `enter`/`right` to focus, `left`/`right` to
switch modes, `j`/`k` or arrows to scroll, `g`/`G` for top/bottom, `f` to
follow new entries, and `esc` to return to the agents. The selected run's
events, completion ledger, and selected journal are tailed independently about
every 400 ms, so progress appears before the agent completes.

## Rules of thumb

1. Scout inline first (list files, scope the diff), then orchestrate over the
   discovered work-list.
2. Scale to the ask: "find bugs" means a few finders plus single-vote verify;
   "thoroughly audit" means a large finder pool, a 3-5 vote adversarial pass,
   and a synthesis stage.
3. If you bound coverage (top-N, sampling), `log()` what was dropped.
4. Prompt every worker to use its journal at the required milestones; use
   concise progress/next-step notes, never requests for chain-of-thought.
5. Each `agent()` starts an independent worker session, so pack all needed
   context into the prompt. A worker cannot see other agents or caller
   history; dyna continues only that exact session for bounded transient
   recovery or journal reminders and never substitutes a fresh worker.
6. Workers run as real agent CLIs in `cwd` and can read and edit files
   there. For parallel file mutation, give each worker a disjoint scope.
