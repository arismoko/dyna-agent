package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// The detailed reference and runnable examples live in guide/GUIDE.md
// (`dyna guide`). Shared orchestration mechanics live in guidance_shared.go;
// Pi adds its tool-native invocation mechanics in pi.go.
const agentFacingGuidance = `When this skill loads, open your response with: "Dyna orchestration engaged — ready to fan out the fleet."

# Multi-model workflows with dyna

Dyna runs plain JavaScript workflow files that orchestrate registered model
workers deterministically.

## Before orchestrating

- Scout inline first: list files, inspect the diff, and discover the concrete
  work list. Then orchestrate over that list. Keep each run to one coherent
  phase when reading its result should influence the next phase. Scale to the
  user's words: a quick check needs a small fan-out and one verification pass;
  a thorough audit can justify broader finders, multiple votes, and synthesis.
- If these instructions arrived inside a worker prompt with a run-owned Dyna
  journal, you are already a Dyna worker. Never load the Dyna skill, run a
  workflow, or recursively orchestrate Dyna; use only ` + "`dyna journal`" + `.
  Native harness subagents remain governed by the selected
  profile; ` + "`disableSubagents`" + ` profiles require the worker to finish alone.

## CLI invocation

Run ` + "`dyna profiles list --json`" + ` to inspect the enabled fleet, read
` + "`dyna guide`" + `, then write a plain ` + "`.js`" + ` workflow file. Run it with
` + "`dyna run workflow.js --args '{...}'`" + `; progress goes to stderr and the
returned JSON goes to stdout. ` + "`--detach`" + ` prints a run ID immediately, which
` + "`dyna runs wait <id>`" + ` collects. ` + "`--resume <id>`" + ` reuses successful calls
matching profile, prompt, and schema; failures and kept changed worktrees
rerun. Inspect a run with ` + "`dyna runs show <id> --json`" + ` or ` + "`dyna tui`" + `.

` + sharedProfileRoutingGuidance + sharedScriptContractGuidance + sharedWorkflowShapeGuidance + sharedQualityPatternsGuidance + `## Worker journals

Dyna gives every worker a live ` + "`agents/<agent-id>/journal.jsonl`" + ` progress
side channel and keeps the root ` + "`journal.jsonl`" + ` as the completed-call/resume
ledger. Dyna injects the journal and no-recursion rules automatically; reinforce
brief entries after orientation, on meaningful findings/decisions/verification/
blockers, before long operations, and before finishing. The journal never replaces
the worker's final response or schema output. A quiet resumable built-in session is
reminded in that exact session; Dyna never starts a replacement merely to obtain a
journal entry.
`

const skillBody = agentFacingGuidance

const skillDescription = "Load Dyna orchestration when the user explicitly requests Dyna, a workflow, agent fan-out, or multi-model orchestration such as parallel review, audits, judge panels, adversarial verification, or isolated migrations; do not infer that scale merely because it could help, and use ordinary subagents for small context-local delegation or describe the proposed fleet and ask."

const skillFrontmatter = "---\nname: load-dyna-orchestrator\ndescription: " + skillDescription + "\n---\n\n"

const piSkillFrontmatter = "---\nname: load-dyna-orchestrator\ndescription: " + skillDescription + "\ndisable-model-invocation: true\n---\n\n"

const (
	markBegin         = "<!-- dyna:skill:begin (managed by `dyna skill install`; do not edit inside) -->"
	markEnd           = "<!-- dyna:skill:end -->"
	guidanceMarkBegin = "<!-- dyna:guidance:begin (managed by `dyna skill guidance install`; do not edit inside) -->"
	guidanceMarkEnd   = "<!-- dyna:guidance:end -->"
)

// Every supported harness loads Claude-style skills (a directory holding a
// SKILL.md with name/description frontmatter), so installation is always a
// standalone skill dir. Legacy fields identify locations managed by older
// versions; install and uninstall clean them up.
type harnessTarget struct {
	name string
	// detect returns true if this harness appears to be installed.
	detect func() bool
	// path of the SKILL.md we manage.
	path func() string
	// legacySkillDir is the old skills/dyna directory retired when the skill
	// was renamed. Empty = none.
	legacySkillDir func() string
	// shared root-agent instructions file where retired guidance is removed.
	guidancePath func() string
	// legacyAgentsMD is a shared instructions file older versions managed a
	// marker block in; cleaned up on install/uninstall. Empty = none.
	legacyAgentsMD func() string
	// legacyGuidancePath is an obsolete location for the current guidance
	// marker block. Empty = none.
	legacyGuidancePath func() string
}

func home() string { h, _ := os.UserHomeDir(); return h }

