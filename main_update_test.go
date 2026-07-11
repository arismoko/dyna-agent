package main

import (
	"bytes"
	"strings"
	"testing"

	selfupdate "dyna-agent/internal/update"
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

func TestPrintUpdateStatus(t *testing.T) {
	tests := []struct {
		name string
		in   selfupdate.Result
		want string
	}{
		{name: "none", in: selfupdate.Result{Current: "v1.0.0"}, want: "no stable GitHub release"},
		{name: "available", in: selfupdate.Result{Current: "v1.0.0", Latest: "v1.1.0", Available: true}, want: "v1.1.0 is available"},
		{name: "development", in: selfupdate.Result{Current: "dev+abc", Latest: "v1.1.0"}, want: "installed build: dev+abc"},
		{name: "current", in: selfupdate.Result{Current: "v1.1.0", Latest: "v1.1.0"}, want: "v1.1.0 is up to date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printUpdateStatus(&out, tt.in)
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output = %q, want substring %q", out.String(), tt.want)
			}
		})
	}
}

func TestUpdateConfigUsesDynaDataDir(t *testing.T) {
	previous := version
	version = "dev"
	t.Cleanup(func() { version = previous })
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	cfg := updateConfig()
	if got := cfg.StatePath; got != data+"/dyna/update-check.json" {
		t.Fatalf("state path = %q", got)
	}
	if cfg.Version != "dev" {
		t.Fatalf("source build update version = %q, want dev", cfg.Version)
	}
}
