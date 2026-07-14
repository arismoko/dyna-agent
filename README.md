# dyna

Dynamic multi-agent workflows for any coding agent.

`dyna` is a standalone, harness-agnostic port of Claude Code's dynamic
workflow system. Any coding agent (claude-code, codex CLI, opencode, pi) can
write plain JavaScript workflow scripts that orchestrate fleets of model
workers deterministically, while humans configure the fleet and watch runs
live in a terminal dashboard.

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

![dyna TUI: a live adversarial-review run with agents fanning out](docs/img/tui-workflows.png)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/arismoko/dyna-agent/main/install.sh | bash
```

The installer downloads a release binary, falls back to building from source,
and installs the agent skill into every detected harness. From a checkout,
`./install.sh` does the same (builds locally into `~/.local/bin`). Overrides:
`DYNA_INSTALL_DIR`, `DYNA_REPO`, `DYNA_VERSION`, `DYNA_NO_SKILLS=1`.
Release downloads are verified against the published `checksums.txt` before
the existing binary is replaced.

Then:

```bash
dyna profiles init        # register the curated default worker fleet
dyna demo                 # register mock workers and run a sample workflow
dyna tui                  # open the dashboard
```

## Updating

Release builds can update themselves from the latest stable GitHub release:

```bash
dyna --version
dyna update --check       # report only
dyna update               # verify and install now
```

`dyna tui` performs the same update automatically at most once every 24 hours.
The check is best effort: an offline GitHub API or a read-only install
directory never prevents the dashboard from opening. Set
`DYNA_NO_AUTO_UPDATE=1` to disable automatic checks. Development builds are
not replaced automatically; `dyna update --force` is the explicit escape
hatch when replacing one is intentional.

Updates are downloaded beside the installed executable, verified with the
release's SHA-256 checksum, smoke-tested with `--version`, and moved into
place atomically. Already-running workflows keep their current executable
inode and are never restarted or killed; the new version is used by future
invocations. A successful update also refreshes the embedded dyna skill in
detected agent harnesses. On an interactive terminal, the update then offers
to replace colliding bundled profiles, keep them managed by future releases,
and install root-agent guidance. That consent is durable across releases:
later updates refresh only profiles that remain marked managed and refresh
previously accepted guidance without asking the three questions again or
replaying the one-time replacement choice. Non-interactive and worker-facing
paths never prompt.

## Teaching your agents about dyna

`dyna skill install` writes an agent skill (a SKILL.md with name/description
frontmatter) into every detected harness, so agents know dyna exists and to
read `dyna guide`:

- **claude-code**: `~/.claude/skills/dyna/SKILL.md`
- **codex**: `~/.codex/skills/dyna/SKILL.md`
- **opencode**: `~/.config/opencode/skills/dyna/SKILL.md`
- **pi**: `~/.pi/agent/skills/dyna/SKILL.md`

The Pi skill uses Pi's supported `disable-model-invocation: true` frontmatter,
so it stays available to a person as `/skill:dyna` in plain Pi without appearing
in model discovery. `dyna pi` supplies its own self-contained prompt and tools.

`dyna skill install <harness>` forces one, `--all` forces all,
`dyna skill uninstall` removes cleanly, and `dyna skill show` prints the
content. Older versions wrote managed AGENTS.md blocks; install and uninstall
migrate those away automatically.

`dyna skill guidance install` optionally adds a short, separately managed
root-agent block to each detected non-Pi harness's shared `CLAUDE.md` or
`AGENTS.md`.
It explains when multi-model fan-out is worth its cost and when native
subagents are the better fit. The command is idempotent, accepts the same
harness names and `--all` flag as skill installation. Pi guidance is
explicit-only via `dyna skill guidance install pi`; automatic setup removes
older managed Pi blocks from both `~/.pi/agent/AGENTS.md` and the legacy
`~/.pi/AGENTS.md` while preserving user content. The
`dyna skill guidance uninstall` removes only its marker block. Uninstalling
the dyna skill also removes this guidance.

## Worker profiles

You register the models agents are allowed to use, each with a description
and three standardized stats, all 1-10 where higher is better (for cost,
higher means cheaper):

- **taste**: code quality, judgment, design sense, review ability
- **intelligence**: raw capability on hard, long, complex tasks
- **cost**: cost efficiency (10 = very cheap to run, 1 = very expensive)

**Quick start:** `dyna profiles init` registers a curated default fleet:
`fable` (Claude Fable 5 via claude-code, for verification, judging,
high-stakes review, and frontend work), `sol` and `sol-max` (gpt-5.6-sol via
codex at high and xhigh effort, for hard implementation and debugging), `terra`
(the balanced default generalist), and `luna` (cheap, fast sweeps). Each
description tells agents when to pick that worker, when to avoid it, and how
it tends to fail. These profiles are managed, so installed entries follow
bundled settings in later dyna builds while their default and enabled state
remain yours. Editing one in the TUI or overwriting it with `profiles add`
opts it out unless you explicitly keep `managed` enabled; deleting one does
not recreate it. Existing unmanaged profiles are never overwritten unless
you pass `--force`. The deep-work `fable` and `sol` profiles may use their
harnesses' native subagents, while the `luna`, `terra`, and `sol-max`
fan-out/panel profiles block nested delegation.

Or build profiles interactively with the TUI's profile wizard (`w` on the
Profiles tab), six multiple-choice steps:

1. **Harness**: which CLI runs the worker
2. **Model**: fetched from the harness itself (`codex debug models`,
   `claude --help` model aliases, `opencode models`), or type it yourself
3. **Reasoning effort**: the levels that model actually supports, translated
   to the right flags and env vars on save
4. **Stats**: taste, intelligence, cost efficiency
5. **Description**: the blurb agents read when picking workers
6. **Finish**: name (auto-suggested), limits, subagent policy, enabled,
   default, save

Or register by hand:

```bash
dyna profiles add --name sol --harness codex --model gpt-5.6-sol \
  --extra-arg '-c' --extra-arg 'model_reasoning_effort=high' \
  --taste 7 --intelligence 9 --cost 6 \
  --desc "Workhorse for hard implementation and debugging. Works boldly; pin the scope on legacy code."

