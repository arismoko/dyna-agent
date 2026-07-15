package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootVersionFlagsAndCommand(t *testing.T) {
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })

	for _, args := range [][]string{{"--version"}, {"version"}} {
		cmd := newRootCommand()
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("dyna %v: %v", args, err)
		}
		if got := strings.TrimSpace(stdout.String()); got != "dyna v1.2.3" {
			t.Fatalf("dyna %v output = %q", args, got)
		}
	}
}
