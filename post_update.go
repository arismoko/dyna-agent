package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

type postUpdateAnswers struct {
	Replace bool `json:"replace"`
	Managed bool `json:"managed"`
}

type postUpdateState struct {
	Version string            `json:"version"`
	Answers postUpdateAnswers `json:"answers"`
}

func postUpdateStatePath() string {
	return filepath.Join(runstore.DataDir(), "update-consent.json")
}

func shouldPromptPostUpdate(command string, stdinTTY, stdoutTTY bool, currentVersion string, disabled, worker bool) bool {
	if !stdinTTY || !stdoutTTY || disabled || worker || currentVersion == "" || strings.HasPrefix(currentVersion, "dev") {
		return false
	}
	switch command {
	case "journal", "run", "update", "_post-update-apply":
		return false
	default:
		return true
	}
}

func maybeOfferPostUpdateSetup(c *cobra.Command) error {
	currentVersion := resolveVersion()
	if !shouldPromptPostUpdate(
		c.Name(),
		isTerminalFile(c.InOrStdin()),
		isTerminalFile(c.OutOrStdout()),
		currentVersion,
		os.Getenv("DYNA_NO_AUTO_UPDATE") == "1",
		os.Getenv(runstore.AgentJournalEnv) != "",
	) {
		return nil
	}
	if _, err := os.Stat(profile.DefaultPath()); err != nil {
		return nil
	}
	if state, err := readPostUpdateState(); err == nil {
		if state.Version == currentVersion {
			return nil
		}
		if err := applyRecurringPostUpdateSetup(state.Answers, c.OutOrStdout()); err != nil {
			return err
		}
		return writePostUpdateState(currentVersion, state.Answers)
	}

	answers, err := promptPostUpdateSetup(c.InOrStdin(), c.OutOrStdout())
	if err != nil {
		return err
	}
	if err := applyPostUpdateSetup(answers, c.OutOrStdout()); err != nil {
		return err
	}
	return writePostUpdateState(currentVersion, answers)
}

func promptPostUpdateSetup(in io.Reader, out io.Writer) (postUpdateAnswers, error) {
	reader := bufio.NewReader(in)
	questions := []string{
		"dyna now ships managed default profiles (kept up to date by dyna updates). Replace your local profiles that collide with the bundled ones now? [y/N] ",
		"Keep them automatically updated from future dyna releases? WARNING: local customizations to these profiles could be overwritten. [y/N] ",
	}
	answers := make([]bool, len(questions))
	for i, question := range questions {
		answer, err := askYesNo(reader, out, question)
		if err != nil {
			return postUpdateAnswers{}, err
		}
		answers[i] = answer
	}
	return postUpdateAnswers{Replace: answers[0], Managed: answers[1]}, nil
}

func askYesNo(reader *bufio.Reader, out io.Writer, question string) (bool, error) {
	if _, err := fmt.Fprint(out, question); err != nil {
		return false, err
	}
	answer, err := reader.ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func applyPostUpdateSetup(answers postUpdateAnswers, out io.Writer) error {
	store, err := loadStore()
	if err != nil {
		return err
	}
	if collisions := store.ApplyBundledPreferences(answers.Replace, answers.Managed); collisions > 0 {
		if err := store.Save(); err != nil {
			return err
		}
		fmt.Fprintf(out, "updated %d bundled profile collision(s)\n", collisions)
	}
	return nil
}

// applyRecurringPostUpdateSetup deliberately avoids ApplyBundledPreferences:
// loading the store refreshes only profiles the user still marks managed.
func applyRecurringPostUpdateSetup(_ postUpdateAnswers, _ io.Writer) error {
	if _, err := loadStore(); err != nil {
		return err
	}
	return nil
}

func readPostUpdateState() (postUpdateState, error) {
	b, err := os.ReadFile(postUpdateStatePath())
	if err != nil {
		return postUpdateState{}, err
	}
	var state postUpdateState
	if err := json.Unmarshal(b, &state); err != nil {
		return postUpdateState{}, err
	}
	if strings.TrimSpace(state.Version) == "" {
		return postUpdateState{}, fmt.Errorf("invalid post-update consent state: missing version")
	}
	return state, nil
}

func writePostUpdateState(currentVersion string, answers postUpdateAnswers) error {
	path := postUpdateStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(postUpdateState{Version: currentVersion, Answers: answers}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func postUpdateApplyCmd() *cobra.Command {
	var answers postUpdateAnswers
	var stampVersion string
	var recurring bool
	cmd := &cobra.Command{
		Use:    "_post-update-apply",
		Hidden: true,
		RunE: func(c *cobra.Command, _ []string) error {
			apply := applyPostUpdateSetup
			if recurring {
				apply = applyRecurringPostUpdateSetup
			}
			if err := apply(answers, c.OutOrStdout()); err != nil {
				return err
			}
			if stampVersion == "" {
				stampVersion = resolveVersion()
			}
			return writePostUpdateState(stampVersion, answers)
		},
	}
	cmd.Flags().BoolVar(&answers.Replace, "replace", false, "replace colliding bundled profiles")
	cmd.Flags().BoolVar(&answers.Managed, "managed", false, "keep colliding bundled profiles managed")
	cmd.Flags().BoolVar(&recurring, "recurring", false, "refresh only previously accepted managed setup")
	cmd.Flags().StringVar(&stampVersion, "stamp-version", "", "version recorded as prompted")
	return cmd
}

func offerSetupAfterUpdate(c *cobra.Command, executable, latest string) {
	if state, err := readPostUpdateState(); err == nil {
		if err := applySetupWithExecutable(c.Context(), executable, latest, state.Answers, true, c.OutOrStdout(), c.ErrOrStderr()); err != nil {
			fmt.Fprintf(c.ErrOrStderr(), "warning: dyna updated, but accepted setup refresh failed: %v\n", err)
		}
		return
	}
	if !shouldOfferPostUpdateSetup(
		isTerminalFile(c.InOrStdin()),
		isTerminalFile(c.OutOrStdout()),
		os.Getenv(runstore.AgentJournalEnv) != "",
	) {
		fmt.Fprintln(c.OutOrStdout(), "post-update setup is interactive; later you can run `dyna profiles init --force`")
		return
	}
	answers, err := promptPostUpdateSetup(c.InOrStdin(), c.OutOrStdout())
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "warning: dyna updated, but post-update setup was skipped: %v\n", err)
		return
	}
	if err := applySetupWithExecutable(c.Context(), executable, latest, answers, false, c.OutOrStdout(), c.ErrOrStderr()); err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "warning: dyna updated, but post-update setup failed: %v\n", err)
	}
}

func shouldOfferPostUpdateSetup(stdinTTY, stdoutTTY, worker bool) bool {
	return stdinTTY && stdoutTTY && !worker
}

func applySetupWithExecutable(ctx context.Context, executable, latest string, answers postUpdateAnswers, recurring bool, stdout, stderr io.Writer) error {
	args := []string{
		"_post-update-apply",
		"--replace=" + strconv.FormatBool(answers.Replace),
		"--managed=" + strconv.FormatBool(answers.Managed),
		"--stamp-version=" + latest,
	}
	if recurring {
		args = append(args, "--recurring=true")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "DYNA_NO_AUTO_UPDATE=1")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func isTerminalFile(v any) bool {
	file, ok := v.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
