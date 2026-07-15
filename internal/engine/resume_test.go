package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestExecuteWarnsAboutResumeUnstableJavaScriptAPIs(t *testing.T) {
	var warnings []string
	result, err := Execute(context.Background(), Options{
		ScriptSrc: `
const timestamp = Date.now();
const instant = new Date();
const sample = Math.random();
return [timestamp, instant.toISOString(), sample];
`,
		Store:     &profile.Store{},
		WorkDir:   t.TempDir(),
		OnWarning: func(message string) { warnings = append(warnings, message) },
	})
	if err != nil || result == "" {
		t.Fatalf("Execute() = %q, %v", result, err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one combined warning", warnings)
	}
	for _, want := range []string{"Date.now()", "new Date()", "Math.random()", "--resume", "prompt or schema"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning does not mention %q: %s", want, warnings[0])
		}
	}
}

func TestExecuteDoesNotWarnWithoutResumeUnstableAPIs(t *testing.T) {
	for _, script := range []string{
		`return args;`,
		`const Mathrandomizer = () => 0.5; return Mathrandomizer();`,
		`const myDateNow = () => 1; return myDateNow();`,
	} {
		t.Run(script, func(t *testing.T) {
			var warnings []string
			_, err := Execute(context.Background(), Options{
				ScriptSrc: script,
				Store:     &profile.Store{},
				WorkDir:   t.TempDir(),
				OnWarning: func(message string) { warnings = append(warnings, message) },
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if len(warnings) != 0 {
				t.Fatalf("warnings = %#v, want none", warnings)
			}
		})
	}
}

func TestCacheStatsTrackHitsAgainstPriorJournaledCalls(t *testing.T) {
	key := callKey("mock", "prompt", "")
	cache := NewCache([]runstore.JournalEntry{
		{Key: key, Result: "cached"},
		{Key: "failed", Error: "failed"},
		{Key: "isolated", Result: "changed", Dir: "/tmp/worktree"},
	})
	if stats := cache.Stats(); stats != (CacheStats{PriorCalls: 3}) {
		t.Fatalf("initial Stats() = %#v", stats)
	}
	if result, ok := cache.pop(key); !ok || result != "cached" {
		t.Fatalf("pop() = %#v, %t", result, ok)
	}
	if stats := cache.Stats(); stats != (CacheStats{Hits: 1, PriorCalls: 3}) {
		t.Fatalf("final Stats() = %#v", stats)
	}
}
