// dyna: harness-agnostic dynamic multi-agent workflows.
// CLI for agents (codex, claude-code, pi, opencode) + TUI for humans.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"dyna-agent/internal/engine"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/tui"
	selfupdate "dyna-agent/internal/update"
)

//go:embed guide/GUIDE.md
var guideMD string

//go:embed defaults/profiles.json
var defaultProfilesJSON []byte

// version is stamped from the release tag with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "dyna",
		Short:   "Dynamic multi-agent workflows for any coding agent",
		Version: resolveVersion(),
		Long: "dyna runs JavaScript workflow scripts that orchestrate registered worker\n" +
			"profiles (claude-code, codex, opencode, pi, custom CLIs). Agents use the CLI;\n" +
			"humans use `dyna tui` to configure profiles and watch runs live.",
		SilenceUsage: true,
	}
	root.SetVersionTemplate("dyna {{.Version}}\n")
	root.AddCommand(profilesCmd(), runCmd(), runsCmd(), journalCmd(), guideCmd(), tuiCmd(), demoCmd(), skillCmd(), updateCmd(), versionCmd())
	return root
}

func resolveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			revision := setting.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
			return "dev+" + revision
		}
	}
	return "dev"
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the installed dyna version",
		Run: func(c *cobra.Command, _ []string) {
			fmt.Fprintf(c.OutOrStdout(), "dyna %s\n", resolveVersion())
		},
	}
}

func updateCmd() *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install the latest stable GitHub release",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg := updateConfig()
			if check {
				result, err := cfg.Check(c.Context(), false)
				if err != nil {
					return err
				}
				printUpdateStatus(c.OutOrStdout(), result)
				return nil
			}
			result, err := cfg.Apply(c.Context(), force, false)
			if errors.Is(err, selfupdate.ErrDevelopmentBuild) {
				return fmt.Errorf("%w; release builds self-update, or use `dyna update --force` to replace this build", err)
			}
			if err != nil {
				return err
			}
			if !result.Updated {
				printUpdateStatus(c.OutOrStdout(), result)
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "updated dyna %s -> %s; running processes continue safely on %s\n", result.Current, result.Latest, result.Current)
			if err := refreshInstalledSkills(c.Context(), result.Target, c.OutOrStdout()); err != nil {
				fmt.Fprintf(c.ErrOrStderr(), "warning: dyna updated, but skill refresh failed: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check the latest release without installing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace a development, equal, or newer local build")
	cmd.MarkFlagsMutuallyExclusive("check", "force")
	return cmd
}

func updateConfig() selfupdate.Config {
	return selfupdate.Config{
		Version:   releaseVersion(),
		Repo:      selfupdate.DefaultRepo,
		StatePath: filepath.Join(runstore.DataDir(), "update-check.json"),
	}
}

func releaseVersion() string {
	if version == "" || version == "dev" {
		return "dev"
	}
	return version
}

func printUpdateStatus(w io.Writer, result selfupdate.Result) {
	switch {
	case result.Latest == "":
		fmt.Fprintln(w, "no stable GitHub release is published yet")
	case result.Available:
		fmt.Fprintf(w, "dyna %s is available (installed: %s)\n", result.Latest, result.Current)
	case result.Current == "" || strings.HasPrefix(result.Current, "dev"):
		fmt.Fprintf(w, "latest stable release: %s (installed build: %s)\n", result.Latest, result.Current)
	default:
		fmt.Fprintf(w, "dyna %s is up to date\n", result.Current)
	}
}

func refreshInstalledSkills(ctx context.Context, executable string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, "skill", "install")
	cmd.Env = append(os.Environ(), "DYNA_NO_AUTO_UPDATE=1")
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

// ---------- worker journal ----------

func journalCmd() *cobra.Command {
	var kind, next string
	cmd := &cobra.Command{
		Use:   "journal <message>",
		Short: "Append a progress entry to this worker's journal",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runstore.AppendAgentJournalFromEnv(kind, args[0], next)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "update", "entry kind (for example: update, finding, decision, verification, blocker)")
	cmd.Flags().StringVar(&next, "next", "", "concise next step")
	return cmd
}

func loadStore() (*profile.Store, error) {
	return profile.Load(profile.DefaultPath())
}

// ---------- profiles ----------

func profilesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profiles", Short: "Manage worker profiles (registered models + stats)"}

	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered worker profiles with descriptions and stats",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(s.Profiles, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if len(s.Profiles) == 0 {
				fmt.Println("No profiles registered. Add one:\n  dyna profiles add --name opus-4.8 --harness claude-code --model opus --taste 5 --intelligence 4 --cost 2 --desc \"...\"")
				return nil
			}
			for _, p := range s.Profiles {
				def := ""
				if p.Default {
					def = "  (default)"
				}
				if p.Disabled {
					def += "  (DISABLED)"
				}
				limits := ""
				if p.MaxConcurrent > 0 {
					limits += fmt.Sprintf("  max-concurrent: %d", p.MaxConcurrent)
				}
				if p.MaxCallsPerRun > 0 {
					limits += fmt.Sprintf("  max-calls/run: %d", p.MaxCallsPerRun)
				}
				if p.DisableSubagents {
					limits += "  subagents: blocked"
				}
				fmt.Printf("%s%s\n  harness: %s  model: %s%s\n  taste: %d/10  intelligence: %d/10  cost-efficiency: %d/10\n  %s\n\n",
					p.Name, def, p.Harness, orDash(p.Model), limits, p.Taste, p.Intelligence, p.Cost, p.Description)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one profile (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			p, ok := s.Get(a[0])
			if !ok {
				return fmt.Errorf("no profile named %q", a[0])
			}
			b, _ := json.MarshalIndent(p, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}

	var p profile.Profile
	var command []string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register or update a worker profile",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			p.Command = command
			if err := s.Upsert(p); err != nil {
				return err
			}
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Printf("saved profile %q (%s)\n", p.Name, p.Harness)
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&p.Name, "name", "", "unique profile name (required)")
	f.StringVar(&p.Description, "desc", "", "personality/strengths description shown to agents")
	f.StringVar(&p.Harness, "harness", profile.HarnessClaudeCode, "one of: "+strings.Join(profile.Harnesses, ", "))
	f.StringVar(&p.Model, "model", "", "model id passed to the harness CLI")
	f.IntVar(&p.Taste, "taste", 5, "1..10, higher better")
	f.IntVar(&p.Intelligence, "intelligence", 5, "1..10, higher better")
	f.IntVar(&p.Cost, "cost", 5, "cost efficiency 1..10, higher better (10 = very cheap)")
	f.StringArrayVar(&p.ExtraArgs, "extra-arg", nil, "extra CLI arg for the harness (repeatable)")
	f.StringArrayVar(&command, "command", nil, "custom harness argv (repeatable; {{prompt}}/{{model}} placeholders)")
	f.IntVar(&p.TimeoutSec, "timeout", 0, "default per-call timeout in seconds (minimum 1800)")
	f.BoolVar(&p.Default, "default", false, "make this the default profile")
	f.BoolVar(&p.SafeMode, "safe-mode", false, "keep the harness's own permission prompts/sandbox (default: bypassed)")
	f.BoolVar(&p.DisableSubagents, "disable-subagents", false, "prevent this worker from spawning or delegating to child agents")
	f.IntVar(&p.MaxConcurrent, "limit-concurrent", 0, "max simultaneous workers of this profile per run (0 = unlimited)")
	f.IntVar(&p.MaxCallsPerRun, "limit-calls", 0, "max total calls to this profile per run (0 = unlimited)")
	add.MarkFlagRequired("name")

	rm := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			if !s.Remove(a[0]) {
				return fmt.Errorf("no profile named %q", a[0])
			}
			return s.Save()
		},
	}

	toggle := func(use, short string, disabled bool) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <name>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, a []string) error {
				s, err := loadStore()
				if err != nil {
					return err
				}
				p, ok := s.Get(a[0])
				if !ok {
					return fmt.Errorf("no profile named %q", a[0])
				}
				q := *p
				q.Disabled = disabled
				if err := s.Upsert(q); err != nil {
					return err
				}
				if err := s.Save(); err != nil {
					return err
				}
				fmt.Println(use + "d " + a[0])
				return nil
			},
		}
	}
	enable := toggle("enable", "Re-enable a disabled profile", false)
	disable := toggle("disable", "Disable a profile (stats/description kept, workflows can't use it)", true)

	setDefault := &cobra.Command{
		Use:   "set-default <name>",
		Short: "Mark a profile as the default worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			p, ok := s.Get(a[0])
			if !ok {
				return fmt.Errorf("no profile named %q", a[0])
			}
			q := *p
			q.Default = true
			if err := s.Upsert(q); err != nil {
				return err
			}
			return s.Save()
		},
	}

	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Register the bundled default worker profiles (fable, sol, sol-max, terra, luna)",
		Long: "Registers a curated starter fleet: Claude Fable 5 via claude-code plus the\n" +
			"gpt-5.6 family (sol/sol-max/terra/luna) via codex, with agent-facing\n" +
			"descriptions, stats, and sensible limits. Existing profiles with the same\n" +
			"name are kept unless --force is given.",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			var bundle struct {
				Profiles []profile.Profile `json:"profiles"`
			}
			if err := json.Unmarshal(defaultProfilesJSON, &bundle); err != nil {
				return err
			}
			// The bundle marks terra default; don't fight an existing default.
			_, hasDefault := s.DefaultProfile()
			for _, p := range bundle.Profiles {
				if _, exists := s.Get(p.Name); exists && !force {
					fmt.Printf("  - %-8s exists, kept (overwrite with --force)\n", p.Name)
					continue
				}
				if p.Default && hasDefault && !force {
					p.Default = false
				}
				if err := s.Upsert(p); err != nil {
					return err
				}
				fmt.Printf("  ✓ %-8s %s · %s\n", p.Name, p.Harness, orDash(p.Model))
			}
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Println("browse them with `dyna tui` (Profiles tab); agents see them via `dyna profiles list --json`")
			return nil
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite existing profiles with the same name")

	cmd.AddCommand(list, show, add, rm, enable, disable, setDefault, initCmd)
	return cmd
}