dyna profiles add --name fable --harness claude-code --model fable \
  --taste 10 --intelligence 10 --cost 2 \
  --desc "Premium reviewer and judge. Excellent taste; best for verification and high-stakes decisions."

dyna profiles add --name luna --harness codex --model gpt-5.6-luna \
  --taste 5 --intelligence 7 --cost 10 \
  --desc "Cheap and fast. Ideal for wide fan-outs, sweeps, and first-pass triage."
```

Profiles can be toggled on and off without losing anything:
`dyna profiles disable <name>` / `enable <name>` (TUI: `t`). A disabled
profile keeps its stats and description and stays editable, but disappears
from the agents' view, and any `agent()` call to it fails.

Add `--disable-subagents` (or choose **block** for subagents in the TUI) when
the selected worker must complete each task itself instead of spawning or
delegating to child agents. This does not stop Dyna from launching that
worker. Claude Code and Codex profiles receive their verified native
delegation controls, and every harness receives the same final worker-prompt
restriction. This is a strong policy guard, not a security boundary: workers
with shell access can still launch another CLI if they deliberately disobey.

Supported harnesses: `claude-code`, `codex`, `opencode`, `pi`, `custom` (any
argv with `{{prompt}}`/`{{model}}` placeholders; the prompt goes to stdin if
no placeholder is given), and `mock` for demos and tests. Workers run
headless (`claude -p`, `codex exec`, `opencode run`) in the workflow's
working directory, so they can read and edit files.

**Permissions are bypassed by default** unless a profile supplies an explicit
permission or sandbox mode: claude-code workers get
`--dangerously-skip-permissions` and codex workers get
`--dangerously-bypass-approvals-and-sandbox`, because a headless worker that
stops to ask for permission hangs forever. Register a profile with
`--safe-mode` to keep the harness's own guardrails, and add other
harness-specific flags with `--extra-arg`.

If a built-in harness exits unsuccessfully or loses its final output after
establishing a session, dyna waits briefly and nudges that exact session once
to finish the original task. The retry stays inside the original timeout and
logical `agent()` call. Cancellation is never retried, and profiles with
explicit persistence, session, budget, or resume-incompatible flags (and
custom harnesses) stay single-shot so recovery cannot silently change those
controls.

Every `agent()` call defaults to 5 hours. Explicit script and profile timeout
values have a 30-minute minimum; shorter values are clamped and larger values
are preserved. Parent workflow cancellation can still stop a worker earlier.

Agents discover the fleet with `dyna profiles list --json` and pick workers
by description and stats, or dynamically inside scripts via the `profiles`
global.

Profiles can also be limited so agents can't lean on an expensive model too
hard: `--limit-concurrent N` caps simultaneous workers of that profile
(excess calls queue), and `--limit-calls N` hard-caps total calls per run.
The first call past a call cap aborts the whole workflow with a clear error
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
dyna run review.js --args '{"target":"src/"}'   # progress on stderr, result JSON on stdout
dyna run audit.js --detach                      # background; prints the run id
dyna journal "mapped the parser boundary" --kind finding --next "check callers"
dyna runs wait <id>                             # block until done, print the result
dyna run review.js --resume <id>                # replay unchanged agent calls from a prior run
dyna runs list                                  # inspect history
dyna runs steer <id> <agent-id> "check the parser first" # steer the same live worker session
dyna runs cancel <id>                           # stop a running workflow
dyna runs pause <id> / unpause <id>             # hold new worker launches / resume
dyna runs remove <id>... / clear                # delete finished runs
```

