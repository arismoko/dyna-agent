// Package profile manages worker profiles: registered model+harness combos
// with human-authored descriptions and standardized stats (taste,
// intelligence, cost, all 1..10, higher is better; for cost, higher means
// cheaper / better value).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
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
	Taste        int               `json:"taste"`        // 1..10, higher better
	Intelligence int               `json:"intelligence"` // 1..10, higher better
	Cost         int               `json:"cost"`         // 1..10, higher better (10 = very cheap)
	ExtraArgs    []string          `json:"extraArgs,omitempty"`
	Command      []string          `json:"command,omitempty"` // custom harness: argv, {{prompt}}/{{model}} placeholders
	Env          map[string]string `json:"env,omitempty"`
	TimeoutSec   int               `json:"timeoutSec,omitempty"`
	Default      bool              `json:"default,omitempty"`
	// Managed profiles are owned by the bundled defaults and automatically
	// refreshed when a newer dyna build ships different bundled values.
	Managed bool `json:"managed,omitempty"`
	// SafeMode keeps the harness's own permission prompts / sandbox. By
	// default dyna bypasses them (workers run headless and must act freely).
	SafeMode bool `json:"safeMode,omitempty"`
	// DisableSubagents prevents this worker from delegating to child agents.
	// Dyna may still launch the worker itself.
	DisableSubagents bool `json:"disableSubagents,omitempty"`
	// MaxConcurrent caps how many workers of this profile may run at once
	// across a workflow (0 = unlimited). Expensive models set this low.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// MaxCallsPerRun caps total agent() calls to this profile in one run
	// (0 = unlimited). Exceeding it aborts the whole run.
	MaxCallsPerRun int `json:"maxCallsPerRun,omitempty"`
	// Disabled hides the profile from workflows (agent() calls to it fail,
	// it leaves the scripts' profiles global) while keeping its stats and
	// description saved and editable.
	Disabled bool `json:"disabled,omitempty"`
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
		if v.v < 1 || v.v > 10 {
			return fmt.Errorf("%s must be 1..10 (got %d)", v.n, v.v)
		}
	}
	if p.Harness == HarnessCustom && len(p.Command) == 0 {
		return errors.New("custom harness requires a command")
	}
	for _, v := range []struct {
		n string
		v int
	}{
		{"timeoutSec", p.TimeoutSec},
		{"maxConcurrent", p.MaxConcurrent},
		{"maxCallsPerRun", p.MaxCallsPerRun},
	} {
		if v.v < 0 {
			return fmt.Errorf("%s must be non-negative (got %d)", v.n, v.v)
		}
	}
	return nil
}

// Store is the on-disk profile registry.
type Store struct {
	Path     string    `json:"-"`
	Version  int       `json:"version"` // 2 = 1..10 stat scale
	Profiles []Profile `json:"profiles"`
}

const storeVersion = 2

var (
	bundledMu       sync.RWMutex
	bundledProfiles map[string]Profile
)

// SetBundledDefaults configures the bundled profiles that Load uses to
// refresh managed profiles. Passing nil clears the bundle.
func SetBundledDefaults(raw []byte) error {
	profiles := make(map[string]Profile)
	if len(raw) > 0 {
		var bundle struct {
			Profiles []Profile `json:"profiles"`
		}
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return err
		}
		for _, p := range bundle.Profiles {
			profiles[p.Name] = cloneProfile(p)
		}
	}
	bundledMu.Lock()
	bundledProfiles = profiles
	bundledMu.Unlock()
	return nil
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
	changed := false
	// v1 stores used a 1..5 stat scale; double onto the 1..10 scale once.
	if s.Version < storeVersion && len(s.Profiles) > 0 {
		for i := range s.Profiles {
			s.Profiles[i].Taste = scaleTo10(s.Profiles[i].Taste)
			s.Profiles[i].Intelligence = scaleTo10(s.Profiles[i].Intelligence)
			s.Profiles[i].Cost = scaleTo10(s.Profiles[i].Cost)
		}
		s.Version = storeVersion
		changed = true
	}
	if s.refreshManaged() {
		changed = true
	}
	for _, p := range s.Profiles {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("invalid profile %q in %s: %w", p.Name, path, err)
		}
	}
	if changed {
		if err := s.Save(); err != nil {
			return nil, fmt.Errorf("updating %s: %w", path, err)
		}
	}
	sort.Slice(s.Profiles, func(i, j int) bool { return s.Profiles[i].Name < s.Profiles[j].Name })
	return s, nil
}