// ---------- run ----------

var (
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "244", Dark: "243"})
	stOK    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "35", Dark: "42"})
	stErr   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	stPhase = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "63", Dark: "111"})
	stName  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "31", Dark: "81"})
)

func runCmd() *cobra.Command {
	var argsJSON, name, dir, resumeID string
	var quiet, asJSON, detach bool
	var maxConc, maxAgents int
	cmd := &cobra.Command{
		Use:   "run <script.js>",
		Short: "Execute a workflow script (progress on stderr, result JSON on stdout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if detach {
				return detachRun(a[0])
			}
			src, err := os.ReadFile(a[0])
			if err != nil {
				return err
			}
			var argsVal any
			if argsJSON != "" {
				if err := json.Unmarshal([]byte(argsJSON), &argsVal); err != nil {
					return fmt.Errorf("--args must be valid JSON: %w", err)
				}
			}
			store, err := loadStore()
			if err != nil {
				return err
			}
			if len(store.Profiles) == 0 {
				return fmt.Errorf("no worker profiles registered; run `dyna profiles add` or `dyna demo` first")
			}
			if name == "" {
				name = metaName(string(src), a[0])
			}
			if dir == "" {
				dir, _ = os.Getwd()
			}

			var cache *engine.Cache
			if resumeID != "" {
				entries, err := runstore.ReadJournal(resumeID)
				if err != nil {
					return fmt.Errorf("cannot resume %s: %w", resumeID, err)
				}
				cache = engine.NewCache(entries)
				fmt.Fprintln(os.Stderr, stDim.Render(fmt.Sprintf("resuming from %s (%d journaled calls)", resumeID, len(entries))))
			}

			run, err := runstore.Create(name, string(src), argsVal)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, stDim.Render("run "+run.Meta.ID+"  •  watch live with `dyna tui`"))
			run.Append(runstore.Event{T: "run_start", Title: name})

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			var onEvent func(runstore.Event)
			if !quiet {
				onEvent = printEvent
			}
			start := time.Now()
			result, err := engine.Execute(ctx, engine.Options{
				ScriptSrc: string(src), Args: argsVal, Store: store,
				Run: run, OnEvent: onEvent, WorkDir: dir, MaxConc: maxConc,
				MaxAgents: maxAgents, Cache: cache,
				Paused: func() bool { return runstore.IsPaused(run.Meta.ID) },
			})
			runstore.SetPaused(run.Meta.ID, false)
			status := "ok"
			if err != nil {
				if ctx.Err() != nil {
					status = "canceled"
				} else {
					status = "error"
				}
			}
			run.Append(runstore.Event{T: "run_end", Status: status, DurMs: time.Since(start).Milliseconds()})
			run.Finish(status, result, err)
			if err != nil {
				return err
			}
			if asJSON {
				out, _ := json.Marshal(map[string]any{"runId": run.Meta.ID, "status": status, "result": json.RawMessage(orNull(result))})
				fmt.Println(string(out))
			} else {
				fmt.Println(result)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&argsJSON, "args", "", "JSON value exposed to the script as `args`")
	cmd.Flags().StringVar(&name, "name", "", "run name (default: meta.name from the script)")
	cmd.Flags().StringVar(&dir, "dir", "", "working directory for workers (default: cwd)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress progress on stderr")
	cmd.Flags().BoolVar(&asJSON, "json", false, "wrap stdout result with runId/status envelope")
	cmd.Flags().IntVar(&maxConc, "max-concurrent", 0, "max concurrent workers (default min(16, cores-2))")
	cmd.Flags().IntVar(&maxAgents, "max-agents", 0, "lifetime agent() cap for this run (default 1000)")
	cmd.Flags().StringVar(&resumeID, "resume", "", "run id to resume: unchanged agent() calls replay from its journal")
	cmd.Flags().BoolVar(&detach, "detach", false, "run in the background; prints the run id (poll with `dyna runs wait`)")
	return cmd
}

