// Package detect discovers which models the installed agent CLIs can serve,
// feeding the profile-builder wizard.
package detect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/profile"
)

// Candidate is one model a detected harness can run.
type Candidate struct {
	Harness    string
	Model      string
	Name       string // suggested profile name
	Registered bool   // a profile for this harness+model already exists
}

// Candidates probes the installed harnesses and returns every model found.
// Detection is best-effort: a missing CLI or a failing probe just yields no
// candidates for that harness.
func Candidates(store *profile.Store) []Candidate {
	var out []Candidate
	add := func(harness, model string) {
		name := suggestName(harness, model)
		for _, c := range out {
			if c.Harness == harness && c.Model == model {
				return
			}
		}
		out = append(out, Candidate{Harness: harness, Model: model, Name: name, Registered: isRegistered(store, harness, model)})
	}

	if _, err := exec.LookPath("claude"); err == nil {
		for _, m := range []string{"opus", "sonnet", "haiku"} {
			add(profile.HarnessClaudeCode, m)
		}
	}
	if _, err := exec.LookPath("codex"); err == nil {
		for _, m := range codexModels() {
			add(profile.HarnessCodex, m)
		}
	}
	if _, err := exec.LookPath("opencode"); err == nil {
		for _, m := range opencodeModels() {
			add(profile.HarnessOpenCode, m)
		}
	}
	if _, err := exec.LookPath("pi"); err == nil {
		add(profile.HarnessPi, "")
	}
	return out
}

func isRegistered(store *profile.Store, harness, model string) bool {
	for _, p := range store.Profiles {
		if p.Harness == harness && p.Model == model {
			return true
		}
	}
	return false
}

func suggestName(harness, model string) string {
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
	}[harness]
	name := prefix + "-" + base
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[^a-z0-9.-]+`).ReplaceAllString(name, "-")
	return strings.Trim(name, "-")
}

// codexModels combines the model(s) named in ~/.codex/config.toml with the
// current well-known codex model ids.
func codexModels() []string {
	models := []string{}
	if b, err := os.ReadFile(filepath.Join(home(), ".codex", "config.toml")); err == nil {
		re := regexp.MustCompile(`(?m)^\s*model\s*=\s*["']([^"']+)["']`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			models = append(models, m[1])
		}
	}
	models = append(models, "gpt-5.5-codex", "gpt-5.5", "gpt-5.2-codex")
	return dedup(models)
}

// opencodeModels asks the opencode CLI which provider/model ids it can serve.
func opencodeModels() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "opencode", "models").Output()
	if err != nil {
		return nil
	}
	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.ContainsAny(line, " \t") || !strings.Contains(line, "/") {
			continue
		}
		models = append(models, line)
		if len(models) >= 40 {
			break
		}
	}
	return dedup(models)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func home() string { h, _ := os.UserHomeDir(); return h }
