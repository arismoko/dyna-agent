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
  name: 'audit-target',
  description: 'Cheap sweep for hotspots, deep review, adversarial verification',
  phases: [{ title: 'Sweep' }, { title: 'Review' }, { title: 'Verify' }],
}

// Route on stats: cheapest worker sweeps wide, smartest reviews deep,
// highest taste judges the findings.
const by = k => [...profiles].sort((a, b) => b[k] - a[k])[0].name
const sweeper = by('cost'), reviewer = by('intelligence'), judge = by('taste')

const HOTSPOTS = { type: 'object', required: ['files'], properties: {
  files: { type: 'array', items: { type: 'string' } } } }
const FINDINGS = {
  type: 'object', required: ['findings'],
  properties: { findings: { type: 'array', items: {
    type: 'object', required: ['title'],
    properties: { title: {type:'string'}, detail: {type:'string'} },
  }}},
}
const VERDICT = { type: 'object', required: ['isReal'], properties: {
  isReal: {type:'boolean'}, reason: {type:'string'} }}

phase('Sweep')
const { files } = await agent(
  `List the files under ${args.target} most likely to hide correctness or
   security bugs (complex state, auth, concurrency). Return at most 8 paths.`,
  { profile: sweeper, label: 'sweep', schema: HOTSPOTS })
log(`sweep found ${files.length} hotspot files`)

// pipeline = no barrier: each file's Verify starts the moment its own
// Review finishes, while other files are still being reviewed.
const results = await pipeline(
  files,
  f => agent(`Review ${f} for correctness and security bugs. Cite line numbers.`,
    { profile: reviewer, label: `review:${f}`, phase: 'Review', schema: FINDINGS }),
  (review, f) => parallel(review.findings.map(x => () =>
    agent(`Adversarially verify — try to REFUTE: "${x.title}" in ${f}. ${x.detail || ''}`,
      { profile: judge, label: `verify:${f}`, phase: 'Verify', schema: VERDICT })
      .then(v => ({ ...x, file: f, verdict: v })))),
)
return { confirmed: results.filter(Boolean).flat().filter(Boolean)
  .filter(x => x.verdict?.isReal) }
```

The script's `return` value is printed as JSON on stdout when the run ends.

## Choosing workers: route on the stats

Users register profiles (`dyna profiles add`, `dyna profiles init`, or the
TUI's profile wizard). Every profile carries three standardized stats, all
**1-10, higher is better**, and the stats are your primary routing signal:

- **taste**: code quality, judgment, design/frontend sense, review ability
- **intelligence**: raw capability on hard, long, complex tasks
- **cost**: cost efficiency (10 = very cheap to run, 1 = very expensive)

Each stage of a workflow stresses exactly one stat, so route stage by stage:

- **Sweep with cost. Grind with intelligence. Judge with taste.**
- High **cost** (cheap): wide fan-outs, scouting, dedup, first-pass triage,
  mechanical bulk edits — anywhere volume matters more than depth.
- High **intelligence**: hard debugging, long implementation grinds,
  root-cause analysis, gnarly correctness work.
- High **taste**: review, verification votes, judge panels, synthesis,
  frontend/design work — anywhere a verdict or polish is the product.

Pick the stage's worker from the stats first; then read the description as
color — it names failure modes and quirks the numbers can't (e.g. "correct
but unpolished code, weak frontend taste"), and should only confirm or veto
the stat-based pick, not replace it. A common shape: the cheapest profile
gathers, the smartest digs, the most tasteful decides.

Disabled profiles never appear in your `profiles` global and calling them
fails. Only orchestrate with what you can see.

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

A profile registered with `dyna profiles add --disable-subagents` requires
that selected worker to complete tasks itself without spawning or delegating
to child agents. It does not prevent Dyna's own `agent()` call from launching
the worker. Claude Code and Codex use verified native delegation controls,
and every harness receives a final worker-prompt restriction. This is a
strong policy guard, not a security boundary for workers that can invoke
other CLIs through a shell.

## Script API

Scripts are plain JavaScript (NOT TypeScript) running in an async context;
use `await` at top level. `export const meta = { name, description, phases }`
at the top is encouraged; `meta.name` labels the run.

- `agent(prompt, opts?) -> Promise<string|object>`: run one worker turn.
  opts: `profile` (registered profile name), `label` (display name),
  `phase` (progress group), `schema` (JSON Schema; output is parsed,
  validated, retried up to 2 times, and returned as an object),
  `cwd` (working directory for the worker), `timeout` (seconds; defaults to
  5 hours, with a 30-minute minimum for explicit values),
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
- `profiles`: registered worker profiles: `{name, description, harness, model, taste, intelligence, cost, default, disableSubagents}[]`.

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
  const votes = await parallel([1, 2, 3].map(() => () =>
    agent(`Try to refute: ${claim}. Default refuted=true if uncertain.`,
      { profile: judge, schema: VERDICT })))  // judge = highest-taste profile
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
dyna runs list [--json] [--session <id>] # past/active runs, optionally by parent session
dyna runs show <id> [--json]       # events, result
dyna runs wait <id> [--timeout N]  # block until a run finishes, print result
dyna runs steer <id> <agent-id> "message" # steer the exact active worker session
dyna runs cancel <id>              # stop a running workflow (kills workers)
dyna runs pause <id> / unpause <id> # hold new worker launches / resume
dyna runs remove <id>... | clear   # delete finished runs
dyna journal "message" --kind update|finding|decision|verification|blocker [--next "..."]
dyna guide                         # this document
dyna tui                           # human dashboard (profiles + live runs)
dyna pi [pi args...]               # interactive pi harness with session-scoped /dyna
```

`dyna run` streams progress to stderr and prints the workflow's JSON result to
stdout, so you can pipe it. Runs persist under the data dir. The root
`journal.jsonl` remains the completed-call/resume ledger (including worker
prompts and results), while each live worker writes progress to
`agents/<agent-id>/journal.jsonl`; the final workflow value is in
`result.json`.

`dyna pi` launches Pi with direct Dyna instructions and the bundled extension,
defaulting the root orchestrator to Pi's built-in
`openai-codex/gpt-5.6-terra` model at `xhigh` reasoning. Explicit Pi
`--provider`, `--model`, or `--thinking` flags take precedence. The extension
asks Codex's app server to own OAuth refresh and installs only the current
access token in Pi's in-memory runtime auth; it does not copy credentials into
Pi's config. A missing login or unsupported Codex credential store stops the
prompt with a `codex login` error instead of selecting another provider. The
launcher passes `--no-skills`, so the harness has no runtime skill dependency;
explicit Pi arguments are appended unchanged.

Every workflow started from that invocation is tagged with one session id; use
`/dyna` inside Pi to watch only those runs, or `dyna runs list --session <id>`
to apply the same filter from a shell. Its model-visible `dyna_steer` tool can
send a short instruction to a running worker from that session. In `dyna tui`,
select a running worker in the agent inspector and press `s` for the same
capability. Steering gracefully interrupts the current harness turn and
continues the exact resumable session; unsupported profiles reject the request
and never launch a replacement.

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
