package pi

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
	"strings"
	"testing"
	"time"
)

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
