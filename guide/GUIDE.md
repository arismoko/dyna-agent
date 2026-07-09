# dyna — dynamic multi-agent workflows for any coding agent

`dyna` lets an agent (you) orchestrate fleets of other model workers
deterministically. You write a plain-JavaScript workflow script that fans work
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
  d => agent(d.prompt, { profile: 'gpt-5.5-xhigh', label: `review:${d.key}`, phase: 'Review', schema: FINDINGS }),
  review => parallel(review.findings.map(f => () =>
    agent(`Adversarially verify — try to REFUTE: ${f.title} in ${f.file}. ${f.detail || ''}`,
      { profile: 'opus-4.8', label: `verify:${f.file}`, phase: 'Verify', schema: VERDICT })
      .then(v => ({ ...f, verdict: v }))
  ))
)
return { confirmed: results.flat().filter(Boolean).filter(f => f.verdict?.isReal) }
```

The script's `return` value is printed as JSON on stdout when the run ends.

## Choosing workers: profiles

Users register profiles (`dyna profiles add` or the TUI). Each has a
description plus three standardized stats, all **1–5, higher is better**:

- **taste** — code quality, judgment, design/frontend sense, review ability
- **intelligence** — raw capability on hard, long, complex tasks
- **cost** — cost efficiency (5 = very cheap to run, 1 = very expensive)

Read the descriptions — they tell you each worker's personality (e.g. "tireless
workhorse, writes correct but unpolished code, weak frontend taste"). Match
workers to stages:

- High **intelligence**, medium cost → long implementation grinds, hard debugging
- High **taste** → review, verification, judging panels, frontend/design work
- High **cost** (cheap) → wide fan-outs, sweeps, dedup, first-pass triage

Scripts also get a `profiles` global — the registry as an array — so you can
select dynamically: `profiles.filter(p => p.cost >= 4).map(p => p.name)`.
Omitting `profile` in `agent()` uses the user's default profile.

Profiles may carry user-set limits, visible in `dyna profiles list --json`:
`maxConcurrent` (simultaneous workers of that profile) and `maxCallsPerRun`
(total calls per run). Concurrency limits queue automatically — you don't
need to do anything. Call limits are FATAL: the first call past the cap
**aborts the entire run** (a silently degraded result would still spend
money). Before writing a script, check `maxCallsPerRun` for each profile you
use and size fan-outs within it — route bulk work to unlimited/cheap
profiles, reserve capped profiles for the few calls that need them.

## Script API

Scripts are plain JavaScript (NOT TypeScript) running in an async context —
use `await` at top level. `export const meta = { name, description, phases }`
at the top is encouraged; `meta.name` labels the run.

- `agent(prompt, opts?) → Promise<string|object>` — run one worker turn.
  opts: `profile` (registered profile name), `label` (display name),
  `phase` (progress group), `schema` (JSON Schema — output is parsed,
  validated, auto-retried up to 2×, and returned as an object),
  `cwd` (working directory for the worker), `timeout` (seconds),
  `isolation: 'worktree'` (run the worker in a fresh detached git worktree —
  use when parallel workers mutate files; the tree is auto-removed if
  unchanged, kept and reported via `log` if the worker changed files).
  Rejects on worker failure — `parallel`/`pipeline` absorb rejections to `null`.
- `parallel(thunks) → Promise<any[]>` — run concurrently, BARRIER: waits for
  all. Failed thunks become `null`; `.filter(Boolean)` before use.
- `pipeline(items, ...stages) → Promise<any[]>` — each item flows through all
  stages independently, NO barrier between stages. Stage callbacks receive
  `(prevResult, originalItem, index)`. A throwing stage drops the item to `null`.
- `phase(title)` — start a progress group; later `agent()` calls are shown
  under it (or pass `opts.phase` explicitly inside concurrent stages).
- `log(msg)` — narrator line shown to the user and stored in the run.
- `sleep(ms)` — pacing for polling loops.
- `args` — the value of `--args` (JSON), verbatim.
- `profiles` — registered worker profiles: `{name, description, harness, model, taste, intelligence, cost, default}[]`.

Workers are told (when you pass `schema`) to return raw JSON; otherwise you
get their final message as a string. Concurrency is capped (default
min(16, cores−2)); extra `agent()` calls queue automatically.

**DEFAULT TO `pipeline()`.** Use `parallel()` as a barrier only when a stage
genuinely needs ALL prior results together (dedup across the full set,
early-exit on zero findings, "compare against the other findings" prompts).

## Quality patterns

- **Adversarial verify** — N independent skeptics per finding, each prompted
  to REFUTE; keep only findings a majority can't kill:
  ```js
  const votes = await parallel([1,2,3].map(() => () =>
    agent(`Try to refute: ${claim}. Default refuted=true if uncertain.`,
      { profile: 'opus-4.8', schema: VERDICT })))
  const survives = votes.filter(Boolean).filter(v => !v.refuted).length >= 2
  ```
- **Judge panel** — generate N attempts from different angles with different
  workers, score with parallel judges, synthesize from the winner.
- **Loop-until-dry** — for unknown-size discovery, keep spawning finders until
  2 consecutive rounds surface nothing new (dedup against everything *seen*,
  not just confirmed).
- **Cheap-first triage** — sweep with a high-cost-stat (cheap) worker, escalate
  only survivors to the expensive high-taste/intelligence workers.
- **Completeness critic** — a final agent that asks "what's missing?"; its
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
dyna guide                         # this document
dyna tui                           # human dashboard (profiles + live runs)
```

`dyna run` streams progress to stderr and prints the workflow's JSON result to
stdout, so you can pipe it. Runs persist under the data dir; full per-agent
prompts/results are in `journal.jsonl`, the result in `result.json`.

- **Background runs**: `dyna run --detach script.js` prints the run id
  immediately; continue other work, then `dyna runs wait <id>` for the result.
- **Resume**: after a failure or script edit, `dyna run script.js --resume
  <previous-run-id>` — agent calls whose (profile, prompt, schema) are
  unchanged replay instantly from the previous journal (`⚡cached`); edited or
  new calls run live. Failed calls always re-run.
- Workers run with their harness permissions bypassed (no sandboxes, no
  approval prompts) so they can edit files and act autonomously — scope
  prompts accordingly.

## Rules of thumb

1. Scout inline first (list files, scope the diff), then orchestrate over the
   discovered work-list.
2. Scale to the ask: "find bugs" → few finders + single-vote verify;
   "thoroughly audit" → large finder pool + 3–5 vote adversarial pass + synthesis.
3. If you bound coverage (top-N, sampling), `log()` what was dropped.
4. Workers are stateless one-shots — pack all needed context into the prompt;
   they cannot see the conversation, each other, or previous turns.
5. Workers run as real agent CLIs in `cwd` — they can read and edit files
   there. For parallel file mutation, give each worker a disjoint scope.