func piAgentDir() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		if dir == "~" {
			return home()
		}
		if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
			return filepath.Join(home(), dir[2:])
		}
		return dir
	}
	return filepath.Join(home(), ".pi", "agent")
}

func hasCLI(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func skillTargets() []harnessTarget {
	return []harnessTarget{
		{
			name:           "claude-code",
			detect:         func() bool { return hasCLI("claude") || dirExists(filepath.Join(home(), ".claude")) },
			path:           func() string { return filepath.Join(home(), ".claude", "skills", "load-dyna-orchestrator", "SKILL.md") },
			legacySkillDir: func() string { return filepath.Join(home(), ".claude", "skills", "dyna") },
			guidancePath:   func() string { return filepath.Join(home(), ".claude", "CLAUDE.md") },
		},
		{
			name:           "codex",
			detect:         func() bool { return hasCLI("codex") || dirExists(filepath.Join(home(), ".codex")) },
			path:           func() string { return filepath.Join(home(), ".codex", "skills", "load-dyna-orchestrator", "SKILL.md") },
			legacySkillDir: func() string { return filepath.Join(home(), ".codex", "skills", "dyna") },
			guidancePath:   func() string { return filepath.Join(home(), ".codex", "AGENTS.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".codex", "AGENTS.md") },
		},
		{
			name:   "opencode",
			detect: func() bool { return hasCLI("opencode") || dirExists(filepath.Join(home(), ".config", "opencode")) },
			path: func() string {
				return filepath.Join(home(), ".config", "opencode", "skills", "load-dyna-orchestrator", "SKILL.md")
			},
			legacySkillDir: func() string { return filepath.Join(home(), ".config", "opencode", "skills", "dyna") },
			guidancePath:   func() string { return filepath.Join(home(), ".config", "opencode", "AGENTS.md") },
			legacyAgentsMD: func() string { return filepath.Join(home(), ".config", "opencode", "AGENTS.md") },
		},
		{
			name:               "pi",
			detect:             func() bool { return hasCLI("pi") || dirExists(filepath.Join(home(), ".pi")) },
			path:               func() string { return filepath.Join(piAgentDir(), "skills", "load-dyna-orchestrator", "SKILL.md") },
			legacySkillDir:     func() string { return filepath.Join(piAgentDir(), "skills", "dyna") },
			guidancePath:       func() string { return filepath.Join(piAgentDir(), "AGENTS.md") },
			legacyAgentsMD:     func() string { return filepath.Join(home(), ".pi", "AGENTS.md") },
			legacyGuidancePath: func() string { return filepath.Join(home(), ".pi", "AGENTS.md") },
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
		Short: "Remove retired root-agent guidance from shared instruction files",
	}

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

	cmd.AddCommand(uninstall)
	return cmd
}

func installSkill(t harnessTarget) error {
	if _, err := uninstallGuidance(t); err != nil {
		return err
	}
	removeLegacyBlock(t)
	if _, err := removeLegacySkillDir(t); err != nil {
		return err
	}
	p := t.path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	frontmatter := skillFrontmatter
	if t.name == "pi" {
		frontmatter = piSkillFrontmatter
	}
	if err := os.WriteFile(p, []byte(frontmatter+skillBody), 0o644); err != nil {
		return err
	}
	return nil
}

func uninstallSkill(t harnessTarget) (bool, error) {
	removed := removeLegacyBlock(t)
	legacySkillRemoved, err := removeLegacySkillDir(t)
	if err != nil {
		return removed, err
	}
	removed = removed || legacySkillRemoved
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

func removeLegacySkillDir(t harnessTarget) (bool, error) {
	if t.legacySkillDir == nil {
		return false, nil
	}
	dir := t.legacySkillDir()
	if dir == "" || dir == filepath.Dir(t.path()) {
		return false, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

func uninstallGuidance(t harnessTarget) (bool, error) {
	if t.guidancePath == nil {
		return removeLegacyGuidance(t)
	}
	path := t.guidancePath()
	removed, err := removeManagedBlock(path, guidanceMarkBegin, guidanceMarkEnd)
	if err != nil {
		return false, err
	}
	legacyRemoved, legacyErr := removeLegacyGuidance(t)
	if legacyErr != nil {
		return removed, legacyErr
	}
	removed = removed || legacyRemoved
	return removed, nil
}

func removeLegacyGuidance(t harnessTarget) (bool, error) {
	if t.legacyGuidancePath == nil {
		return false, nil
	}
	legacyPath := t.legacyGuidancePath()
	if legacyPath == "" || (t.guidancePath != nil && legacyPath == t.guidancePath()) {
		return false, nil
	}
	return removeManagedBlock(legacyPath, guidanceMarkBegin, guidanceMarkEnd)
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