// detachRun re-execs this command in the background with a pre-assigned run
// id, so callers can immediately watch/poll it.
func detachRun(script string) error {
	id := runstore.NewID()
	runDir := filepath.Join(runstore.RunsDir(), id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	logf, err := os.Create(filepath.Join(runDir, "daemon.log"))
	if err != nil {
		return err
	}
	defer logf.Close()

	self, err := os.Executable()
	if err != nil {
		return err
	}
	var childArgs []string
	for _, a := range os.Args[1:] {
		if a == "--detach" || a == "--detach=true" {
			continue
		}
		childArgs = append(childArgs, a)
	}
	child := exec.Command(self, childArgs...)
	child.Env = append(os.Environ(), "DYNA_RUN_ID="+id)
	child.Stdout = logf
	child.Stderr = logf
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	_ = script
	fmt.Println(id)
	fmt.Fprintln(os.Stderr, stDim.Render("running in background; `dyna runs wait "+id+"` blocks until done, watch live with `dyna tui`"))
	return nil
}

func printEvent(e runstore.Event) {
	w := os.Stderr
	switch e.T {
	case "phase":
		fmt.Fprintln(w, stPhase.Render("── "+e.Title+" ──"))
	case "agent_start":
		fmt.Fprintln(w, stDim.Render("  ◌ queued  ")+stName.Render(e.Label)+stDim.Render("  ["+e.Profile+"]  "+e.Preview))
	case "agent_run":
		fmt.Fprintln(w, stDim.Render("  ▶ running ")+stName.Render(e.Label)+stDim.Render("  ["+e.Profile+"]"))
	case "agent_end":
		d := time.Duration(e.DurMs) * time.Millisecond
		switch {
		case e.Cached:
			fmt.Fprintln(w, stOK.Render("  ⚡ cached  ")+stName.Render(e.Label)+stDim.Render("  "+e.Preview))
		case e.Status == "ok":
			fmt.Fprintln(w, stOK.Render("  ✓ done    ")+stName.Render(e.Label)+stDim.Render(fmt.Sprintf("  %s  %s", d.Round(time.Second), e.Preview)))
		default:
			fmt.Fprintln(w, stErr.Render("  ✗ failed  ")+stName.Render(e.Label)+stErr.Render("  "+e.Error))
		}
	case "log":
		fmt.Fprintln(w, "  › "+e.Msg)
	}
}

// ---------- runs ----------

func runsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "runs", Short: "Inspect past and active workflow runs"}
	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List runs, newest first",
		RunE: func(c *cobra.Command, _ []string) error {
			runs, err := runstore.List()
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(runs, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			for _, r := range runs {
				status := r.Status
				if status == "running" && runstore.IsPaused(r.ID) {
					status = "paused"
				}
				fmt.Printf("%-32s %-9s %-20s %s\n", r.ID, status, r.StartedAt.Format("2006-01-02 15:04:05"), r.Name)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	var showJSON bool
	show := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show a run's events and result",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			events, err := runstore.ReadEvents(a[0])
			if err != nil {
				return err
			}
			result, _ := runstore.ReadResult(a[0])
			if showJSON {
				b, _ := json.MarshalIndent(map[string]any{"events": events, "result": json.RawMessage(orNull(result))}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			for _, e := range events {
				printEvent(e)
			}
			if result != "" {
				fmt.Println(result)
			}
			return nil
		},
	}
	show.Flags().BoolVar(&showJSON, "json", false, "machine-readable output")

	var waitTimeout int
	wait := &cobra.Command{
		Use:   "wait <run-id>",
		Short: "Block until a run finishes, then print its result JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			deadline := time.Now().Add(time.Duration(waitTimeout) * time.Second)
			started := time.Now()
			for {
				m, err := runstore.ReadMeta(a[0])
				if err != nil {
					// A just-detached run may not have written meta.json yet.
					if os.IsNotExist(err) && time.Since(started) < 15*time.Second {
						time.Sleep(300 * time.Millisecond)
						continue
					}
					return err
				}
				if m.Status != "running" {
					if result, ok := runstore.ReadResult(a[0]); ok {
						fmt.Print(result)
					}
					if m.Status != "ok" {
						return fmt.Errorf("run %s finished with status %s: %s", a[0], m.Status, m.Error)
					}
					return nil
				}
				if waitTimeout > 0 && time.Now().After(deadline) {
					return fmt.Errorf("timed out waiting for %s (still running)", a[0])
				}
				time.Sleep(500 * time.Millisecond)
			}
		},
	}
	wait.Flags().IntVar(&waitTimeout, "timeout", 0, "give up after N seconds (0 = wait forever)")

	cancel := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Stop a running workflow (in-flight workers are killed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if err := runstore.Cancel(a[0]); err != nil {
				return err
			}
			fmt.Println("canceled " + a[0])
			return nil
		},
	}

	pause := &cobra.Command{
		Use:   "pause <run-id>",
		Short: "Pause a running workflow: running workers finish, no new ones start",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			m, err := runstore.ReadMeta(a[0])
			if err != nil {
				return err
			}
			if m.Status != "running" {
				return fmt.Errorf("run %s is not running (status %s)", a[0], m.Status)
			}
			if err := runstore.SetPaused(a[0], true); err != nil {
				return err
			}
			fmt.Println("paused " + a[0] + "; resume with `dyna runs unpause " + a[0] + "`")
			return nil
		},
	}

	unpause := &cobra.Command{
		Use:   "unpause <run-id>",
		Short: "Resume a paused workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if err := runstore.SetPaused(a[0], false); err != nil {
				return err
			}
			fmt.Println("resumed " + a[0])
			return nil
		},
	}

	remove := &cobra.Command{
		Use:     "remove <run-id>...",
		Aliases: []string{"rm", "delete"},
		Short:   "Delete finished runs (cancel running ones first)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			for _, id := range a {
				if err := runstore.Remove(id); err != nil {
					return err
				}
				fmt.Println("removed " + id)
			}
			return nil
		},
	}

	clear := &cobra.Command{
		Use:   "clear",
		Short: "Delete every finished run",
		RunE: func(c *cobra.Command, _ []string) error {
			n, err := runstore.ClearCompleted()
			if err != nil {
				return err
			}
			fmt.Printf("removed %d run(s)\n", n)
			return nil
		},
	}

	cmd.AddCommand(list, show, wait, cancel, pause, unpause, remove, clear)
	return cmd
}

