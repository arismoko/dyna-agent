package main

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/runstore"
)

//go:embed assets/pi-extension/dyna.ts
var piExtensionTS []byte

const piOrchestrationPrompt = "Dyna is enabled for this Pi launch. Treat these instructions as standing session guidance. The launcher provides the Dyna extension directly, so do not search for or load a separate dyna skill.\n\n" + skillBody + "\n" + guidanceBody

const (
	piDefaultProvider = "openai-codex"
	piDefaultModel    = "gpt-5.6-terra"
	piDefaultThinking = "xhigh"
	piCodexAuthEnv    = "DYNA_PI_CODEX_AUTH"
)

func piCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "pi [-- pi-args...]",
		Short:              "Launch pi with dyna workflows wired in (extension, instructions, session-scoped /dyna)",
		DisableFlagParsing: true,
		RunE:               runPi,
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func runPi(c *cobra.Command, args []string) error {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		return fmt.Errorf("pi is not installed (npm install -g @earendil-works/pi-coding-agent)")
	}

	extPath, err := provisionPiExtension()
	if err != nil {
		return fmt.Errorf("provision pi extension: %w", err)
	}
	session, err := newPiSessionID()
	if err != nil {
		return fmt.Errorf("create pi session id: %w", err)
	}

	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	piArgs := []string{"--extension", extPath, "--append-system-prompt", piOrchestrationPrompt, "--no-skills"}
	piArgs = append(piArgs, piDefaultArgs(args)...)
	piArgs = append(piArgs, args...)
	cmd := exec.Command(piPath, piArgs...)
	cmd.Env = setEnv(os.Environ(), runstore.SessionEnv, session)
	if !piHasFlag(args, "--api-key") {
		cmd.Env = setEnv(cmd.Env, piCodexAuthEnv, "1")
	} else {
		cmd.Env = setEnv(cmd.Env, piCodexAuthEnv, "0")
	}
	if exe, err := os.Executable(); err == nil {
		cmd.Env = setEnv(cmd.Env, "DYNA_BIN", exe)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runPiProcess(cmd)
}

func piDefaultArgs(args []string) []string {
	defaults := make([]string, 0, 6)
	if !piHasFlag(args, "--provider") && !piHasFlag(args, "--model") && !piHasFlag(args, "--models") {
		defaults = append(defaults, "--provider", piDefaultProvider, "--model", piDefaultModel)
	}
	if !piHasFlag(args, "--thinking") && !piModelHasThinking(args) && !piModelScopeHasThinking(args) {
		defaults = append(defaults, "--thinking", piDefaultThinking)
	}
	return defaults
}

func piHasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func piFlagValue(args []string, name string) string {
	value := ""
	for i, arg := range args {
		if strings.HasPrefix(arg, name+"=") {
			value = strings.TrimPrefix(arg, name+"=")
		}
		if arg == name && i+1 < len(args) {
			value = args[i+1]
		}
	}
	return value
}

func piModelScopeHasThinking(args []string) bool {
	for _, model := range strings.Split(piFlagValue(args, "--models"), ",") {
		if piThinkingSuffix(model) {
			return true
		}
	}
	return false
}

func piModelHasThinking(args []string) bool {
	return piThinkingSuffix(piFlagValue(args, "--model"))
}

func piThinkingSuffix(model string) bool {
	if i := strings.LastIndex(model, ":"); i >= 0 {
		model = model[i+1:]
	} else {
		return false
	}
	switch model {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func newPiSessionID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pisess_" + hex.EncodeToString(b), nil
}

func provisionPiExtension() (string, error) {
	dir := filepath.Join(runstore.DataDir(), "pi-extension")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dyna.ts")
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, piExtensionTS) {
		if err := os.WriteFile(path, piExtensionTS, 0o644); err != nil {
			return "", err
		}
	}
	return path, nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}
