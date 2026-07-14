package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// The agent-facing skill. Kept short on purpose: `dyna guide` is the real
// documentation; this just teaches harnesses that dyna exists and when to
// reach for it.
const skillBody = `# dyna: dynamic multi-agent workflows

You have access to the ` + "`dyna`" + ` CLI: write a JavaScript workflow script that
orchestrates registered model workers, then run it. Use it when a task
benefits from fanning work out to multiple models (parallel code review,
wide sweeps/audits, adversarial verification, judge panels, migrations) or
when the user asks for a workflow.

If your instructions include a run-owned dyna journal, you are a dyna worker:
do not use this skill; the only permitted dyna command is ` + "`dyna journal`" + `.

**Read ` + "`dyna guide`" + ` first**: it is the full scripting guide (API + orchestration
patterns). Quick reference:

1. ` + "`dyna profiles list --json`" + `: the workers you may use. Each has a
   description and stats (taste, intelligence, cost: 1-10, higher is better;
   cost = cost-efficiency, 10 = very cheap). Match workers to stages: high
   taste for review/judging/frontend; high intelligence for long hard tasks;
   high cost stat (cheap) for wide fan-outs and first-pass triage.
2. Write the script: ` + "`agent(prompt, {profile, label, phase, schema})`" + `,
   ` + "`parallel(thunks)`" + `, ` + "`pipeline(items, ...stages)`" + `, ` + "`phase(title)`" + `, ` + "`log(msg)`" + `,
   plus ` + "`args`" + ` and ` + "`profiles`" + ` globals. Plain JS, top-level await, return value
   becomes the result. ` + "`schema`" + ` gives validated JSON output back. Every call defaults to
   5 hours; explicit script and profile timeout values have a 30-minute minimum; shorter values are clamped.
3. ` + "`dyna run script.js --args '{...}'`" + `: progress streams to stderr, the
   result JSON prints to stdout. Add ` + "`--detach`" + ` to run in the background
   (prints run id; collect later with ` + "`dyna runs wait <id>`" + `), and
   ` + "`--resume <run-id>`" + ` to replay unchanged agent calls from a previous run
   after a failure or script edit.
4. ` + "`dyna runs list`" + ` / ` + "`dyna runs show <id> --json`" + ` to inspect past runs.
   The user watches live with ` + "`dyna tui`" + `.

Every worker gets ` + "`runs/<run-id>/agents/<agent-id>/journal.jsonl`" + `; the root
` + "`journal.jsonl`" + ` remains the completed-call/resume ledger. Dyna prepends the
base journal instructions. Reinforce them in every ` + "`agent()`" + ` prompt: use
` + "`dyna journal \"message\" --kind update|finding|decision|verification|blocker --next \"...\"`" + `
once after orientation, at meaningful discoveries/decisions/verification/
blockers, before a long operation, and before finishing. Notes are concise
and brief (one or two sentences plus an optional next step), not chain-of-thought
or a running transcript. This includes read-only exploration: the worker treats
the run-owned journal as its only allowed write. An explicitly read-only Codex
profile stays read-only; dyna grants write access only to its agent journal directory,
not the target workspace. Other explicit read-only modes are not auto-bypassed;
if they cannot allow the journal narrowly, dyna records the missing entry.
After five minutes without a valid agent-authored entry,
dyna gracefully interrupts and continues the exact same
resumable built-in session with a write-now-and-continue reminder; it never
starts a fresh worker for a journal nudge. A fast resumable worker that finishes
without an entry gets one bounded, immediate reminder in that same session, while its
original result is preserved. Non-resumable/custom sessions are only marked
quiet or missing-entry. Journal entries are a progress side channel,
not the worker's final response or schema output. The user can watch them appear live
in the TUI while the worker is still running.

Unless a profile opts into safe/read-only behavior, workers run with harness
permissions bypassed so headless approval prompts cannot hang them. For
parallel file mutation, pass ` + "`isolation: 'worktree'`" + ` per agent.
`

const skillFrontmatter = `---
name: dyna
description: Orchestrate multi-model workflows with the dyna CLI. Fan work out to registered worker models (parallel review, sweeps, judge panels, adversarial verification). Use when a task would benefit from multiple models working together or the user mentions dyna, workflows, or worker profiles.
---

`

const (
	markBegin         = "<!-- dyna:skill:begin (managed by `dyna skill install`; do not edit inside) -->"
	markEnd           = "<!-- dyna:skill:end -->"
	guidanceMarkBegin = "<!-- dyna:guidance:begin (managed by `dyna skill guidance install`; do not edit inside) -->"
	guidanceMarkEnd   = "<!-- dyna:guidance:end -->"
)