func (s *Store) refreshManaged() bool {
	bundledMu.RLock()
	defer bundledMu.RUnlock()

	changed := false
	for i := range s.Profiles {
		stored := &s.Profiles[i]
		bundle, ok := bundledProfiles[stored.Name]
		if !stored.Managed || !ok || bundledFieldsEqual(*stored, bundle) {
			continue
		}
		defaultValue, disabled, managed := stored.Default, stored.Disabled, stored.Managed
		copyBundledFields(stored, bundle)
		stored.Default, stored.Disabled, stored.Managed = defaultValue, disabled, managed
		changed = true
	}
	return changed
}

// ApplyBundledPreferences updates profiles whose names collide with the
// bundled fleet. Replacement preserves the user's default and disabled flags.
func (s *Store) ApplyBundledPreferences(replace, managed bool) int {
	bundledMu.RLock()
	defer bundledMu.RUnlock()

	collisions := 0
	for i := range s.Profiles {
		stored := &s.Profiles[i]
		bundle, ok := bundledProfiles[stored.Name]
		if !ok {
			continue
		}
		collisions++
		defaultValue, disabled := stored.Default, stored.Disabled
		if replace {
			copyBundledFields(stored, bundle)
		}
		stored.Default, stored.Disabled, stored.Managed = defaultValue, disabled, managed
	}
	return collisions
}

func bundledFieldsEqual(a, b Profile) bool {
	a.Default, a.Disabled, a.Managed, a.Name = false, false, false, ""
	b.Default, b.Disabled, b.Managed, b.Name = false, false, false, ""
	return reflect.DeepEqual(a, b)
}

func copyBundledFields(dst *Profile, src Profile) {
	dst.Description = src.Description
	dst.Harness = src.Harness
	dst.Model = src.Model
	dst.Taste = src.Taste
	dst.Intelligence = src.Intelligence
	dst.Cost = src.Cost
	dst.ExtraArgs = slices.Clone(src.ExtraArgs)
	dst.Command = slices.Clone(src.Command)
	dst.Env = maps.Clone(src.Env)
	dst.TimeoutSec = src.TimeoutSec
	dst.SafeMode = src.SafeMode
	dst.DisableSubagents = src.DisableSubagents
	dst.MaxConcurrent = src.MaxConcurrent
	dst.MaxCallsPerRun = src.MaxCallsPerRun
}

func cloneProfile(p Profile) Profile {
	p.ExtraArgs = slices.Clone(p.ExtraArgs)
	p.Command = slices.Clone(p.Command)
	p.Env = maps.Clone(p.Env)
	return p
}

func scaleTo10(v int) int {
	v *= 2
	if v < 1 {
		v = 1
	}
	if v > 10 {
		v = 10
	}
	return v
}

func (s *Store) Save() error {
	if s.Version == 0 {
		s.Version = storeVersion
	}
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

// DefaultProfile returns the enabled profile marked default, or the first
// enabled one.
func (s *Store) DefaultProfile() (*Profile, bool) {
	for i := range s.Profiles {
		if s.Profiles[i].Default && !s.Profiles[i].Disabled {
			return &s.Profiles[i], true
		}
	}
	for i := range s.Profiles {
		if !s.Profiles[i].Disabled {
			return &s.Profiles[i], true
		}
	}
	return nil, false
}
