# dyna — dynamic multi-agent workflows for any coding agent

`dyna` is a standalone, harness-agnostic port of Claude Code's dynamic
workflow system. Any coding agent — codex CLI, claude-code, opencode, pi —
can write plain-JavaScript workflow scripts that orchestrate fleets of model
workers deterministically, while humans configure the fleet and watch runs
live in a beautiful TUI.

```
┌─────────────┐   dyna run script.js    ┌──────────────────────────────┐
│ your agent  │ ──────────────────────▶ │ dyna engine (embedded JS)    │
│ (any CLI)   │ ◀── result JSON ─────── │ agent()/parallel()/pipeline()│
└─────────────┘                         └──────┬───────────────────────┘
                                               │ fans out to worker profiles
                              ┌────────────────┼────────────────┐
                              ▼                ▼                ▼
                        claude -p …      codex exec …     opencode run …
```

## Install

One-liner (once the repo is public — downloads a release binary, falls back to
building from source, and installs agent skills into detected harnesses):

```bash
curl -fsSL https://raw.githubusercontent.com/Aria-Figueredo/dyna-agent/main/install.sh | bash
```

From a checkout, `./install.sh` does the same (builds locally into
`~/.local/bin`). Overrides: `DYNA_INSTALL_DIR`, `DYNA_REPO`, `DYNA_VERSION`,
`DYNA_NO_SKILLS=1`.

```bash
dyna demo                 # registers mock workers + runs a sample workflow
dyna tui                  # open the dashboard
```

## Teaching your agents about dyna

`dyna skill install` plants a proper agent skill (SKILL.md with
name/description frontmatter) into every detected harness so agents know dyna
exists and to read `dyna guide`:

- **claude-code** → `~/.claude/skills/dyna/SKILL.md`
- **codex** → `~/.codex/skills/dyna/SKILL.md`
- **opencode** → `~/.config/opencode/skills/dyna/SKILL.md`
- **pi** → `~/.pi/agent/skills/dyna/SKILL.md`

`dyna skill install <harness>` forces one, `--all` forces all,
`dyna skill uninstall` removes cleanly, `dyna skill show` prints the content.
(Older versions wrote managed AGENTS.md blocks; install/uninstall migrate
those away automatically.)

## Worker profiles

You register the models agents are allowed to use, each with a description and
three standardized stats (**1–10, higher is better** — for cost, higher means
*cheaper*). The fastest way is the TUI's **profile wizard** (`w` on the
Profiles tab): it probes your installed agent CLIs for their models (claude
model aliases, codex config + known ids, `opencode models`), lets you pick
which to register — plus a "write your own" entry for niche setups — and
steps you through a prefilled form for each (name, description, stats).

Or register by hand:

```bash
dyna profiles add --name gpt-5.5-xhigh --harness codex --model gpt-5.5 \
  --extra-arg '-c' --extra-arg 'model_reasoning_effort=xhigh' \
  --taste 4 --intelligence 10 --cost 6 --default \
  --desc "Workhorse. Operates alone on long tasks, writes good but unpolished code. Weak frontend design taste."

dyna profiles add --name opus-4.8 --harness claude-code --model opus \
  --taste 10 --intelligence 8 --cost 4 \
  --desc "Excellent taste; reviews code and finds issues extremely well; excels at frontend. Not the best at long complex grinds. High cost."

dyna profiles add --name glm-5.2 --harness opencode --model zai/glm-5.2 \
  --taste 8 --intelligence 6 --cost 10 \
  --desc "Low cost, fast, great taste; intelligence a notch below opus/gpt. Ideal for wide fan-outs and first-pass triage."
```

Profiles can be **toggled on/off** without losing anything: `dyna profiles
disable <name>` / `enable <name>` (TUI: `t`). A disabled profile keeps its
stats and description and stays editable, but disappears from the agents'
view and any `agent()` call to it fails.