const guidanceBody = `# Multi-model workflows with dyna

You can orchestrate fleets of worker models with the dyna CLI (see the dyna skill).

- Reach for dyna when the user explicitly asks (mentions dyna, workflows, worker profiles, or multi-model orchestration) or when a task genuinely benefits from fanning out across models: parallel code review, wide audits or sweeps, adversarial verification, judge panels, or large migrations. Workflows can spawn many worker sessions and consume significant tokens, so the user should be requesting that scale rather than having it inferred from an ordinary task.
- Prefer a hybrid approach: scout inline first (list files, scope the diff, find the work list), then orchestrate over what you found.
- Scale to the ask: "find bugs" means a few finders and a single verification vote; "thoroughly audit" means a large finder pool, an adversarial multi-vote pass, and a synthesis stage.
- For ordinary context-local delegation (a focused search or a scoped edit), use your harness's built-in subagents. Dyna is for cases where different models or deterministic fan-out add real value.
- EXCEPTION: if you are yourself a dyna worker (your instructions mention the run-owned dyna journal), none of this applies. Never invoke dyna or its skill; use only ` + "`dyna journal`" + `.
`

// Every supported harness loads Claude-style skills (a directory holding a
// SKILL.md with name/description frontmatter), so installation is always a
// standalone skill dir. legacyAgentsMD points at the AGENTS.md an older dyna
// version wrote a managed block into; install/uninstall clean it up.
type harnessTarget struct {
	name string
	// detect returns true if this harness appears to be installed.
	detect func() bool
	// path of the SKILL.md we manage.
	path func() string
	// shared root-agent instructions file where optional guidance is managed.
	guidancePath func() string
	// legacyAgentsMD is a shared instructions file older versions managed a
	// marker block in; cleaned up on install/uninstall. Empty = none.
	legacyAgentsMD func() string
}

func home() string { h, _ := os.UserHomeDir(); return h }