Cancel, pause, and delete are also available in the TUI (`x`, `p`, `d` on the
Workflows tab). In a run's agent inspector, select a running worker and press
`s` to send a short steering message. Dyna interrupts the current harness turn
and continues its exact resumable session; profiles without that safe session
contract reject steering instead of launching a replacement.

Per-agent `isolation: 'worktree'` runs a worker in a detached git worktree,
removed automatically if untouched and kept (with its path logged) if the
worker changed files.

Every run persists under `~/.local/share/dyna/runs/<run-id>/`:

```text
script.js                        # captured workflow
meta.json                        # run status and timestamps
events.jsonl                     # live run/agent event stream
journal.jsonl                    # completed-call/resume ledger
agents/<agent-id>/journal.jsonl  # that worker's live progress journal
result.json                      # final workflow value
```

The root `journal.jsonl` holds the completed-call records (including prompts
and results) used by workflow resume; it is not the live progress journal.
Every live worker, including a read-only explorer, gets its own run-owned
`agents/<agent-id>/journal.jsonl`.

## Agent journals

Dyna prepends journal instructions to every worker prompt. During a run the
worker records concise progress notes with:

```bash
dyna journal "confirmed the decoder rejects truncated input" \
  --kind verification --next "report the evidence"
```

That prompt also tells the process unambiguously that it is inside a dyna
workflow. A worker may use `dyna journal` and no other dyna command: it must
not load the dyna skill, start another workflow, or orchestrate more dyna
workers. When its profile permits delegation, it uses only the current
harness's built-in subagents.

`--kind` is one of `update`, `finding`, `decision`, `verification`, or
`blocker`; `--next` is optional. Dyna supplies the timestamp and agent
identity and serializes appends so concurrent writes cannot corrupt a line.
An agent-authored line is a complete JSON object followed by a newline:

```json
{"ts":1783706645123,"kind":"finding","message":"Located the retry boundary.","next":"Inspect cancellation behavior.","source":"agent"}
```

`ts` is Unix time in milliseconds. `message` is a non-empty progress note and
`next` is optional. Dyna may also append system start, nudge, and completion
records with extra metadata. Each physical line is independently valid JSON;
the file is JSONL, not a single JSON array.

Orchestrators should reinforce the cadence in worker prompts: write once
after orientation, at meaningful discoveries, decisions, verifications, and
blockers, before a long operation, and before finishing. Entries should say
what changed and what comes next, in one or two sentences plus an optional
next step. They are not chain-of-thought, scratchpads, command transcripts,
or a substitute for the final answer.

This applies to read-only exploration too: the worker treats its run-owned
journal as its only allowed write. When a codex profile explicitly selects a
read-only sandbox or a built-in read-only permission profile, dyna preserves
that boundary and layers a narrow permission profile that makes only the
agent's journal directory writable. It does not turn the target workspace
writable or silently disable the read-only sandbox. Other provider-managed or
custom read-only modes are never auto-bypassed; if they cannot expose a
narrow journal exception, dyna records the missing entry instead of widening
access.

After five minutes without a valid agent-authored entry, dyna marks the
worker quiet. For resumable built-in harnesses it gracefully interrupts,
allows the CLI to flush its session, and continues the exact same session
with a reminder to write now and keep working; it never launches a fresh
worker. Explicitly non-resumable sessions and custom harnesses can be shown
as quiet, but cannot be safely live-resumed.

A resumable worker that finishes quickly without writing anything gets one
bounded, immediate reminder in that same session to add a brief entry; its
already-successful task result stays authoritative. If a custom or explicitly
non-resumable worker finishes without an entry, dyna records that the
reminder was unavailable rather than risking a duplicate fresh invocation.

The journal is a progress side channel. A final response still has to satisfy
the `schema` passed to `agent()` (when present); journal JSONL never counts
as or replaces that result.

## The TUI

`dyna tui` has three views; switch with `tab` or `1`/`2`/`3`:

- **Workflows**: every run, with active agents visible. Inspect a run for a
  journal-first live timeline and per-agent **Journal**, **Task**, and
  **Result** modes. Agent rows show lifecycle status, journal-entry count,
  relative freshness, and whether a quiet-worker nudge was sent. Press
  `s` on a running worker to steer it, `enter` or `right` to focus,
  `left`/`right` to change mode, `j`/`k` or
  arrows to scroll, `g`/`G` for top/bottom, `f` to follow new entries, and
  `esc` to return to the agent list. The selected run's events, completion
  ledger, and selected agent journal are tailed independently about every
  400 ms, so entries appear while the worker is still running, not only when
  it finishes.

  `dyna pi` launches the built-in root agent preset `dyna-orchestrator`, names
  new sessions after that preset unless `--name`/`-n` is supplied, and keeps an
  `agent:dyna-orchestrator` footer status visible without a startup message. It
  uses Dyna workflows by default for code changes, reserving direct work for
  clearly small, straightforward changes that are easy to verify. By default it
  activates every tool registered when Pi starts, including Pi's
  normally opt-in built-ins, the native Dyna tools, and tools from other loaded
  extensions. Explicit `--tools`/`-t`, `--exclude-tools`/`-xt`, `--no-tools`/`-nt`,
  and `--no-builtin-tools`/`-nbt` selections remain authoritative.

  The preset uses Pi's existing `openai-codex/gpt-5.6-terra` model at `xhigh`,
  while explicit provider, model, and thinking flags still win. It reuses
  Codex's ChatGPT OAuth in memory and
  delegates refresh to Codex's app server, so no second Pi login or credential
  copy is required. Unsupported or missing Codex auth fails with a `codex login`
  instruction rather than falling back to another provider. Other Pi skills
  remain enabled; an installed Dyna Pi skill is hidden from model discovery by
  its frontmatter because the launcher supplies the Dyna contract directly.

  The extension registers model-visible `dyna_profiles`, `dyna_run`,
  `dyna_runs`, and `dyna_steer` tools. The orchestrator writes each bounded
  workflow to a unique `/tmp/dyna-workflow-*.js` file and passes that path to
  `dyna_run`, which always starts it detached and promptly returns its run ID.
  The tools manage only runs owned by the persisted Pi session and steer active
  workers without shell command assembly. Type `/dyna` to suspend Pi and open
  the built-in `dyna tui` dashboard filtered to that persisted Pi session, so
  runs from other sessions cannot be viewed or acted upon there; exiting the
  dashboard restores and redraws Pi. A direct `dyna tui` remains global.

  Pi 0.80.7 already reports `openai-codex/gpt-5.6-terra` with its correct 372K
  context window, so Dyna leaves that model metadata untouched. Pi's public
  extension API exposes context usage and manual compaction, but no
  session-local compaction-threshold override, so Dyna does not mutate global
  settings or replace the provider/model to emulate Codex's 95% threshold.

  ![Run inspector: per-agent journal timeline while workers run](docs/img/tui-journal.png)
- **Profiles**: the fleet at a glance, with descriptions and
  taste/intelligence/cost stat bars. `a` add, `e` edit, `d` delete, `s` set
  default, `t` toggle enabled, `w` wizard.

  ![Profiles tab: the worker fleet with stats and descriptions](docs/img/tui-profiles.png)
- **Guide**: the scripting guide, rendered.

## Script example

```js
export const meta = { name: 'fix-and-check', phases: [{title:'Fix'},{title:'Check'}] }

phase('Fix')
const fix = await agent(`Fix the failing test in ${args.pkg}. Edit files as needed.`,
  { profile: 'sol', label: 'fix' })

phase('Check')
const verdict = await agent(
  `Review the working-tree diff. Was the fix correct and minimal? ${fix}`,
  { profile: 'fable', label: 'review', schema: { type:'object', required:['ok'],
    properties: { ok: {type:'boolean'}, notes: {type:'string'} } } })

return { verdict }
```

See `examples/` for adversarial-review and judge-panel workflows, and
`dyna guide` for the full API.

## Releasing

The GitHub release tag is the canonical version. Create an annotated semantic
version tag on `main` and push it:

```bash
git switch main
git pull --ff-only
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The release workflow verifies the tag is on `main`, runs the race-enabled
test suite, vet, and installer checks, then builds static Linux and macOS
binaries for amd64 and arm64. It stamps `dyna --version`, publishes the four
assets plus `checksums.txt`, and generates GitHub release notes. A tag with a
prerelease suffix such as `v0.2.0-rc.1` is published as a prerelease and is
never offered by the stable updater.

## Layout

- `main.go`: CLI (cobra): `profiles`, `run`, `runs`, `journal`, `guide`,
  `tui`, `demo`, `skill`, `update`, `version`
- `internal/engine`: embedded JS runtime (goja plus an event loop),
  concurrency semaphore, JSON-schema-validated structured output with
  auto-retry
- `internal/harness`: headless CLI adapters with bounded, exact-session
  recovery for transient harness failures
- `internal/profile`: profile registry (`~/.config/dyna/profiles.json`)
- `internal/runstore`: run persistence, event stream, completed-call ledger,
  and per-agent journals
- `internal/tui`: Bubble Tea dashboard
- `internal/update`: cached GitHub release lookup, checksum verification, and
  atomic executable replacement
- `guide/GUIDE.md`: the agent-facing scripting guide (embedded in the binary)

## License

MIT