Harnesses: `claude-code`, `codex`, `opencode`, `pi`, `custom` (any argv with
`{{prompt}}`/`{{model}}` placeholders, prompt on stdin if no placeholder), and
`mock` for demos/tests. Workers run headless (`claude -p`, `codex exec`,
`opencode run`) in the workflow's working directory, so they can read and
edit files. **Permissions are bypassed by default** — claude-code workers get
`--dangerously-skip-permissions`, codex workers get
`--dangerously-bypass-approvals-and-sandbox` (no sandbox) — because headless
workers that stop to ask for permission hang forever. Register a profile with
`--safe-mode` to keep the harness's own guardrails. Add other
harness-specific flags with `--extra-arg`.

Agents discover the fleet with `dyna profiles list --json` and pick workers by
description and stats — or dynamically inside scripts via the `profiles`
global.

Profiles can also be **limited** so agents can't lean on an expensive model
too hard: `--limit-concurrent N` caps simultaneous workers of that profile
(excess calls queue), and `--limit-calls N` hard-caps total calls per run —
the first call past the cap **aborts the whole workflow** with a clear error,
rather than continuing toward a silently degraded (but still billed) result.
Limits show up in `profiles list` and in the scripts' `profiles` global, so
agents can size fan-outs around them up front.

## For agents

Point your agent at the guide; it contains the full script API and the
orchestration patterns (adversarial verify, judge panel, loop-until-dry,
cheap-first triage):

```bash
dyna guide --plain
```

Then:

```bash
dyna run review.js --args '{"target":"src/"}'   # progress → stderr, result JSON → stdout
dyna run audit.js --detach                       # background; prints run id
dyna runs wait <id>                              # block until done, print result
dyna run review.js --resume <id>                 # replay unchanged agent calls from a prior run
dyna runs list                                   # inspect history
dyna runs cancel <id>                            # stop a running workflow
dyna runs pause <id> / unpause <id>              # hold new worker launches / resume
dyna runs remove <id>... / clear                 # delete finished runs
```

Cancel, pause, and delete are also available in the TUI (`x`, `p`, `d` on the
Workflows tab).

Per-agent `isolation: 'worktree'` runs a worker in a detached git worktree —
removed automatically if untouched, kept (and its path logged) if the worker
changed files.

Every run persists under `~/.local/share/dyna/runs/<id>/`: the script,
`events.jsonl` (live-tailed by the TUI), `journal.jsonl` (full per-agent
prompts and results), and `result.json`.

## The TUI

`dyna tui` — three views, switch with `tab` or `1`/`2`/`3`:

- **Workflows** — every run with a live progress tree: phases, per-agent
  spinner/status, durations, result previews, the `log()` narration, and the
  final result. Updates in real time while runs execute in other terminals.
- **Profiles** — the fleet at a glance: description plus taste/intelligence/
  cost-efficiency stat bars. `a` add, `e` edit, `d` delete, `s` set default.
- **Guide** — the scripting guide, rendered.

## Script example

```js
export const meta = { name: 'fix-and-check', phases: [{title:'Fix'},{title:'Check'}] }

phase('Fix')
const fix = await agent(`Fix the failing test in ${args.pkg}. Edit files as needed.`,
  { profile: 'gpt-5.5-xhigh', label: 'fix' })

phase('Check')
const verdict = await agent(
  `Review the working-tree diff. Was the fix correct and minimal? ${fix}`,
  { profile: 'opus-4.8', label: 'review', schema: { type:'object', required:['ok'],
    properties: { ok: {type:'boolean'}, notes: {type:'string'} } } })

return { verdict }
```

See `examples/` for adversarial review and judge-panel workflows, and
`dyna guide` for the full API.

## Layout

- `main.go` — CLI (cobra): `profiles`, `run`, `runs`, `guide`, `tui`, `demo`
- `internal/engine` — embedded JS runtime (goja + event loop), concurrency
  semaphore, JSON-schema-validated structured output with auto-retry
- `internal/harness` — adapters that turn one `agent()` call into one
  headless CLI invocation
- `internal/profile` — profile registry (`~/.config/dyna/profiles.json`)
- `internal/runstore` — run persistence and event journal
- `internal/tui` — Bubble Tea dashboard
- `guide/GUIDE.md` — the agent-facing scripting guide (embedded)
