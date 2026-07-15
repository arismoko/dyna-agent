package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/cli/interactive"
	"dyna-agent/internal/runstore"
)

func TestPiCmdMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cmd := NewCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "pi is not installed") {
		t.Fatalf("pi command error = %v", err)
	}
}

func TestPiCmdLaunchesWithExtensionSessionAndArgs(t *testing.T) {
	binDir := t.TempDir()
	home := t.TempDir()
	data := t.TempDir()
	capture := t.TempDir()
	piPath := filepath.Join(binDir, "pi")
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$DYNA_SESSION\" > \"$CAPTURE_SESSION\"\nprintf '%s\\n' \"$DYNA_BIN\" > \"$CAPTURE_BIN\"\nprintf '%s\\n' \"$DYNA_PI_CODEX_AUTH\" > \"$CAPTURE_AUTH\"\nprintf '%s\\n' \"$DYNA_PI_ACTIVATE_ALL_TOOLS\" > \"$CAPTURE_TOOLS\"\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := piCommandSubprocess(t, binDir, "pi", "--model", "test-model", "--system-prompt", "user prompt", "--append-system-prompt", "user addition", "--skill", "user-skill", "-c")
	for key, value := range map[string]string{
		"HOME":                home,
		"XDG_DATA_HOME":       data,
		"CAPTURE_ARGS":        filepath.Join(capture, "args"),
		"CAPTURE_SESSION":     filepath.Join(capture, "session"),
		"CAPTURE_BIN":         filepath.Join(capture, "bin"),
		"CAPTURE_AUTH":        filepath.Join(capture, "auth"),
		"CAPTURE_TOOLS":       filepath.Join(capture, "tools"),
		runstore.SessionEnv:   "stale-session",
		"DYNA_BIN":            "stale-binary",
		"DYNA_NO_AUTO_UPDATE": "1",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pi subprocess: %v\n%s", err, output)
	}

	args := readNULArgs(t, filepath.Join(capture, "args"))
	wantExtension := filepath.Join(data, "dyna", "pi-extension", "dyna.ts")
	wantArgs := []string{"--extension", wantExtension, "--append-system-prompt", piOrchestrationPrompt, "--thinking", "xhigh", "--name", piRootAgent, "--model", "test-model", "--system-prompt", "user prompt", "--append-system-prompt", "user addition", "--skill", "user-skill", "-c"}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("pi args = %#v, want %#v", args, wantArgs)
	}
	if session := strings.TrimSpace(readFile(t, filepath.Join(capture, "session"))); session != "" {
		t.Fatalf("dyna pi leaked process-scoped DYNA_SESSION = %q", session)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "bin"))); got == "" || got == "stale-binary" {
		t.Fatalf("DYNA_BIN = %q", got)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "auth"))); got != "1" {
		t.Fatalf("DYNA_PI_CODEX_AUTH = %q, want 1", got)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "tools"))); got != "1" {
		t.Fatalf("DYNA_PI_ACTIVATE_ALL_TOOLS = %q, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "skills", "dyna", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("dyna pi installed a redundant skill: %v", err)
	}
}

func TestPiCmdPreservesExplicitNameAndToolControls(t *testing.T) {
	for _, tt := range []struct {
		name        string
		args        []string
		want        []string
		activateAll bool
	}{
		{name: "long name", args: []string{"--name=fixture"}, want: []string{"--name", "fixture"}, activateAll: true},
		{name: "short name", args: []string{"-n=fixture"}, want: []string{"-n", "fixture"}, activateAll: true},
		{name: "tools equals", args: []string{"--tools=read,fixture_extension_tool"}, want: []string{"--tools", "read,fixture_extension_tool"}},
		{name: "short tools equals", args: []string{"-t=read"}, want: []string{"-t", "read"}},
		{name: "exclude tools equals", args: []string{"--exclude-tools=write"}, want: []string{"--exclude-tools", "write"}},
		{name: "short exclude tools equals", args: []string{"-xt=write"}, want: []string{"-xt", "write"}},
		{name: "no tools", args: []string{"--no-tools"}, want: []string{"--no-tools"}},
		{name: "short no tools", args: []string{"-nt"}, want: []string{"-nt"}},
		{name: "no builtin tools", args: []string{"--no-builtin-tools"}, want: []string{"--no-builtin-tools"}},
		{name: "short no builtin tools", args: []string{"-nbt"}, want: []string{"-nbt"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			capture := t.TempDir()
			writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"${DYNA_PI_ACTIVATE_ALL_TOOLS-unset}\" > \"$CAPTURE_TOOLS\"\n")
			cmd := piCommandSubprocess(t, binDir, append([]string{"pi"}, tt.args...)...)
			cmd.Env = setEnv(cmd.Env, "CAPTURE_ARGS", filepath.Join(capture, "args"))
			cmd.Env = setEnv(cmd.Env, "CAPTURE_TOOLS", filepath.Join(capture, "tools"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pi subprocess: %v\n%s", err, output)
			}
			got := readNULArgs(t, filepath.Join(capture, "args"))
			if !containsArgs(got, tt.want) {
				t.Fatalf("pi args = %#v, missing %#v", got, tt.want)
			}
			wantActivation := "unset"
			if tt.activateAll {
				wantActivation = "1"
			}
			if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "tools"))); got != wantActivation {
				t.Fatalf("DYNA_PI_ACTIVATE_ALL_TOOLS = %q, want %q", got, wantActivation)
			}
		})
	}
}

