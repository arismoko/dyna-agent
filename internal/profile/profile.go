// Package profile manages worker profiles: registered model+harness combos
// with human-authored descriptions and standardized stats (taste,
// intelligence, cost — all 1..5, higher is better; for cost, higher means
// cheaper / better value).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Harness identifies which CLI executes the worker.
const (
	HarnessClaudeCode = "claude-code"
	HarnessCodex      = "codex"
	HarnessOpenCode   = "opencode"
	HarnessPi         = "pi"
	HarnessCustom     = "custom"
	HarnessMock       = "mock"
)

var Harnesses = []string{HarnessClaudeCode, HarnessCodex, HarnessOpenCode, HarnessPi, HarnessCustom, HarnessMock}

// Profile describes one registered worker.
type Profile struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Harness      string            `json:"harness"`
	Model        string            `json:"model,omitempty"`
	Taste        int               `json:"taste"`        // 1..5, higher better
	Intelligence int               `json:"intelligence"` // 1..5, higher better
	Cost         int               `json:"cost"`         // 1..5, higher better (5 = very cheap)
	ExtraArgs    []string          `json:"extraArgs,omitempty"`
	Command      []string          `json:"command,omitempty"` // custom harness: argv, {{prompt}}/{{model}} placeholders
	Env          map[string]string `json:"env,omitempty"`
	TimeoutSec   int               `json:"timeoutSec,omitempty"`
	Default      bool              `json:"default,omitempty"`
	// SafeMode keeps the harness's own permission prompts / sandbox. By
	// default dyna bypasses them (workers run headless and must act freely).
	SafeMode bool `json:"safeMode,omitempty"`
}

func (p *Profile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if strings.ContainsAny(p.Name, " \t\n") {
		return errors.New("profile name must not contain whitespace (use dashes)")
	}
	ok := false
	for _, h := range Harnesses {
		if p.Harness == h {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("unknown harness %q (valid: %s)", p.Harness, strings.Join(Harnesses, ", "))
	}
	for _, v := range []struct {
		n string
		v int
	}{{"taste", p.Taste}, {"intelligence", p.Intelligence}, {"cost", p.Cost}} {
		if v.v < 1 || v.v > 5 {
			return fmt.Errorf("%s must be 1..5 (got %d)", v.n, v.v)
		}
	}
	if p.Harness == HarnessCustom && len(p.Command) == 0 {
		return errors.New("custom harness requires a command")
	}
	return nil
}

// Store is the on-disk profile registry.
type Store struct {
	Path     string
	Profiles []Profile `json:"profiles"`
}

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "profiles.json")
}

func Load(path string) (*Store, error) {
	s := &Store{Path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.Path = path
	sort.Slice(s.Profiles, func(i, j int) bool { return s.Profiles[i].Name < s.Profiles[j].Name })
	return s, nil
}

func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, append(b, '\n'), 0o644)
}

func (s *Store) Get(name string) (*Profile, bool) {
	for i := range s.Profiles {
		if s.Profiles[i].Name == name {
			return &s.Profiles[i], true
		}
	}
	return nil, false
}

func (s *Store) Upsert(p Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Default {
		for i := range s.Profiles {
			s.Profiles[i].Default = false
		}
	}
	for i := range s.Profiles {
		if s.Profiles[i].Name == p.Name {
			s.Profiles[i] = p
			return nil
		}
	}
	s.Profiles = append(s.Profiles, p)
	sort.Slice(s.Profiles, func(i, j int) bool { return s.Profiles[i].Name < s.Profiles[j].Name })
	return nil
}

func (s *Store) Remove(name string) bool {
	for i := range s.Profiles {
		if s.Profiles[i].Name == name {
			s.Profiles = append(s.Profiles[:i], s.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// DefaultProfile returns the profile marked default, or the first one.
func (s *Store) DefaultProfile() (*Profile, bool) {
	for i := range s.Profiles {
		if s.Profiles[i].Default {
			return &s.Profiles[i], true
		}
	}
	if len(s.Profiles) > 0 {
		return &s.Profiles[0], true
	}
	return nil, false
}