// ---------- guide / tui / demo ----------

func guideCmd() *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:   "guide",
		Short: "Print the workflow scripting guide (agents: read this first)",
		RunE: func(c *cobra.Command, _ []string) error {
			if plain || !isTTY() {
				fmt.Print(guideMD)
				return nil
			}
			out, err := glamour.Render(guideMD, "auto")
			if err != nil {
				fmt.Print(guideMD)
				return nil
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false, "raw markdown (default when piped)")
	return cmd
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the dashboard: configure profiles, watch workflows live",
		RunE: func(c *cobra.Command, _ []string) error {
			maybeAutoUpdateForTUI(c)
			return tui.Run(guideMD)
		},
	}
}

func maybeAutoUpdateForTUI(c *cobra.Command) {
	if os.Getenv("DYNA_NO_AUTO_UPDATE") == "1" || !isTTY() {
		return
	}
	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Minute)
	defer cancel()
	result, err := updateConfig().Apply(ctx, false, true)
	if err != nil || !result.Updated {
		return // Automatic checks are best effort and never block normal use on errors.
	}
	fmt.Fprintln(c.ErrOrStderr(), stOK.Render(fmt.Sprintf("updated dyna %s -> %s; this TUI session will finish on %s", result.Current, result.Latest, result.Current)))
	_ = refreshInstalledSkills(ctx, result.Target, io.Discard)
}

func demoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "demo",
		Short: "Register mock worker profiles and run a sample workflow",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := loadStore()
			if err != nil {
				return err
			}
			demos := []profile.Profile{
				{Name: "mock-workhorse", Harness: profile.HarnessMock, Taste: 4, Intelligence: 10, Cost: 6, Default: true,
					Description: "Demo worker. Pretend GPT-class workhorse: grinds long tasks alone, correct but unpolished code, weak frontend taste."},
				{Name: "mock-reviewer", Harness: profile.HarnessMock, Taste: 10, Intelligence: 8, Cost: 4,
					Description: "Demo worker. Pretend Opus-class reviewer: excellent taste, finds subtle issues, great frontend instincts, pricey."},
				{Name: "mock-sprinter", Harness: profile.HarnessMock, Taste: 8, Intelligence: 6, Cost: 10,
					Description: "Demo worker. Pretend GLM-class sprinter: fast and very cheap, great taste, slightly below the big models."},
			}
			for _, d := range demos {
				if _, exists := s.Get(d.Name); !exists {
					if err := s.Upsert(d); err != nil {
						return err
					}
				}
			}
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Println("registered demo profiles: mock-workhorse, mock-reviewer, mock-sprinter")
			fmt.Println("running demo workflow...")

			run, err := runstore.Create("demo-fanout", demoScript, nil)
			if err != nil {
				return err
			}
			run.Append(runstore.Event{T: "run_start", Title: "demo-fanout"})
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			cwd, _ := os.Getwd()
			start := time.Now()
			result, err := engine.Execute(ctx, engine.Options{
				ScriptSrc: demoScript, Store: s, Run: run, OnEvent: printEvent, WorkDir: cwd,
			})
			status := "ok"
			if err != nil {
				status = "error"
			}
			run.Append(runstore.Event{T: "run_end", Status: status, DurMs: time.Since(start).Milliseconds()})
			run.Finish(status, result, err)
			if err != nil {
				return err
			}
			fmt.Println(result)
			fmt.Println(stDim.Render("open `dyna tui` to browse this run"))
			return nil
		},
	}
}

const demoScript = `export const meta = { name: 'demo-fanout', description: 'demo', phases: [{title:'Scan'},{title:'Verify'}] }
phase('Scan')
log('fanning out 3 scanners')
const found = await parallel(['auth', 'billing', 'frontend'].map(area => () =>
  agent('Scan the ' + area + ' area for issues. RESPOND: {"area":"' + area + '","issues":' + (area === 'billing' ? 2 : 1) + '}',
    { profile: 'mock-sprinter', label: 'scan:' + area, schema: { type:'object', required:['area','issues'] } })))
const total = found.filter(Boolean).reduce((n, f) => n + f.issues, 0)
log(total + ' issues found, verifying')
const verdicts = await pipeline(found.filter(Boolean),
  (f) => agent('Adversarially verify issues in ' + f.area + '. RESPOND: verified ' + f.area,
    { profile: 'mock-reviewer', label: 'verify:' + f.area, phase: 'Verify' }))
return { totalIssues: total, verdicts }
`

// ---------- helpers ----------

// metaName extracts meta.name from a script via a light textual scan.
func metaName(src, fallback string) string {
	if i := strings.Index(src, "name:"); i >= 0 {
		rest := src[i+5:]
		if q := strings.IndexAny(rest, "'\""); q >= 0 {
			quote := rest[q]
			rest = rest[q+1:]
			if e := strings.IndexByte(rest, quote); e >= 0 {
				return rest[:e]
			}
		}
	}
	base := fallback
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".js")
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orNull(s string) string {
	if strings.TrimSpace(s) == "" {
		return "null"
	}
	return s
}
