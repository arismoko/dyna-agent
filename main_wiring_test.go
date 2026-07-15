package main

import "testing"

func TestPiCmdRegistered(t *testing.T) {
	cmd, _, err := newRootCommand().Find([]string{"pi"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Name() != "pi" {
		t.Fatalf("pi command = %#v", cmd)
	}
	if !cmd.DisableFlagParsing {
		t.Fatal("pi command parses passthrough flags")
	}
}
