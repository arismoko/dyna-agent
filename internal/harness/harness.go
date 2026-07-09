// Package harness executes a single worker turn on a concrete agent CLI
// (claude-code, codex, opencode, pi, a custom argv, or the built-in mock).
package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/profile"
)

// Result of one worker invocation.
type Result struct {
	Output   string
	Duration time.Duration
}

// Run sends prompt to the worker described by p and returns its final message.
func Run(ctx context.Context, p profile.Profile, prompt, cwd string) (Result, error) {
	start := time.Now()
	if p.Harness == profile.HarnessMock {
		out, err := runMock(ctx, prompt)
		return Result{Output: out, Duration: time.Since(start)}, err
	}

	argv, stdinPrompt, outFile, cleanup, err := buildArgv(p, prompt)
	if err != nil {
		return Result{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for k, v := range p.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if stdinPrompt {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Own process group so a timeout/cancel kills the whole worker tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	runErr := cmd.Run()
	dur := time.Since(start)

	out := strings.TrimSpace(stdout.String())
	if outFile != "" {
		if b, err := os.ReadFile(outFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			out = strings.TrimSpace(string(b))
		}
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return Result{Output: out, Duration: dur}, fmt.Errorf("worker %s canceled/timed out: %w", p.Name, ctx.Err())
		}
		errTail := tail(strings.TrimSpace(stderr.String()), 2000)
		if errTail == "" {
			errTail = tail(out, 2000)
		}
		return Result{Output: out, Duration: dur}, fmt.Errorf("worker %s (%s) failed: %v: %s", p.Name, argv[0], runErr, errTail)
	}
	if out == "" {
		return Result{Output: out, Duration: dur}, fmt.Errorf("worker %s returned empty output", p.Name)
	}
	return Result{Output: out, Duration: dur}, nil
}

// buildArgv returns the command line for one worker turn. stdinPrompt reports
// whether the prompt is delivered on stdin (vs already substituted into argv).
// outFile, when non-empty, is a file the CLI writes the final message to.
func buildArgv(p profile.Profile, prompt string) (argv []string, stdinPrompt bool, outFile string, cleanup func(), err error) {
	switch p.Harness {
	case profile.HarnessClaudeCode:
		argv = []string{"claude", "-p"}
		// Workers run headless and must act autonomously; permission prompts
		// would hang forever. Profiles can opt back out with safeMode.
		if !p.SafeMode && !hasArg(p.ExtraArgs, "--dangerously-skip-permissions") {
			argv = append(argv, "--dangerously-skip-permissions")
		}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		return argv, true, "", nil, nil

	case profile.HarnessCodex:
		f, ferr := os.CreateTemp("", "dyna-codex-*.txt")
		if ferr != nil {
			return nil, false, "", nil, ferr
		}
		f.Close()
		outFile = f.Name()
		cleanup = func() { os.Remove(outFile) }
		argv = []string{"codex", "exec", "--skip-git-repo-check", "--output-last-message", outFile}
		if !p.SafeMode && !hasArg(p.ExtraArgs, "--dangerously-bypass-approvals-and-sandbox") {
			argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
		}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		argv = append(argv, "-") // read prompt from stdin
		return argv, true, outFile, cleanup, nil

	case profile.HarnessOpenCode:
		argv = []string{"opencode", "run"}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		argv = append(argv, prompt)
		return argv, false, "", nil, nil

	case profile.HarnessPi:
		argv = []string{"pi", "-p"}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		argv = append(argv, prompt)
		return argv, false, "", nil, nil

	case profile.HarnessCustom:
		hasPrompt := false
		for _, a := range p.Command {
			out := strings.ReplaceAll(a, "{{model}}", p.Model)
			if strings.Contains(out, "{{prompt}}") {
				out = strings.ReplaceAll(out, "{{prompt}}", prompt)
				hasPrompt = true
			}
			argv = append(argv, out)
		}
		return argv, !hasPrompt, "", nil, nil
	}
	return nil, false, "", nil, fmt.Errorf("unknown harness %q", p.Harness)
}

// runMock is a deterministic in-process worker used for demos and tests.
// If the prompt contains a line starting with "RESPOND:", everything after
// that marker is echoed back verbatim (lets tests exercise schema parsing).
func runMock(ctx context.Context, prompt string) (string, error) {
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Ignore the schema instruction suffix the engine may have appended.
	body := prompt
	if i := strings.Index(body, "\n\n---\nOUTPUT FORMAT:"); i >= 0 {
		body = body[:i]
	}
	if i := strings.Index(body, "RESPOND:"); i >= 0 {
		return strings.TrimSpace(body[i+len("RESPOND:"):]), nil
	}
	head := body
	if len(head) > 120 {
		head = head[:120] + "…"
	}
	return "[mock worker] I received your task: " + head, nil
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