func hasCLI(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func skillTargets() []harnessTarget {
	return []harnessTarget{
		{
			name:         "claude-code",
			detect:       func() bool { return hasCLI("claude") || dirExists(filepath.Join(home(), ".claude")) },
			path:         func() string { return filepath.Join(home(), ".claude", "skills", "dyna", "SKILL.md") },
			guidancePath: func() string { return filepath.Join(home(), ".claude", "CLAUDE.md") },
		},
		{
			name:           "codex",
			detect:         func() bool { return hasCLI("codex") || dirExists(filepath.Join(home(), ".codex")) },
			path:           func() string { return filepath.Join(home(), ".codex", "skills", "dyna", "SKILL.md") },
			guidancePath:   func() string { return filepath.Join(home(), ".codex", "AGENTS.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".codex", "AGENTS.md") },
		},
		{
			name:           "opencode",
			detect:         func() bool { return hasCLI("opencode") || dirExists(filepath.Join(home(), ".config", "opencode")) },
			path:           func() string { return filepath.Join(home(), ".config", "opencode", "skills", "dyna", "SKILL.md") },
			guidancePath:   func() string { return filepath.Join(home(), ".config", "opencode", "AGENTS.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".config", "opencode", "AGENTS.md") },
		},
		{
			name:           "pi",
			detect:         func() bool { return hasCLI("pi") || dirExists(filepath.Join(home(), ".pi")) },
			path:           func() string { return filepath.Join(home(), ".pi", "agent", "skills", "dyna", "SKILL.md") },
			guidancePath:   func() string { return filepath.Join(home(), ".pi", "AGENTS.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".pi", "AGENTS.md") },
		},
	}
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the dyna skill/instructions into agent harnesses",
	}

	var all bool
	install := &cobra.Command{
		Use:   "install [harness...]",
		Short: "Install into harnesses (default: all detected; or name claude-code|codex|opencode|pi)",
		RunE: func(c *cobra.Command, argv []string) error {
			targets := skillTargets()
			pick := map[string]bool{}
			for _, a := range argv {
				pick[a] = true
			}
			installed := 0
			for _, t := range targets {
				if len(pick) > 0 && !pick[t.name] {
					continue
				}
				if len(pick) == 0 && !all && !t.detect() {
					fmt.Printf("  - %-11s not detected, skipping (force with `dyna skill install %s`)\n", t.name, t.name)
					continue
				}
				if err := installSkill(t); err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
				fmt.Printf("  ✓ %-11s %s\n", t.name, t.path())
				installed++
			}
			if installed == 0 {
				fmt.Println("nothing installed")
			}
			return nil
		},
	}
	install.Flags().BoolVar(&all, "all", false, "install into every supported harness even if not detected")

	uninstall := &cobra.Command{
		Use:   "uninstall [harness...]",
		Short: "Remove the dyna skill/instructions",
		RunE: func(c *cobra.Command, argv []string) error {
			pick := map[string]bool{}
			for _, a := range argv {
				pick[a] = true
			}
			for _, t := range skillTargets() {
				if len(pick) > 0 && !pick[t.name] {
					continue
				}
				removed, err := uninstallSkill(t)
				if err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
				if removed {
					fmt.Printf("  ✓ %-11s removed\n", t.name)
				}
			}
			return nil
		},
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the skill content",
		Run:   func(c *cobra.Command, _ []string) { fmt.Print(skillFrontmatter + skillBody) },
	}

	cmd.AddCommand(install, uninstall, show, guidanceCmd())
	return cmd
}

func guidanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guidance",
		Short: "Manage optional root-agent guidance in shared instruction files",
	}

	var all bool
	install := &cobra.Command{
		Use:   "install [harness...]",
		Short: "Install root-agent guidance (default: all detected harnesses)",
		RunE: func(c *cobra.Command, argv []string) error {
			pick := make(map[string]bool, len(argv))
			for _, a := range argv {
				pick[a] = true
			}
			installed := 0
			for _, t := range skillTargets() {
				if len(pick) > 0 && !pick[t.name] {
					continue
				}
				if len(pick) == 0 && !all && !t.detect() {
					fmt.Printf("  - %-11s not detected, skipping (force with `dyna skill guidance install %s`)\n", t.name, t.name)
					continue
				}
				if err := installGuidance(t); err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
				fmt.Printf("  ✓ %-11s %s\n", t.name, t.guidancePath())
				installed++
			}
			if installed == 0 {
				fmt.Println("nothing installed")
			}
			return nil
		},
	}
	install.Flags().BoolVar(&all, "all", false, "install into every supported harness even if not detected")

	uninstall := &cobra.Command{
		Use:   "uninstall [harness...]",
		Short: "Remove root-agent guidance while preserving user content",
		RunE: func(c *cobra.Command, argv []string) error {
			pick := make(map[string]bool, len(argv))
			for _, a := range argv {
				pick[a] = true
			}
			for _, t := range skillTargets() {
				if len(pick) > 0 && !pick[t.name] {
					continue
				}
				removed, err := uninstallGuidance(t)
				if err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
				if removed {
					fmt.Printf("  ✓ %-11s removed\n", t.name)
				}
			}
			return nil
		},
	}

	cmd.AddCommand(install, uninstall)
	return cmd
}

func installSkill(t harnessTarget) error {
	p := t.path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(skillFrontmatter+skillBody), 0o644); err != nil {
		return err
	}
	removeLegacyBlock(t)
	return nil
}

func uninstallSkill(t harnessTarget) (bool, error) {
	removed := removeLegacyBlock(t)
	guidanceRemoved, err := uninstallGuidance(t)
	if err != nil {
		return removed, err
	}
	removed = removed || guidanceRemoved
	p := t.path()
	if _, err := os.Stat(p); err != nil {
		return removed, nil
	}
	if err := os.Remove(p); err != nil {
		return removed, err
	}
	// Clean the now-empty skill dir.
	os.Remove(filepath.Dir(p))
	return true, nil
}

func installGuidance(t harnessTarget) error {
	return upsertManagedBlock(t.guidancePath(), guidanceMarkBegin, guidanceMarkEnd, guidanceBody)
}

func uninstallGuidance(t harnessTarget) (bool, error) {
	return removeManagedBlock(t.guidancePath(), guidanceMarkBegin, guidanceMarkEnd)
}

func upsertManagedBlock(path, begin, end, body string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	block := begin + "\n" + strings.TrimSpace(body) + "\n" + end
	content := string(existing)
	if start := strings.Index(content, begin); start >= 0 {
		finish := strings.Index(content[start+len(begin):], end)
		if finish < 0 {
			return fmt.Errorf("managed block in %s has a begin marker without an end marker", path)
		}
		finish += start + len(begin) + len(end)
		content = content[:start] + block + content[finish:]
	} else {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += block + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func removeManagedBlock(path, begin, end string) (bool, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	content := string(existing)
	start := strings.Index(content, begin)
	if start < 0 {
		return false, nil
	}
	finish := strings.Index(content[start+len(begin):], end)
	if finish < 0 {
		return false, fmt.Errorf("managed block in %s has a begin marker without an end marker", path)
	}
	finish += start + len(begin) + len(end)
	if finish < len(content) && content[finish] == '\n' {
		finish++
	}
	out := content[:start] + content[finish:]
	if strings.TrimSpace(out) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// removeLegacyBlock strips the AGENTS.md marker block written by older dyna
// versions. Reports whether anything was removed.
func removeLegacyBlock(t harnessTarget) bool {
	if t.legacyAgentsMD == nil {
		return false
	}
	p := t.legacyAgentsMD()
	existing, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	content := string(existing)
	start := strings.Index(content, markBegin)
	if start < 0 {
		return false
	}
	end := strings.Index(content, markEnd)
	if end < 0 {
		end = len(content) - len(markEnd) - 1
	}
	out := strings.TrimRight(content[:start], "\n") + "\n" + strings.TrimLeft(content[end+len(markEnd):], "\n")
	if strings.TrimSpace(out) == "" {
		os.Remove(p)
		return true
	}
	return os.WriteFile(p, []byte(out), 0o644) == nil
}
