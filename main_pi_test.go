package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$DYNA_SESSION\" > \"$CAPTURE_SESSION\"\nprintf '%s\\n' \"$DYNA_BIN\" > \"$CAPTURE_BIN\"\nprintf '%s\\n' \"$DYNA_PI_CODEX_AUTH\" > \"$CAPTURE_AUTH\"\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("CAPTURE_ARGS", filepath.Join(capture, "args"))
	t.Setenv("CAPTURE_SESSION", filepath.Join(capture, "session"))
	t.Setenv("CAPTURE_BIN", filepath.Join(capture, "bin"))
	t.Setenv("CAPTURE_AUTH", filepath.Join(capture, "auth"))
	t.Setenv(runstore.SessionEnv, "stale-session")
	t.Setenv("DYNA_BIN", "stale-binary")
	t.Setenv("DYNA_NO_AUTO_UPDATE", "1")

	cmd := newRootCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"pi", "--model", "test-model", "--system-prompt", "user prompt", "--append-system-prompt", "user addition", "--skill", "user-skill", "-c"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	args := readNULArgs(t, filepath.Join(capture, "args"))
	wantExtension := filepath.Join(data, "dyna", "pi-extension", "dyna.ts")
	wantArgs := []string{"--extension", wantExtension, "--append-system-prompt", piOrchestrationPrompt, "--no-skills", "--thinking", "xhigh", "--model", "test-model", "--system-prompt", "user prompt", "--append-system-prompt", "user addition", "--skill", "user-skill", "-c"}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("pi args = %#v, want %#v", args, wantArgs)
	}
	session := strings.TrimSpace(readFile(t, filepath.Join(capture, "session")))
	if !regexp.MustCompile(`^pisess_[0-9a-f]{16}$`).MatchString(session) {
		t.Fatalf("DYNA_SESSION = %q", session)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "bin"))); got == "" || got == "stale-binary" {
		t.Fatalf("DYNA_BIN = %q", got)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(capture, "auth"))); got != "1" {
		t.Fatalf("DYNA_PI_CODEX_AUTH = %q, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "skills", "dyna", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("dyna pi installed a redundant skill: %v", err)
	}
}

func TestPiDefaultArgsPreserveExplicitSelection(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "all defaults", want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra", "--thinking", "xhigh"}},
		{name: "model", args: []string{"--model", "anthropic/claude"}, want: []string{"--thinking", "xhigh"}},
		{name: "provider", args: []string{"--provider=anthropic"}, want: []string{"--thinking", "xhigh"}},
		{name: "model thinking suffix", args: []string{"--model", "openai-codex/gpt-5.6-terra:high"}},
		{name: "model scope", args: []string{"--models", "anthropic/*"}, want: []string{"--thinking", "xhigh"}},
		{name: "model scope thinking suffix", args: []string{"--models", "sonnet:high,haiku:low"}},
		{name: "thinking", args: []string{"--thinking", "low"}, want: []string{"--provider", "openai-codex", "--model", "gpt-5.6-terra"}},
		{name: "equals forms", args: []string{"--model=other/model", "--thinking=xhigh"}},
		{name: "last model wins", args: []string{"--model", "first:high", "--model", "second"}, want: []string{"--thinking", "xhigh"}},
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

func TestPiCmdExplicitAPIKeyDisablesCodexAuthReuse(t *testing.T) {
	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "auth")
	writeExecutable(t, filepath.Join(binDir, "pi"), "#!/bin/sh\nprintf '%s\\n' \"$DYNA_PI_CODEX_AUTH\" > \"$CAPTURE_AUTH\"\n")
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CAPTURE_AUTH", capture)
	t.Setenv("DYNA_NO_AUTO_UPDATE", "1")

	cmd := newRootCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"pi", "--api-key", "fixture-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
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
	cmd.SetArgs([]string{"pi"})
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
		`candidate.id === params.run_id`,
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

func TestPiExtensionReusesCodexAuthWithoutRegisteringProvider(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`["app-server", "--listen", "stdio://"]`,
		`method: "account/read"`,
		`params: { refreshToken }`,
		`join(home, "auth.json")`,
		`auth.auth_mode !== "chatgpt"`,
		`ctx.modelRegistry.authStorage.setRuntimeApiKey(CODEX_PROVIDER, access.token)`,
		`CODEX_REFRESH_MARGIN_MS`,
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

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func piCommandSubprocess(t *testing.T, binDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPiCommandSubprocess$")
	cmd.Env = append(os.Environ(),
		"GO_PI_COMMAND_HELPER=1",
		"PATH="+binDir,
		"HOME="+t.TempDir(),
		"XDG_DATA_HOME="+t.TempDir(),
		"DYNA_NO_AUTO_UPDATE=1",
	)
	return cmd
}
