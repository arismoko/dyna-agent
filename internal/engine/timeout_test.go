package engine

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestClampAgentTimeoutHasThirtyMinuteMinimum(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "short call", in: 10 * time.Minute, want: 30 * time.Minute},
		{name: "minimum", in: 30 * time.Minute, want: 30 * time.Minute},
		{name: "long call", in: 2 * time.Hour, want: 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampAgentTimeout(tc.in); got != tc.want {
				t.Fatalf("clampAgentTimeout(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestExplicitShortAgentTimeoutIsClamped(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{{
		Name: "slow", Harness: profile.HarnessCustom,
		Command: []string{"/bin/sh", "-c", "sleep 0.1; printf finished"},
		Taste:   5, Intelligence: 5, Cost: 5, Default: true,
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := Execute(ctx, Options{
		ScriptSrc: `return await agent("finish", {profile: "slow", timeout: 0.01});`,
		Store:     store,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v; a sub-30-minute call timeout must be clamped", err)
	}
	if result != `"finished"` {
		t.Fatalf("Execute() = %s, want %q", result, "finished")
	}
}
