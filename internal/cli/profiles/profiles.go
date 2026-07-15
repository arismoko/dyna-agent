package profiles

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/profile"
)

//go:embed defaults/profiles.json
var defaultProfilesJSON []byte

func BundledDefaults() []byte {
	return defaultProfilesJSON
}

func loadStore() (*profile.Store, error) {
	return profile.Load(profile.DefaultPath())
}

// ---------- profiles ----------

func NewCommand() *cobra.Command {
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
	f.IntVar(&p.TimeoutSec, "timeout", 0, "per-call timeout in seconds (default: 5 hours; explicit values have a 30-minute minimum)")
	f.BoolVar(&p.Default, "default", false, "make this the default profile")
	f.BoolVar(&p.Managed, "managed", false, "refresh this profile from bundled defaults on dyna updates")
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
