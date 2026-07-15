package workflows

import (
	"strings"
	"testing"

	"dyna-agent/internal/engine"
)

func TestResumeCacheReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stats      engine.CacheStats
		want       string
		suspicious bool
	}{
		{name: "full replay", stats: engine.CacheStats{Hits: 4, PriorCalls: 4}, want: "4/4"},
		{name: "partial replay", stats: engine.CacheStats{Hits: 3, PriorCalls: 4}, want: "3/4"},
		{name: "low replay", stats: engine.CacheStats{Hits: 1, PriorCalls: 4}, want: "1/4", suspicious: true},
		{name: "empty prior run", stats: engine.CacheStats{}, want: "0/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, suspicious := resumeCacheReport(tc.stats)
			if suspicious != tc.suspicious || !strings.Contains(message, tc.want) {
				t.Fatalf("resumeCacheReport(%#v) = %q, %t", tc.stats, message, suspicious)
			}
			if strings.Contains(message, "warning:") != tc.suspicious {
				t.Fatalf("warning presence in %q does not match suspicious=%t", message, tc.suspicious)
			}
		})
	}
}
