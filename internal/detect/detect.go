// Package detect discovers which models and reasoning efforts the installed
// agent CLIs can serve, feeding the profile-builder wizard. Detection asks
// the harness CLIs themselves (`codex debug models`, `claude --help`,
// `opencode models`); static lists are only a last-resort fallback.
package detect

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/profile"
)

// Model is one model a harness reports it can serve.
type Model struct {
	ID          string
	Description string   // harness-provided blurb, if any
	Efforts     []string // reasoning efforts this model supports (may be empty)
}

// Installed reports whether the harness's CLI is available. custom and mock
// need no CLI.
func Installed(harness string) bool {
	bin := map[string]string{
		profile.HarnessClaudeCode: "claude",
		profile.HarnessCodex:      "codex",
		profile.HarnessOpenCode:   "opencode",
		profile.HarnessPi:         "pi",
	}[harness]
	if bin == "" {
		return true
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// Models asks the harness CLI what it can serve. Best-effort: an empty list
// means "type it yourself".
func Models(harness string) []Model {
	switch harness {
	case profile.HarnessClaudeCode:
		return claudeModels()
	case profile.HarnessCodex:
		return codexModels()
	case profile.HarnessOpenCode:
		return opencodeModels()
	}
	return nil
}

// Efforts is the harness-generic effort list, used when the user typed a
// model id by hand so no per-model catalog entry exists.
func Efforts(harness string) []string {
	switch harness {
	case profile.HarnessCodex:
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	case profile.HarnessClaudeCode:
		return []string{"low", "medium", "high"}
	}
	return nil
}

// ApplyEffort returns the extra args / env vars that select the given effort
// on the harness.
func ApplyEffort(harness, effort string) (extraArgs []string, env map[string]string) {
	if effort == "" || effort == "default" {
		return nil, nil
	}
	switch harness {
	case profile.HarnessCodex:
		return []string{"-c", "model_reasoning_effort=" + effort}, nil
	case profile.HarnessClaudeCode:
		// claude-code has no effort flag; thinking budget approximates it.
		tokens := map[string]string{"low": "4096", "medium": "16384", "high": "63999"}[effort]
		if tokens == "" {
			return nil, nil
		}
		return nil, map[string]string{"MAX_THINKING_TOKENS": tokens}
	}
	return nil, nil
}

// SuggestName proposes a profile name for a harness/model/effort combo.
func SuggestName(harness, model, effort string) string {
	base := model
	if base == "" {
		base = "default"
	}
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	prefix := map[string]string{
		profile.HarnessClaudeCode: "claude",
		profile.HarnessCodex:      "codex",
		profile.HarnessOpenCode:   "oc",
		profile.HarnessPi:         "pi",
		profile.HarnessCustom:     "custom",
		profile.HarnessMock:       "mock",
	}[harness]
	name := prefix + "-" + base
	if effort != "" && effort != "default" {
		name += "-" + effort
	}
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[^a-z0-9.-]+`).ReplaceAllString(name, "-")
	return strings.Trim(name, "-")
}

// claudeModels parses the model aliases out of `claude --help`'s --model
// section; the CLI documents its own accepted aliases there.
func claudeModels() []Model {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--help").CombinedOutput()
	efforts := Efforts(profile.HarnessClaudeCode)
	var models []Model
	if err == nil {
		// Isolate the --model option's paragraph, then collect its quoted
		// aliases (e.g. 'fable', 'opus', 'sonnet').
		text := string(out)
		if i := strings.Index(text, "--model "); i >= 0 {
			section := text[i:]
			if j := regexp.MustCompile(`\n\s+-{1,2}[a-z]`).FindStringIndex(section); j != nil {
				section = section[:j[0]]
			}
			seen := map[string]bool{}
			for _, m := range regexp.MustCompile(`'([a-z][a-z0-9.-]*)'`).FindAllStringSubmatch(section, -1) {
				alias := m[1]
				// Skip the full-name example (e.g. 'claude-fable-5'); aliases
				// are the stable way to address models.
				if strings.HasPrefix(alias, "claude-") || seen[alias] {
					continue
				}
				seen[alias] = true
				models = append(models, Model{ID: alias, Efforts: efforts})
			}
		}
	}
	if len(models) == 0 { // fallback if the help text changes shape
		for _, id := range []string{"opus", "sonnet", "haiku"} {
			models = append(models, Model{ID: id, Efforts: efforts})
		}
	}
	return models
}

// codexModels reads the real model catalog from `codex debug models`.
func codexModels() []Model {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err == nil {
		var catalog struct {
			Models []struct {
				Slug        string `json:"slug"`
				Description string `json:"description"`
				Visibility  string `json:"visibility"`
				Levels      []struct {
					Effort string `json:"effort"`
				} `json:"supported_reasoning_levels"`
			} `json:"models"`
		}
		if json.Unmarshal(out, &catalog) == nil && len(catalog.Models) > 0 {
			var models []Model
			for _, m := range catalog.Models {
				if m.Slug == "" || m.Visibility == "hidden" {
					continue
				}
				var efforts []string
				for _, l := range m.Levels {
					efforts = append(efforts, l.Effort)
				}
				models = append(models, Model{ID: m.Slug, Description: m.Description, Efforts: efforts})
			}
			if len(models) > 0 {
				return models
			}
		}
	}
	// Fallback: whatever config.toml names, with generic efforts.
	var models []Model
	if b, err := os.ReadFile(filepath.Join(home(), ".codex", "config.toml")); err == nil {
		re := regexp.MustCompile(`(?m)^\s*model\s*=\s*["']([^"']+)["']`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			models = append(models, Model{ID: m[1], Efforts: Efforts(profile.HarnessCodex)})
		}
	}
	return dedup(models)
}

// opencodeModels asks the opencode CLI which provider/model ids it can serve.
func opencodeModels() []Model {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "opencode", "models").Output()
	if err != nil {
		return nil
	}
	var models []Model
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.ContainsAny(line, " \t") || !strings.Contains(line, "/") {
			continue
		}
		models = append(models, Model{ID: line})
		if len(models) >= 60 {
			break
		}
	}
	return dedup(models)
}

func dedup(in []Model) []Model {
	seen := map[string]bool{}
	var out []Model
	for _, m := range in {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

func home() string { h, _ := os.UserHomeDir(); return h }
