package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
)

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

func TestPiCmdMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cmd := piCmd()
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
	combined := string(piExtensionTS) + piOrchestrationPrompt + guideMD
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
	cmd := newRootCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	args := []string{"pi"}
	if raw := os.Getenv("GO_PI_COMMAND_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		os.Exit(commandExitCode(err))
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

func TestPiExtensionRegistersModelVisibleSteeringTool(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`import { Type } from "@earendil-works/pi-ai"`,
		`pi.registerTool({`,
		`name: "dyna_steer"`,
		`run_id: Type.String`,
		`agent_id: Type.Integer`,
		`message: Type.String`,
		`maxLength: 2000`,
		`const run = await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id))`,
		`["runs", "steer", params.run_id, String(params.agent_id), params.message]`,
		`never starts a replacement`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("pi steering tool contract is missing %q", required)
		}
	}
	if strings.Contains(source, `execute(_toolCallId, params) { return pi.sendUserMessage`) {
		t.Fatal("pi steering tool delegates to prose instead of invoking the command boundary")
	}
}

func TestPiExtensionRegistersNativeWorkflowTools(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`const ROOT_AGENT = "dyna-orchestrator"`,
		`const ACTIVATE_ALL_TOOLS = process.env.DYNA_PI_ACTIVATE_ALL_TOOLS === "1"`,
		`pi.setActiveTools(pi.getAllTools().map((tool) => tool.name));`,
		"ctx.ui.setStatus(\"dyna-agent\", `agent:${ROOT_AGENT}`);",
		`name: "dyna_profiles"`,
		`name: "dyna_run"`,
		`name: "dyna_runs"`,
		`workflow_path: Type.String`,
		`args: Type.Optional(Type.Unknown`,
		`max_concurrent: Type.Optional(Type.Integer`,
		`call_cap: Type.Optional(Type.Integer`,
		`async function consumeWorkflow(workflowPath: string)`,
		`await rename(sourcePath, scriptPath)`,
		`const cliArgs = ["run", scriptPath, "--json", "--detach"]`,
		`await rm(tempDir, { recursive: true, force: true })`,
		`execFile(DYNA, args`,
		`ctx.sessionManager.getSessionId()`,
		`const session = sessionID(ctx)`,
		`sessionEnv(session)`,
		`await requireSessionRun(params.resume, session, signal)`,
		`await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id))`,
		`DETACHED_REGISTRATION_GRACE_MS = 15 * 1000`,
		`Type.Literal("cancel")`,
		`redactSecrets`,
		`["runs", "list", "--json", "--session", session]`,
		`checkedString(params.message, "message", 2000)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi native workflow tool contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{`name: "dyna_guide"`, `exec(`, `shell: true`, `workflow: Type.String`, `detach: Type.Optional`, `if (!SESSION) return`, `await waitForSessionRunRegistration(runID, signal)`} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Pi native workflow tools contain forbidden implementation %q", forbidden)
		}
	}
}

func TestPiDynaDashboardHandoffStaticContract(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`import { execFile, spawn } from "node:child_process"`,
		`description: "Open the Dyna dashboard for this Pi session"`,
		`const session = sessionID(ctx);`,
		`await ctx.ui.custom<void>`,
		`tui.stop();`,
		`process.stdout.write("\x1b[2J\x1b[H");`,
		`const child = spawn(DYNA, ["tui", "--session", session], {`,
		`stdio: "inherit"`,
		`env: process.env`,
		`child.once("error", (error) => finish(error.message));`,
		`child.once("close", (code, signal) => {`,
		`tui.start();`,
		`tui.requestRender(true);`,
		`done(undefined);`,
		`Unable to run Dyna dashboard`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi Dyna dashboard handoff contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`class DynaRunsOverlay`,
		`type ObserverFocus`,
		`readAgentJournal`,
		`width: "92%"`,
		`@earendil-works/pi-tui`,
		`spawnSync`,
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Pi Dyna dashboard handoff retains obsolete observer behavior %q", forbidden)
		}
	}
}

func TestInstalledPiDynaDashboardHandoffLifecycle(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	extensionJSON, err := json.Marshal(extensionPath)
	if err != nil {
		t.Fatal(err)
	}

	runProbe := func(t *testing.T, dynaBin string) (map[string]any, []byte, []byte) {
		t.Helper()
		marker := filepath.Join(t.TempDir(), "handoff.json")
		probePath := filepath.Join(t.TempDir(), "probe.ts")
		probeSource := `import { writeFile } from "node:fs/promises";
import dynaExtension from ` + string(extensionJSON) + `;

let command: any;
dynaExtension({
	on: () => {},
	registerTool: () => {},
	registerCommand: (_name: string, spec: any) => { command = spec; },
} as any);

export default function (pi: any) {
	pi.on("input", async () => {
		const events: string[] = [];
		const notifications: Array<{ message: string; type: string }> = [];
		const tui = {
			stop: () => events.push("stop"),
			start: () => events.push("start"),
			requestRender: (full: boolean) => events.push("render:" + full),
		};
		const context = {
			mode: "tui",
			sessionManager: { getSessionId: () => "fixture-persisted-session" },
			ui: {
				notify: (message: string, type: string) => notifications.push({ message, type }),
				custom: async (factory: any) => await new Promise((resolve, reject) => {
					try {
						factory(tui, {}, {}, (result: unknown) => {
							events.push("done");
							resolve(result);
						});
					} catch (error) {
						reject(error);
					}
				}),
			},
		};
		try {
			await command.handler("", context);
			await writeFile(process.env.PI_HANDOFF_MARKER!, JSON.stringify({ ok: true, events, notifications }));
		} catch (error) {
			await writeFile(process.env.PI_HANDOFF_MARKER!, JSON.stringify({ ok: false, events, notifications, error: error instanceof Error ? error.message : String(error) }));
		}
		return { action: "handled" };
	});
}
`
		if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, piPath,
			"--offline", "--no-extensions",
			"--extension", probePath,
			"--provider", "openai-codex",
			"--model", "gpt-5.6-terra",
			"--api-key", "fixture-only",
			"-p", "exercise dashboard handoff",
		)
		cmd.Env = os.Environ()
		for key, value := range map[string]string{
			"PI_OFFLINE":          "1",
			"DYNA_BIN":            dynaBin,
			"DYNA_PI_CODEX_AUTH":  "0",
			"PI_HANDOFF_MARKER":   marker,
			"PI_HANDOFF_SENTINEL": "inherited-environment",
			"OPENAI_API_KEY":      "",
			"DYNA_NO_AUTO_UPDATE": "1",
		} {
			cmd.Env = setEnv(cmd.Env, key, value)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatalf("offline Pi dashboard handoff timed out: %v", ctx.Err())
		}
		if err != nil {
			t.Fatalf("offline Pi dashboard handoff: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.Bytes(), stderr.Bytes())
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(readFile(t, marker)), &probe); err != nil {
			t.Fatal(err)
		}
		return probe, stdout.Bytes(), stderr.Bytes()
	}

	t.Run("success", func(t *testing.T) {
		fixtureDir := t.TempDir()
		argsPath := filepath.Join(fixtureDir, "args")
		envPath := filepath.Join(fixtureDir, "env")
		fakeDyna := filepath.Join(fixtureDir, "exact-dyna")
		writeExecutable(t, fakeDyna, `#!/bin/sh
printf '%s\0' "$@" > "$PI_HANDOFF_ARGS"
printf '%s\n' "$PI_HANDOFF_SENTINEL" > "$PI_HANDOFF_ENV"
printf '%s\n' 'handoff-stdout'
printf '%s\n' 'handoff-stderr' >&2
`)
		t.Setenv("PI_HANDOFF_ARGS", argsPath)
		t.Setenv("PI_HANDOFF_ENV", envPath)

		probe, stdout, stderr := runProbe(t, fakeDyna)
		if probe["ok"] != true {
			t.Fatalf("successful handoff probe = %#v", probe)
		}
		if got := strings.Join(anyStrings(t, probe["events"]), ","); got != "stop,start,render:true,done" {
			t.Fatalf("successful handoff lifecycle = %q", got)
		}
		if notifications := probe["notifications"].([]any); len(notifications) != 0 {
			t.Fatalf("successful handoff notifications = %#v", notifications)
		}
		if got := readNULArgs(t, argsPath); !slices.Equal(got, []string{"tui", "--session", "fixture-persisted-session"}) {
			t.Fatalf("dashboard argv = %#v, want session-scoped TUI args", got)
		}
		if got := strings.TrimSpace(readFile(t, envPath)); got != "inherited-environment" {
			t.Fatalf("dashboard environment = %q", got)
		}
		if !bytes.Contains(stdout, []byte("handoff-stdout")) || !bytes.Contains(stderr, []byte("handoff-stderr")) {
			t.Fatalf("dashboard did not inherit streams: stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("spawn failure", func(t *testing.T) {
		probe, _, _ := runProbe(t, filepath.Join(t.TempDir(), "missing-dyna"))
		if probe["ok"] != true {
			t.Fatalf("failed-spawn handoff probe = %#v", probe)
		}
		if got := strings.Join(anyStrings(t, probe["events"]), ","); got != "stop,start,render:true,done" {
			t.Fatalf("failed-spawn handoff lifecycle = %q", got)
		}
		notifications := probe["notifications"].([]any)
		if len(notifications) != 1 {
			t.Fatalf("failed-spawn handoff notifications = %#v", notifications)
		}
		notification := notifications[0].(map[string]any)
		if notification["type"] != "error" || !strings.Contains(notification["message"].(string), "Unable to run Dyna dashboard") {
			t.Fatalf("failed-spawn handoff notification = %#v", notification)
		}
	})
}

func TestPiDynaTerminalDeliveryStaticContract(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`const launchedRunIDs = new Set<string>();`,
		`const pendingRunUpdates = new Map<string, TerminalRunUpdate>();`,
		`function watchLaunchedRun(id: string, session: string, ctx: ExtensionContext): void`,
		`run.status === "ok" || run.status === "error" || run.status === "canceled"`,
		`customType: "dyna_run_terminal"`,
		`display: false`,
		`{ triggerTurn: false }`,
		`ctx.ui.notify(completionMessage(run), completionNotificationType(run));`,
		`pi.on("agent_settled"`,
		`watchLaunchedRun(runID, session, ctx);`,
		`runCompletionAbort.abort()`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi terminal delivery contract is missing %q", required)
		}
	}
}

func TestInstalledPiDeliversDetachedTerminalUpdates(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}

	fixtureDir := t.TempDir()
	fakeDyna := filepath.Join(fixtureDir, "dyna")
	runCount := filepath.Join(fixtureDir, "run-count")
	statusFile := filepath.Join(fixtureDir, "status")
	launchSession := filepath.Join(fixtureDir, "launch-session")
	writeExecutable(t, fakeDyna, `#!/bin/sh
case "$1:$2" in
	run:*)
		printf '%s\n' "$DYNA_SESSION" > "$PI_LAUNCH_SESSION"
		count=0
		if [ -f "$PI_RUN_COUNT" ]; then count=$(cat "$PI_RUN_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_RUN_COUNT"
		printf 'wf_fixture_%s\n' "$count"
		;;
	runs:list)
		count=$(cat "$PI_RUN_COUNT")
		status=$(cat "$PI_STATUS_FILE")
		printf '[{"id":"wf_fixture_%s","name":"fixture","status":"%s","session":"fixture-session","startedAt":"2026-07-14T00:00:00Z"}]\n' "$count" "$status"
		;;
	*)
		printf 'unexpected fixture invocation: %s\n' "$*" >&2
		exit 98
		;;
esac
`)

	marker := filepath.Join(fixtureDir, "updates.json")
	extensionJSON, err := json.Marshal(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(fixtureDir, "probe.ts")
	probeSource := `import { readFile, writeFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";
import dynaExtension from ` + string(extensionJSON) + `;

const tools = new Map<string, any>();
const messages: any[] = [];
const notifications: any[] = [];
dynaExtension({
	on: () => {},
	registerTool: (tool: any) => tools.set(tool.name, tool),
	registerCommand: () => {},
	sendMessage: (message: any, options: any) => messages.push({ message, options }),
} as any);

async function waitForUpdates(count: number): Promise<void> {
	for (let attempt = 0; attempt < 150; attempt++) {
		if (messages.length === count && notifications.length === count) return;
		await sleep(20);
	}
	throw new Error("timed out waiting for terminal update");
}

export default function (pi: any) {
	pi.on("input", async () => {
		try {
			const context = {
				isIdle: () => true,
				sessionManager: { getSessionId: () => "fixture-session" },
				ui: { notify: (message: string, type: string) => notifications.push({ message, type }) },
			};
			const signal = new AbortController().signal;
			const run = tools.get("dyna_run");
			for (const status of ["ok", "error", "canceled"]) {
				const workflowPath = "/tmp/dyna-workflow-fixture-" + status + ".js";
				await writeFile(workflowPath, "return 1");
				await writeFile(process.env.PI_STATUS_FILE!, "running");
				await run.execute("run-" + status, { workflow_path: workflowPath }, signal, undefined, context);
				await writeFile(process.env.PI_STATUS_FILE!, status);
				await waitForUpdates(messages.length + 1);
			}
			await writeFile(process.env.PI_UPDATE_PROBE_MARKER!, JSON.stringify({ ok: true, messages, notifications }));
		} catch (error) {
			await writeFile(process.env.PI_UPDATE_PROBE_MARKER!, JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
		}
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "exercise terminal updates",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"PI_OFFLINE":             "1",
		"DYNA_SESSION":           "fixture-session",
		"DYNA_BIN":               fakeDyna,
		"DYNA_PI_CODEX_AUTH":     "0",
		"PI_RUN_COUNT":           runCount,
		"PI_STATUS_FILE":         statusFile,
		"PI_LAUNCH_SESSION":      launchSession,
		"PI_UPDATE_PROBE_MARKER": marker,
		"DYNA_NO_AUTO_UPDATE":    "1",
		"OPENAI_API_KEY":         "",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi terminal-update probe timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi terminal-update probe: %v\n%s", err, output)
	}

	var probe struct {
		OK       bool `json:"ok"`
		Messages []struct {
			Message struct {
				Details struct {
					Status string `json:"status"`
				} `json:"details"`
			} `json:"message"`
			Options struct {
				TriggerTurn bool `json:"triggerTurn"`
			} `json:"options"`
		}
		Notifications []struct {
			Type string `json:"type"`
		}
	}
	if err := json.Unmarshal([]byte(readFile(t, marker)), &probe); err != nil {
		t.Fatal(err)
	}
	if !probe.OK || len(probe.Messages) != 3 || len(probe.Notifications) != 3 {
		t.Fatalf("terminal-update probe = %#v", probe)
	}
	if got := strings.TrimSpace(readFile(t, launchSession)); got != "fixture-session" {
		t.Fatalf("detached Dyna run session = %q, want persisted Pi session", got)
	}
	for i, want := range []struct {
		status, notification string
	}{{"ok", "info"}, {"error", "error"}, {"canceled", "warning"}} {
		if probe.Messages[i].Message.Details.Status != want.status || probe.Messages[i].Options.TriggerTurn {
			t.Fatalf("terminal message %d = %#v, want %s without a turn", i, probe.Messages[i], want.status)
		}
		if probe.Notifications[i].Type != want.notification {
			t.Fatalf("terminal notification %d = %#v, want %s", i, probe.Notifications[i], want.notification)
		}
	}
}

func TestInstalledPiLoadsNativeWorkflowToolSchemasOffline(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "tools.json")
	probePath := filepath.Join(t.TempDir(), "probe.ts")
	probeSource := `import { writeFile } from "node:fs/promises";
export default function (pi: any) {
	pi.on("input", async () => {
		const wanted = new Set(["dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer"]);
		const tools = pi.getAllTools()
			.filter((tool: any) => wanted.has(tool.name) || tool.name === "dyna_guide")
			.map((tool: any) => ({ name: tool.name, description: tool.description, parameters: tool.parameters }));
		await writeFile(process.env.TOOL_PROBE_MARKER, JSON.stringify(tools));
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "probe native tools",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"HOME":                home,
		"PI_CODING_AGENT_DIR": filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE":          "1",
		"DYNA_SESSION":        "fixture-session",
		"DYNA_BIN":            "/bin/false",
		"DYNA_PI_CODEX_AUTH":  "0",
		"TOOL_PROBE_MARKER":   marker,
		"OPENAI_API_KEY":      "",
		"CODEX_HOME":          filepath.Join(home, ".codex-fixture"),
		"DYNA_CODEX_BIN":      "/bin/false",
		"DYNA_NO_AUTO_UPDATE": "1",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi tool registration timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi tool registration: %v\n%s", err, output)
	}
	var tools []struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(readFile(t, marker)), &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("native Pi tools = %#v", tools)
	}
	byName := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		if tool.Name == "dyna_guide" {
			t.Fatal("dyna_guide was registered")
		}
		byName[tool.Name] = tool.Parameters
	}
	for _, name := range []string{"dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer"} {
		if byName[name] == nil {
			t.Errorf("installed Pi did not load %s", name)
		}
	}
	runProperties, _ := byName["dyna_run"]["properties"].(map[string]any)
	for _, field := range []string{"workflow_path", "cwd", "args", "name", "resume", "max_concurrent", "call_cap"} {
		if runProperties[field] == nil {
			t.Errorf("installed dyna_run schema is missing %s", field)
		}
	}

	missingSessionMarker := filepath.Join(t.TempDir(), "tools.json")
	missingCtx, cancelMissing := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelMissing()
	missing := exec.CommandContext(missingCtx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "probe tools without session",
	)
	missing.Env = cmd.Env
	missing.Env = setEnv(missing.Env, "DYNA_SESSION", "")
	missing.Env = setEnv(missing.Env, "TOOL_PROBE_MARKER", missingSessionMarker)
	if output, err := missing.CombinedOutput(); err != nil {
		t.Fatalf("offline Pi missing-session registration: %v\n%s", err, output)
	}
	var withoutSession []any
	if err := json.Unmarshal([]byte(readFile(t, missingSessionMarker)), &withoutSession); err != nil {
		t.Fatal(err)
	}
	if len(withoutSession) != 4 {
		t.Fatalf("Pi tools must derive ownership from SessionManager, not DYNA_SESSION: %#v", withoutSession)
	}
}

func TestInstalledPiActivatesRootPresetToolsWithoutOverridingExplicitControls(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(t.TempDir(), "active-tools.ts")
	probeSource := `import { writeFile } from "node:fs/promises";
import { Type } from "@earendil-works/pi-ai";
export default function (pi: any) {
	pi.registerTool({
		name: "fixture_extension_tool",
		description: "Fixture extension tool",
		parameters: Type.Object({}),
		async execute() { return { content: [{ type: "text", text: "fixture" }] }; },
	});
	pi.on("session_start", async () => {
		await writeFile(process.env.TOOL_PROBE_MARKER!, JSON.stringify({
			active: pi.getActiveTools(),
			all: pi.getAllTools().map((tool: any) => tool.name),
		}));
	});
	pi.on("input", async () => ({ action: "handled" }));
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	runProbe := func(t *testing.T, activeAll bool, args ...string) struct {
		Active []string `json:"active"`
		All    []string `json:"all"`
	} {
		t.Helper()
		marker := filepath.Join(t.TempDir(), "active-tools.json")
		piArgs := []string{
			"--offline", "--no-extensions",
			"--extension", extensionPath,
			"--extension", probePath,
			"--provider", "openai-codex",
			"--model", "gpt-5.6-terra",
			"--api-key", "fixture-only",
		}
		piArgs = append(piArgs, args...)
		piArgs = append(piArgs, "-p", "probe active tools")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, piPath, piArgs...)
		cmd.Env = os.Environ()
		for key, value := range map[string]string{
			"HOME":                       home,
			"PI_CODING_AGENT_DIR":        filepath.Join(home, ".pi-fixture"),
			"PI_OFFLINE":                 "1",
			"DYNA_SESSION":               "fixture-session",
			"DYNA_BIN":                   "/bin/false",
			"DYNA_PI_CODEX_AUTH":         "0",
			"TOOL_PROBE_MARKER":          marker,
			"OPENAI_API_KEY":             "",
			"CODEX_HOME":                 filepath.Join(home, ".codex-fixture"),
			"DYNA_CODEX_BIN":             "/bin/false",
			"DYNA_NO_AUTO_UPDATE":        "1",
			"DYNA_PI_ACTIVATE_ALL_TOOLS": map[bool]string{true: "1", false: ""}[activeAll],
		} {
			cmd.Env = setEnv(cmd.Env, key, value)
		}
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("offline Pi active-tool probe timed out: %v", ctx.Err())
		}
		if err != nil {
			t.Fatalf("offline Pi active-tool probe: %v\n%s", err, output)
		}
		var result struct {
			Active []string `json:"active"`
			All    []string `json:"all"`
		}
		if err := json.Unmarshal([]byte(readFile(t, marker)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	defaultTools := runProbe(t, true)
	for _, name := range []string{"read", "bash", "edit", "write", "grep", "find", "ls", "dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer", "fixture_extension_tool"} {
		if !slices.Contains(defaultTools.Active, name) {
			t.Errorf("default root preset active tools = %#v, missing %s", defaultTools.Active, name)
		}
	}
	if !slices.Contains(defaultTools.All, "fixture_extension_tool") {
		t.Fatalf("root preset did not register fixture extension tool: %#v", defaultTools.All)
	}

	restrictedTools := runProbe(t, false, "--tools", "read")
	if strings.Join(restrictedTools.Active, ",") != "read" {
		t.Fatalf("explicit --tools selection was overridden: %#v", restrictedTools.Active)
	}
}

func TestInstalledPiExecutesNativeToolsWithRegistrationGraceAndRedaction(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}

	fixtureDir := t.TempDir()
	fakeDyna := filepath.Join(fixtureDir, "dyna")
	listCount := filepath.Join(fixtureDir, "list-count")
	runCount := filepath.Join(fixtureDir, "run-count")
	secret := "fixture-success-secret"
	writeExecutable(t, fakeDyna, `#!/bin/sh
case "$1:$2" in
	run:*)
		count=0
		if [ -f "$PI_RUN_COUNT" ]; then count=$(cat "$PI_RUN_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_RUN_COUNT"
		if [ "$count" -eq 1 ]; then
			printf '%s\n' 'wf_fixture_detached'
		else
			printf 'malformed-%s\n' "$PI_RUNTIME_SECRET"
		fi
		;;
	runs:list)
		if [ "$3" != "--json" ] || [ "$4" != "--session" ] || [ -z "$5" ]; then
			printf '%s\n' 'missing exact session filter' >&2
			exit 97
		fi
		count=0
		if [ -f "$PI_LIST_COUNT" ]; then count=$(cat "$PI_LIST_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_LIST_COUNT"
		printf '[{"id":"wf_fixture_detached","name":"owned","status":"running","session":"%s","startedAt":"2026-07-14T00:00:00Z"}]\n' "$5"
		;;
	runs:show)
		printf '{"output":"%s"}\n' "$PI_RUNTIME_SECRET"
		;;
	runs:steer)
		printf 'queued %s\n' "$PI_RUNTIME_SECRET"
		;;
	*)
		printf 'unexpected fixture invocation: %s\n' "$*" >&2
		exit 98
		;;
esac
`)

	marker := filepath.Join(fixtureDir, "probe.json")
	probePath := filepath.Join(fixtureDir, "probe.ts")
	extensionJSON, err := json.Marshal(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	probeSource := `import { readFile, writeFile } from "node:fs/promises";
import dynaExtension from ` + string(extensionJSON) + `;

const tools = new Map<string, any>();
dynaExtension({
	on: () => {},
	registerTool: (tool: any) => tools.set(tool.name, tool),
	registerCommand: () => {},
} as any);

export default function (pi: any) {
	pi.on("input", async (_event: any, ctx: any) => {
		try {
			const signal = new AbortController().signal;
			const firstPath = "/tmp/dyna-workflow-runtime-first.js";
			await writeFile(firstPath, "return 1");
			const run = await tools.get("dyna_run").execute("run-call", { workflow_path: firstPath }, signal, undefined, ctx);
			const show = await tools.get("dyna_runs").execute("show-call", { action: "show", run_id: "wf_fixture_detached" }, signal, undefined, ctx);
			const steer = await tools.get("dyna_steer").execute("steer-call", { run_id: "wf_fixture_detached", agent_id: 1, message: "continue" }, signal, undefined, ctx);
			let malformedError = "";
			try {
				const secondPath = "/tmp/dyna-workflow-runtime-second.js";
				await writeFile(secondPath, "return 2");
				await tools.get("dyna_run").execute("bad-run-call", { workflow_path: secondPath }, signal, undefined, ctx);
			} catch (error) {
				malformedError = error instanceof Error ? error.message : String(error);
			}
			await writeFile(process.env.PI_TOOL_PROBE_MARKER, JSON.stringify({ ok: true, run, show, steer, malformedError }));
		} catch (error) {
			await writeFile(process.env.PI_TOOL_PROBE_MARKER, JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
		}
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "exercise native tools",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"HOME":                 home,
		"PI_CODING_AGENT_DIR":  filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE":           "1",
		"DYNA_SESSION":         "fixture-session",
		"DYNA_BIN":             fakeDyna,
		"DYNA_PI_CODEX_AUTH":   "0",
		"PI_LIST_COUNT":        listCount,
		"PI_RUN_COUNT":         runCount,
		"PI_RUNTIME_SECRET":    secret,
		"PI_TOOL_PROBE_MARKER": marker,
		"OPENAI_API_KEY":       "",
		"CODEX_HOME":           filepath.Join(home, ".codex-fixture"),
		"DYNA_CODEX_BIN":       "/bin/false",
		"DYNA_NO_AUTO_UPDATE":  "1",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi native tool execution timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi native tool execution: %v\n%s", err, output)
	}

	raw := readFile(t, marker)
	if strings.Contains(raw, secret) || bytes.Contains(output, []byte(secret)) {
		t.Fatal("successful native tool output exposed the fixture secret")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatal(err)
	}
	if probe["ok"] != true {
		t.Fatalf("native tool runtime probe failed: %s", raw)
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("native tool runtime did not redact successful output: %s", raw)
	}
	run, _ := probe["run"].(map[string]any)
	runDetails, _ := run["details"].(map[string]any)
	if runDetails["runId"] != "wf_fixture_detached" || runDetails["detached"] != true {
		t.Fatalf("detached run details = %#v", runDetails)
	}
	show, _ := probe["show"].(map[string]any)
	showDetails, _ := show["details"].(map[string]any)
	if showDetails["action"] != "show" || showDetails["runId"] != "wf_fixture_detached" || showDetails["priorStatus"] != "running" {
		t.Fatalf("show details = %#v", showDetails)
	}
	if showDetails["stdoutTruncated"] != false || showDetails["stderrTruncated"] != false {
		t.Fatalf("show truncation flags = stdout:%v stderr:%v", showDetails["stdoutTruncated"], showDetails["stderrTruncated"])
	}
	if malformed, _ := probe["malformedError"].(string); !strings.Contains(malformed, "invalid run ID") || !strings.Contains(malformed, "[REDACTED]") {
		t.Fatalf("malformed detached ID error = %q", malformed)
	}
}

func TestPiExtensionReusesCodexAuthWithoutRegisteringProvider(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`["app-server", "--listen", "stdio://"]`,
		`method: "account/read"`,
		`params: { refreshToken }`,
		`join(home, "auth.json")`,
		`auth.auth_mode !== "chatgpt"`,
		`ctx.modelRegistry.authStorage.setRuntimeApiKey(CODEX_PROVIDER, access.token)`,
		`ctx.modelRegistry.authStorage.removeRuntimeApiKey(CODEX_PROVIDER)`,
		`CODEX_REFRESH_MARGIN_MS`,
		`process.stderr.write`,
		`process.exitCode = 1`,
		`pi.on("input"`,
		`return { action: "handled" as const }`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("pi Codex auth contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`pi.registerProvider(`,
		`refresh_token`,
		`OPENAI_API_KEY`,
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("pi Codex auth bridge contains forbidden credential/provider handling %q", forbidden)
		}
	}
}

func TestPiExtensionLoadsCodexFixtureIntoRuntimeAuth(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	codexHome := filepath.Join(home, ".codex-fixture")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fixture-account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	fixtureToken := encode([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + encode(payload) + ".fixture"
	auth, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"access_token": fixtureToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "codex")
	argsCapture := filepath.Join(t.TempDir(), "codex-args")
	codexHomeJSON, err := json.Marshal(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	fakeCodexScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" > "$FAKE_CODEX_ARGS"
while IFS= read -r line; do
	case "$line" in
		*'"method":"initialize"'*) printf '%%s\n' '{"id":1,"result":{"codexHome":%s,"userAgent":"fixture"}}' ;;
		*'"method":"account/read"'*) printf '%%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"},"requiresOpenaiAuth":true}}' ;;
	esac
done
`, codexHomeJSON)
	writeExecutable(t, fakeCodex, fakeCodexScript)

	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	probeMarker := filepath.Join(t.TempDir(), "probe")
	probePath := filepath.Join(t.TempDir(), "probe.ts")
	probeSource := `import { writeFile } from "node:fs/promises";
export default function (pi: any) {
	pi.on("input", async (_event: unknown, ctx: any) => {
		const token = await ctx.modelRegistry.getApiKeyForProvider("openai-codex");
		await writeFile(process.env.AUTH_PROBE_MARKER, token === process.env.AUTH_PROBE_TOKEN ? "ok" : "wrong");
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--thinking", "xhigh",
		"-p", "probe auth",
	)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PI_CODING_AGENT_DIR="+filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE=1",
		"DYNA_SESSION=fixture-session",
		"DYNA_BIN=/bin/false",
		"DYNA_PI_CODEX_AUTH=1",
		"DYNA_CODEX_BIN="+fakeCodex,
		"FAKE_CODEX_ARGS="+argsCapture,
		"AUTH_PROBE_MARKER="+probeMarker,
		"AUTH_PROBE_TOKEN="+fixtureToken,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline pi extension contract timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline pi extension contract: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte(fixtureToken)) {
		t.Fatal("offline pi extension contract exposed its fixture token")
	}
	if got := strings.TrimSpace(readFile(t, probeMarker)); got != "ok" {
		t.Fatalf("runtime auth probe = %q, want ok", got)
	}
	if got := strings.TrimSpace(readFile(t, argsCapture)); got != "app-server --listen stdio://" {
		t.Fatalf("codex invocation = %q", got)
	}
}

func TestPiExtensionPrintModeFailsForUnusableCodexAuth(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	tests := []struct {
		name       string
		auth       string
		secret     string
		wantStderr string
	}{
		{
			name:       "missing",
			wantStderr: "Codex ChatGPT credentials are not available in its supported file store. Run `codex login` and retry.",
		},
		{
			name:       "invalid",
			auth:       `{"auth_mode":"chatgpt","tokens":{"access_token":"fixture-secret-not-jwt"}}`,
			secret:     "fixture-secret-not-jwt",
			wantStderr: "Codex uses an unsupported access-token format. Update Codex or run `codex login` and retry.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			data := t.TempDir()
			codexHome := filepath.Join(home, ".codex-fixture")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if tt.auth != "" {
				if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(tt.auth), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			codexHomeJSON, err := json.Marshal(codexHome)
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			fakeCodex := filepath.Join(binDir, "codex")
			writeExecutable(t, fakeCodex, fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
	case "$line" in
		*'"method":"initialize"'*) printf '%%s\n' '{"id":1,"result":{"codexHome":%s,"userAgent":"fixture"}}' ;;
		*'"method":"account/read"'*) printf '%%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"},"requiresOpenaiAuth":true}}' ;;
	esac
done
`, codexHomeJSON))

			t.Setenv("XDG_DATA_HOME", data)
			extensionPath, err := provisionPiExtension()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, piPath,
				"--offline", "--no-extensions",
				"--extension", extensionPath,
				"--provider", "openai-codex",
				"--model", "gpt-5.6-terra",
				"-p", "probe unusable auth",
			)
			cmd.Env = os.Environ()
			for key, value := range map[string]string{
				"HOME":                home,
				"PI_CODING_AGENT_DIR": filepath.Join(home, ".pi-fixture"),
				"PI_OFFLINE":          "1",
				"DYNA_SESSION":        "fixture-session",
				"DYNA_BIN":            "/bin/false",
				"DYNA_PI_CODEX_AUTH":  "1",
				"DYNA_CODEX_BIN":      fakeCodex,
			} {
				cmd.Env = setEnv(cmd.Env, key, value)
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err = cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("offline pi unusable-auth contract timed out: %v", ctx.Err())
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
				t.Fatalf("offline pi unusable-auth exit = %v, stderr=%q", err, stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != tt.wantStderr {
				t.Fatalf("offline pi unusable-auth stderr = %q, want %q", got, tt.wantStderr)
			}
			if strings.Contains(stdout.String(), "credential") || strings.Contains(stdout.String(), "codex login") {
				t.Fatalf("offline pi duplicated its auth failure on stdout: %q", stdout.String())
			}
			if tt.secret != "" && (strings.Contains(stdout.String(), tt.secret) || strings.Contains(stderr.String(), tt.secret)) {
				t.Fatal("offline pi exposed its invalid fixture credential")
			}
		})
	}
}

func TestPiExtensionDropsCodexAuthAfterInFlightRefreshAndModelSwitch(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	codexHome := filepath.Join(home, ".codex-fixture")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"exp":                         time.Now().Add(605 * time.Second).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fixture-account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	fixtureToken := encode([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + encode(payload) + ".fixture"
	auth, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"access_token": fixtureToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "codex")
	refreshStarted := filepath.Join(t.TempDir(), "refresh-started")
	refreshFinished := filepath.Join(t.TempDir(), "refresh-finished")
	codexHomeJSON, err := json.Marshal(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, fakeCodex, fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
	case "$line" in
		*'"method":"initialize"'*) printf '%%s\n' '{"id":1,"result":{"codexHome":%s,"userAgent":"fixture"}}' ;;
		*'"refreshToken":true'*)
			: > "$FAKE_CODEX_REFRESH_STARTED"
			sleep 1
			: > "$FAKE_CODEX_REFRESH_FINISHED"
			printf '%%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"},"requiresOpenaiAuth":true}}'
			;;
		*'"method":"account/read"'*) printf '%%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"},"requiresOpenaiAuth":true}}' ;;
	esac
done
`, codexHomeJSON))

	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	probeMarker := filepath.Join(t.TempDir(), "probe")
	probePath := filepath.Join(t.TempDir(), "probe.ts")
	probeSource := `import { readFile, writeFile } from "node:fs/promises";

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(path: string): Promise<void> {
	const deadline = Date.now() + 10000;
	while (Date.now() < deadline) {
		try {
			await readFile(path);
			return;
		} catch {
			await delay(25);
		}
	}
	throw new Error("timed out waiting for fixture refresh");
}

export default function (pi: any) {
	pi.registerProvider("fixture-provider", {
		baseUrl: "http://127.0.0.1:1/v1",
		apiKey: "fixture-only",
		api: "openai-completions",
		models: [{
			id: "fixture-model",
			name: "Fixture Model",
			reasoning: false,
			input: ["text"],
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
			contextWindow: 1024,
			maxTokens: 128,
		}],
	});
	pi.on("input", async (_event: unknown, ctx: any) => {
		try {
			await waitFor(process.env.FAKE_CODEX_REFRESH_STARTED);
			const model = ctx.modelRegistry.find("fixture-provider", "fixture-model");
			if (!model || !await pi.setModel(model)) throw new Error("fixture model was not selectable");
			await waitFor(process.env.FAKE_CODEX_REFRESH_FINISHED);
			await delay(500);
			const token = await ctx.modelRegistry.authStorage.getApiKey("openai-codex", { includeFallback: false });
			await writeFile(process.env.AUTH_PROBE_MARKER, token === undefined ? "ok" : "stale");
		} catch (error) {
			await writeFile(process.env.AUTH_PROBE_MARKER, error instanceof Error ? error.message : String(error));
		}
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"-p", "probe auth lifecycle",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"HOME":                        home,
		"PI_CODING_AGENT_DIR":         filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE":                  "1",
		"DYNA_SESSION":                "fixture-session",
		"DYNA_BIN":                    "/bin/false",
		"DYNA_PI_CODEX_AUTH":          "1",
		"DYNA_CODEX_BIN":              fakeCodex,
		"FAKE_CODEX_REFRESH_STARTED":  refreshStarted,
		"FAKE_CODEX_REFRESH_FINISHED": refreshFinished,
		"AUTH_PROBE_MARKER":           probeMarker,
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline pi auth lifecycle contract timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline pi auth lifecycle contract: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte(fixtureToken)) {
		t.Fatal("offline pi auth lifecycle contract exposed its fixture token")
	}
	if got := strings.TrimSpace(readFile(t, probeMarker)); got != "ok" {
		t.Fatalf("runtime auth cleanup probe = %q, want ok", got)
	}
}

func TestRunsListSessionFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_session-one")
	t.Setenv(runstore.SessionEnv, "s1")
	first, err := runstore.Create("first", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Finish("ok", "1", nil)

	t.Setenv("DYNA_RUN_ID", "wf_session-none")
	t.Setenv(runstore.SessionEnv, "")
	second, err := runstore.Create("second", "return 2", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Finish("ok", "2", nil)

	cmd := runsCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"list", "--json", "--session", "s1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var runs []runstore.Meta
	if err := json.Unmarshal(stdout.Bytes(), &runs); err != nil {
		t.Fatalf("decode filtered list: %v\n%s", err, stdout.String())
	}
	if len(runs) != 1 || runs[0].ID != first.Meta.ID || runs[0].Session != "s1" {
		t.Fatalf("filtered runs = %#v", runs)
	}
}

func TestTuiCmdSessionFilterIsOptionalAndValidated(t *testing.T) {
	cmd := tuiCmd()
	flag := cmd.Flags().Lookup("session")
	if flag == nil || flag.DefValue != "" {
		t.Fatalf("tui session flag = %#v, want optional empty default", flag)
	}

	cmd.SetArgs([]string{"--session", ""})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid session filter") {
		t.Fatalf("empty explicit TUI session error = %v", err)
	}

	cmd = tuiCmd()
	cmd.SetArgs([]string{"--session", strings.Repeat("s", 129)})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "maximum 128 bytes") {
		t.Fatalf("oversized TUI session error = %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
}

func readNULArgs(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\x00"), "\x00")
}

func anyStrings(t *testing.T, value any) []string {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want string array", value)
	}
	result := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("value[%d] = %#v, want string", i, value)
		}
		result[i] = text
	}
	return result
}

func containsArgs(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func piCommandSubprocess(t *testing.T, binDir string, args ...string) *exec.Cmd {
	t.Helper()
	rawArgs := ""
	if len(args) > 0 {
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		rawArgs = string(encoded)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPiCommandSubprocess$")
	cmd.Env = append(os.Environ(),
		"GO_PI_COMMAND_HELPER=1",
		"GO_PI_COMMAND_ARGS="+rawArgs,
		"PATH="+binDir,
		"HOME="+t.TempDir(),
		"XDG_DATA_HOME="+t.TempDir(),
		"DYNA_NO_AUTO_UPDATE=1",
	)
	return cmd
}
