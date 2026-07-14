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

func piCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "pi [-- pi-args...]",
		Short:              "Launch pi with dyna workflows wired in (skill, extension, session-scoped /dyna)",
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

	for _, target := range skillTargets() {
		if target.name != "pi" {
			continue
		}
		if err := installSkill(target); err != nil {
			fmt.Fprintf(c.ErrOrStderr(), "warning: could not install the dyna skill for pi: %v\n", err)
		}
		break
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
	piArgs := append([]string{"--extension", extPath}, args...)
	cmd := exec.Command(piPath, piArgs...)
	cmd.Env = setEnv(os.Environ(), runstore.SessionEnv, session)
	if exe, err := os.Executable(); err == nil {
		cmd.Env = setEnv(cmd.Env, "DYNA_BIN", exe)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runPiProcess(cmd)
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