func TestPiCmdNormalizesRecognizedEqualsArgs(t *testing.T) {
	binDir := t.TempDir()
	data := t.TempDir()
	capture := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$DYNA_PI_CODEX_AUTH\" > \"$CAPTURE_AUTH\"\n")

	cmd := piCommandSubprocess(t, binDir,
		"pi", "--",
		"--provider=fixture-provider",
		"--model=fixture-model",
		"--models=fixture-provider/*:high",
		"--thinking=xhigh",
		"--api-key=fixture-only",
		"--system-prompt=preserve=exactly",
		"literal=value",
	)
	cmd.Env = setEnv(cmd.Env, "XDG_DATA_HOME", data)
	cmd.Env = setEnv(cmd.Env, "CAPTURE_ARGS", filepath.Join(capture, "args"))
	cmd.Env = setEnv(cmd.Env, "CAPTURE_AUTH", filepath.Join(capture, "auth"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pi subprocess: %v\n%s", err, output)
	}

	wantArgs := []string{
		"--extension", filepath.Join(data, "dyna", "pi-extension", "dyna.ts"),
		"--append-system-prompt", piOrchestrationPrompt,
		"--name", piRootAgent,
		"--provider", "fixture-provider",
		"--model", "fixture-model",
		"--models", "fixture-provider/*:high",
		"--thinking", "xhigh",
		"--api-key", "fixture-only",
		"--system-prompt=preserve=exactly",
		"literal=value",
	}
	if got := readNULArgs(t, filepath.Join(capture, "args")); strings.Join(got, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("pi args = %#v, want %#v", got, wantArgs)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "auth"))); got != "0" {
		t.Fatalf("DYNA_PI_CODEX_AUTH = %q, want 0", got)
	}
}

func TestPiDefaultArgsPreserveExplicitSelection(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "all defaults", want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra", "--thinking", "xhigh", "--name", piRootAgent}},
		{name: "model", args: []string{"--model", "anthropic/claude"}, want: []string{"--thinking", "xhigh", "--name", piRootAgent}},
		{name: "provider", args: []string{"--provider=anthropic"}, want: []string{"--thinking", "xhigh", "--name", piRootAgent}},
		{name: "model thinking suffix", args: []string{"--model", "openai-codex/gpt-5.6-terra:high"}, want: []string{"--name", piRootAgent}},
		{name: "model scope", args: []string{"--models", "anthropic/*"}, want: []string{"--thinking", "xhigh", "--name", piRootAgent}},
		{name: "model scope thinking suffix", args: []string{"--models", "sonnet:high,haiku:low"}, want: []string{"--name", piRootAgent}},
		{name: "thinking", args: []string{"--thinking", "low"}, want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra", "--name", piRootAgent}},
		{name: "equals forms", args: []string{"--model=other/model", "--thinking=xhigh"}, want: []string{"--name", piRootAgent}},
		{name: "last model wins", args: []string{"--model", "first:high", "--model", "second"}, want: []string{"--thinking", "xhigh", "--name", piRootAgent}},
		{name: "long name", args: []string{"--name", "fixture"}, want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra", "--thinking", "xhigh"}},
		{name: "short name equals", args: []string{"-n=fixture"}, want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra", "--thinking", "xhigh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := piDefaultArgs(tt.args)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("piDefaultArgs(%#v) = %#v, want %#v", tt.args, got, tt.want)
			}
		})
	}
}

func TestPiTerraUsesBundledCatalogMetadata(t *testing.T) {
	if piDefaultProvider != "openai-codex" || piDefaultModel != "gpt-5.6-terra" {
		t.Fatalf("Pi default = %s/%s", piDefaultProvider, piDefaultModel)
	}
	combined := string(piExtensionTS) + piOrchestrationPrompt + interactive.GuideMarkdown()
	for _, stale := range []string{"270000", "258000"} {
		if strings.Contains(combined, stale) {
			t.Errorf("Pi integration hard-codes stale Terra context value %s", stale)
		}
	}
	for _, privateOverride := range []string{"setCompactionReserveTokens", `registerProvider("openai-codex"`, "contextWindow: 372000"} {
		if strings.Contains(string(piExtensionTS), privateOverride) {
			t.Errorf("Pi extension mutates catalog/compaction internals with %q", privateOverride)
		}
	}
}

func TestPiCmdExplicitAPIKeyDisablesCodexAuthReuse(t *testing.T) {
	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "auth")
	writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nprintf '%s\\n' \"$DYNA_PI_CODEX_AUTH\" > \"$CAPTURE_AUTH\"\n")
	cmd := piCommandSubprocess(t, binDir, "pi", "--api-key", "fixture-only")
	cmd.Env = setEnv(cmd.Env, "CAPTURE_AUTH", capture)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pi subprocess: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(readFile(t, capture)); got != "0" {
		t.Fatalf("DYNA_PI_CODEX_AUTH = %q, want 0", got)
	}
}

func TestPiCommandSubprocess(t *testing.T) {
	if os.Getenv("GO_PI_COMMAND_HELPER") != "1" {
		return
	}
	cmd := NewCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	args := []string{}
	if raw := os.Getenv("GO_PI_COMMAND_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
	}
	if len(args) > 0 && args[0] == "pi" {
		args = args[1:]
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestProvisionPiExtension(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(piExtensionTS) || !strings.Contains(got, "export default") {
		t.Fatal("provisioned extension does not match the embedded TypeScript")
	}

	old := time.Unix(946684800, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionPiExtension(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("unchanged extension was rewritten: info=%v err=%v", info, err)
	}

	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionPiExtension(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != string(piExtensionTS) {
		t.Fatal("stale extension was not refreshed")
	}
}
