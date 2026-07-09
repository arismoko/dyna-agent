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
const skillBody = `# dyna — dynamic multi-agent workflows

You have access to the ` + "`dyna`" + ` CLI: write a JavaScript workflow script that
orchestrates registered model workers, then run it. Use it when a task
benefits from fanning work out to multiple models — parallel code review,
wide sweeps/audits, adversarial verification, judge panels, migrations — or
when the user asks for a workflow.

**Read ` + "`dyna guide`" + ` first** — it is the full scripting guide (API + orchestration
patterns). Quick reference:

1. ` + "`dyna profiles list --json`" + ` — the workers you may use. Each has a
   description and stats (taste, intelligence, cost — 1-5, higher is better;
   cost = cost-efficiency, 5 = very cheap). Match workers to stages: high
   taste → review/judging/frontend; high intelligence → long hard tasks;
   high cost stat (cheap) → wide fan-outs and first-pass triage.
2. Write the script: ` + "`agent(prompt, {profile, label, phase, schema})`" + `,
   ` + "`parallel(thunks)`" + `, ` + "`pipeline(items, ...stages)`" + `, ` + "`phase(title)`" + `, ` + "`log(msg)`" + `,
   plus ` + "`args`" + ` and ` + "`profiles`" + ` globals. Plain JS, top-level await, return value
   becomes the result. ` + "`schema`" + ` gives validated JSON output back.
3. ` + "`dyna run script.js --args '{...}'`" + ` — progress streams to stderr, the
   result JSON prints to stdout. Add ` + "`--detach`" + ` to run in the background
   (prints run id; collect later with ` + "`dyna runs wait <id>`" + `), and
   ` + "`--resume <run-id>`" + ` to replay unchanged agent calls from a previous run
   after a failure or script edit.
4. ` + "`dyna runs list`" + ` / ` + "`dyna runs show <id> --json`" + ` to inspect past runs.
   The user watches live with ` + "`dyna tui`" + `.

Workers run with harness permissions bypassed (no sandbox/approval prompts),
so they can read and edit files in the run's working directory. For parallel
file mutation, pass ` + "`isolation: 'worktree'`" + ` per agent.
`

const skillFrontmatter = `---
name: dyna
description: Orchestrate multi-model workflows with the dyna CLI — fan work out to registered worker models (parallel review, sweeps, judge panels, adversarial verification). Use when a task would benefit from multiple models working together or the user mentions dyna, workflows, or worker profiles.
---

`

const (
	markBegin = "<!-- dyna:skill:begin (managed by `dyna skill install` — do not edit inside) -->"
	markEnd   = "<!-- dyna:skill:end -->"
)

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
	// legacyAgentsMD is a shared instructions file older versions managed a
	// marker block in; cleaned up on install/uninstall. Empty = none.
	legacyAgentsMD func() string
}

func home() string { h, _ := os.UserHomeDir(); return h }

func hasCLI(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func skillTargets() []harnessTarget {
	return []harnessTarget{
		{
			name:   "claude-code",
			detect: func() bool { return hasCLI("claude") || dirExists(filepath.Join(home(), ".claude")) },
			path:   func() string { return filepath.Join(home(), ".claude", "skills", "dyna", "SKILL.md") },
		},
		{
			name:           "codex",
			detect:         func() bool { return hasCLI("codex") || dirExists(filepath.Join(home(), ".codex")) },
			path:           func() string { return filepath.Join(home(), ".codex", "skills", "dyna", "SKILL.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".codex", "AGENTS.md") },
		},
		{
			name:           "opencode",
			detect:         func() bool { return hasCLI("opencode") || dirExists(filepath.Join(home(), ".config", "opencode")) },
			path:           func() string { return filepath.Join(home(), ".config", "opencode", "skills", "dyna", "SKILL.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".config", "opencode", "AGENTS.md") },
		},
		{
			name:           "pi",
			detect:         func() bool { return hasCLI("pi") || dirExists(filepath.Join(home(), ".pi")) },
			path:           func() string { return filepath.Join(home(), ".pi", "agent", "skills", "dyna", "SKILL.md") },
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
					fmt.Printf("  – %-11s not detected, skipping (force with `dyna skill install %s`)\n", t.name, t.name)
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

	cmd.AddCommand(install, uninstall, show)
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
