// Package harness executes a single worker turn on a concrete agent CLI
// (claude-code, codex, opencode, pi, a custom argv, or the built-in mock).
package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

// Result of one worker invocation.
type Result struct {
	Output        string
	Duration      time.Duration
	Nudged        bool // recovered after a transient harness failure
	JournalNudges int  // exact-session journal reminders that successfully started
}

const (
	sessionNudgeDelay       = 2 * time.Second
	journalInterruptGrace   = 750 * time.Millisecond
	journalTerminateGrace   = 500 * time.Millisecond
	cancelTerminateGrace    = 250 * time.Millisecond
	journalCompletionLimit  = 90 * time.Second
	sessionNudgePrompt      = "Continue from where you left off. The previous turn was interrupted by a transient harness/API failure. Inspect the current state, do not redo completed work, and finish the original task. Return the final response requested by the original prompt."
	journalNudgePrompt      = "Your dyna work journal has been quiet for five minutes. Write a brief progress entry now with `dyna journal` (one or two sentences plus an optional next step), then continue the original task. Do not stop after reporting status, do not redo completed work, and still return the final response requested by the original prompt."
	journalCompletionPrompt = "Before finishing, write one brief entry to your dyna work journal now with `dyna journal` (one or two sentences plus an optional next step). Then return the same final response you already produced for the original task. Do not redo the task."
	steeringPollInterval    = 100 * time.Millisecond

	JournalNudgeIdle    = "idle"
	JournalNudgeMissing = "missing-entry"
)

// JournalNudge describes a journal reminder or a reminder that could not be
// delivered safely. Reason is JournalNudgeIdle or JournalNudgeMissing.
type JournalNudge struct {
	Delivered bool
	Reason    string
}

// JournalOptions supervises the run-owned progress journal for a live worker.
// A valid agent-authored entry resets the inactivity deadline. If a resumable
// session stays quiet, dyna interrupts that turn and continues the exact same
// session with journalNudgePrompt; it never starts a fresh replacement worker.
type JournalOptions struct {
	Path              string
	IdleAfter         time.Duration
	CompletionTimeout time.Duration
	OnEntry           func(runstore.AgentJournalEntry)
	OnNudge           func(JournalNudge)
	State             *JournalState
}

// SteeringOptions identifies the run-owned mailbox for one worker. It is
// activated only after the harness has an exact resumable session ID.
type SteeringOptions struct {
	RunID   string
	AgentID int
	// OnDispatch records an accepted message before its exact-session
	// continuation can start. Returning an error prevents that continuation.
	OnDispatch func(runstore.SteeringMessage) error
	// OnMessage confirms delivery only after the continuation process starts.
	OnMessage func(runstore.SteeringMessage)
	pollEvery time.Duration
}

// JournalState keeps the inactivity clock across multiple harness turns that
// belong to one logical agent call (notably JSON-schema correction retries).
type JournalState struct {
	mu              sync.Mutex
	lastActivity    time.Time
	reminded        bool
	unavailableNote bool
	hasEntry        bool
	completionTried bool
	offset          int64
	offsetPath      string
	offsetReady     bool
	reported        map[string]uint8
}

func NewJournalState() *JournalState {
	return &JournalState{lastActivity: time.Now()}
}

type commandSpec struct {
	argv                  []string
	stdinPrompt           bool
	prompt                string
	outFile               string
	parseOutput           func(string) string
	journalReminderReason string
	steering              []runstore.SteeringMessage
}

type invocation struct {
	initial   commandSpec
	cleanup   func()
	sessionID func(string) string
	resume    func(string, string) commandSpec
}

type attempt struct {
	output              string
	stdout              string
	stderr              string
	runErr              error
	started             bool
	contextDone         bool
	journalInterrupted  bool
	steering            []runstore.SteeringMessage
	steeringInterrupted bool
	steeringErr         error
}

type workingDirectoryError struct {
	dir   string
	cause error
}

func (e *workingDirectoryError) Error() string {
	return fmt.Sprintf("working directory %q is unavailable: %v", e.dir, e.cause)
}

func (e *workingDirectoryError) Unwrap() error { return e.cause }

// Run sends prompt to the worker described by p and returns its final message.
func Run(ctx context.Context, p profile.Profile, prompt, cwd string) (Result, error) {
	return RunWithRecovery(ctx, p, prompt, cwd, true)
}

// RunWithRecovery is Run with an explicit same-session recovery budget. The
// engine uses allowRecovery=false after a schema call has already nudged once.
func RunWithRecovery(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool) (Result, error) {
	return run(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay)
}

// RunWithJournal adds live progress-journal supervision to RunWithRecovery.
// It is kept separate so non-workflow callers retain the simple Run API.
func RunWithJournal(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, journal JournalOptions) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay, journal, SteeringOptions{})
}

// RunWithJournalAndSteering adds a run-owned steering mailbox to the normal
// journaled invocation. Unsupported/non-resumable harnesses never activate it.
func RunWithJournalAndSteering(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, journal JournalOptions, steering SteeringOptions) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, sessionNudgeDelay, journal, steering)
}

func run(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration) (Result, error) {
	return runJournaled(ctx, p, prompt, cwd, allowRecovery, nudgeDelay, JournalOptions{}, SteeringOptions{})
}

func runJournaled(ctx context.Context, p profile.Profile, prompt, cwd string, allowRecovery bool, nudgeDelay time.Duration, journal JournalOptions, steeringOpts SteeringOptions) (Result, error) {
	start := time.Now()
	if p.Harness == profile.HarnessMock {
		if journal.Path != "" {
			e := runstore.AgentJournalEntry{
				TS: time.Now().UnixMilli(), Kind: "update", Message: "Mock worker oriented to the task.",
				Next: "Produce the deterministic mock response.", Source: "agent",
			}
			if err := runstore.AppendAgentJournalPath(journal.Path, e); err == nil && journal.OnEntry != nil {
				journal.OnEntry(e)
			}
		}
		out, err := runMock(ctx, prompt)
		return Result{Output: out, Duration: time.Since(start)}, err
	}

	var err error
	p, err = withJournalPermissions(p, journal.Path)
	if err != nil {
		return Result{}, err
	}
	inv, err := buildInvocation(p, prompt)
	if err != nil {
		return Result{}, err
	}
	if inv.cleanup != nil {
		defer inv.cleanup()
	}

	tracker := newSessionTracker(inv.sessionID)
	tracker.observe("") // Claude/Pi preassign an id without reading stdout.
	supervisor := newJournalSupervisor(journal)
	steering := newSteeringSupervisor(steeringOpts, inv.resume != nil)
	if steering != nil {
		defer steering.close()
		if err := steering.activateIfReady(tracker.idValue()); err != nil {
			return Result{}, err
		}
	}
	spec := inv.initial
	var recoveryErr error
	var lastOutput string
	recovered := false
	journalNudges := 0
	completionReminderAttempted := false
	completionOutput := ""
	resumeSteering := func(messages []runstore.SteeringMessage) error {
		if len(messages) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("worker %s canceled/timed out before steering continuation: %w", p.Name, err)
		}
		sessionID := tracker.idValue()
		if sessionID == "" || inv.resume == nil {
			return fmt.Errorf("worker %s received steering but its exact session could not be resumed", p.Name)
		}
		if err := prepareResumeOutput(inv.initial.outFile); err != nil {
			return err
		}
		if err := steering.dispatch(messages); err != nil {
			return fmt.Errorf("record steering dispatch for worker %s: %w", p.Name, err)
		}
		// Dispatch callbacks may race with external cancellation. CommandContext
		// below is the final pre-Start gate, but avoid preparing a continuation
		// at all when cancellation is already visible here.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("worker %s canceled/timed out before steering continuation: %w", p.Name, err)
		}
		if supervisor != nil {
			supervisor.noteExternalResume()
		}
		completionReminderAttempted = false
		completionOutput = ""
		spec = inv.resume(sessionID, steeringPrompt(messages))
		spec.steering = append([]runstore.SteeringMessage(nil), messages...)
		return nil
	}
	finishOrSteer := func() (bool, error) {
		if steering == nil {
			return false, nil
		}
		messages, err := steering.finish()
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("worker %s canceled/timed out after final steering poll: %w", p.Name, err)
		}
		if len(messages) == 0 {
			return false, nil
		}
		return true, resumeSteering(messages)
	}

	for {
		attemptCtx := ctx
		cancelAttempt := func() {}
		if spec.journalReminderReason == JournalNudgeMissing {
			limit := journal.CompletionTimeout
			if limit <= 0 {
				limit = journalCompletionLimit
			}
			attemptCtx, cancelAttempt = context.WithTimeout(ctx, limit)
		}
		a := runOnce(attemptCtx, p, cwd, spec, tracker, supervisor, steering, inv.resume != nil)
		cancelAttempt()
		if spec.journalReminderReason != "" && a.started {
			journalNudges++
		} else if spec.journalReminderReason != "" && supervisor != nil {
			supervisor.markNudged(false, spec.journalReminderReason)
		}
		tracker.observe(a.stdout)
		if a.output != "" {
			lastOutput = a.output
		}
		if a.steeringInterrupted && ctx.Err() == nil {
			if err := resumeSteering(a.steering); err != nil {
				return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, err
			}
			continue
		}
		if len(a.steering) > 0 && ctx.Err() == nil {
			if err := resumeSteering(a.steering); err != nil {
				return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, err
			}
			continue
		}

		if a.journalInterrupted && ctx.Err() == nil {
			sessionID := tracker.idValue()
			if sessionID == "" || inv.resume == nil {
				return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges},
					fmt.Errorf("worker %s was interrupted for a journal reminder but its exact session could not be resumed", p.Name)
			}
			if err := prepareResumeOutput(inv.initial.outFile); err != nil {
				return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, err
			}
			spec = inv.resume(sessionID, journalNudgePrompt)
			spec.journalReminderReason = JournalNudgeIdle
			continue
		}

		attemptErr := attemptError(ctx, p, spec.argv, a)
		if attemptErr == nil {
			// A short worker can finish before the inactivity deadline. Give every
			// resumable worker one exact-session chance to satisfy the journal
			// contract, while preserving its already-successful task result.
			if supervisor != nil && !supervisor.hasAgentEntry() {
				if completionReminderAttempted {
					supervisor.markNudged(false, JournalNudgeMissing)
					if resume, finishErr := finishOrSteer(); finishErr != nil {
						return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
					} else if resume {
						continue
					}
					return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
				}
				if !supervisor.claimCompletionReminder() {
					if resume, finishErr := finishOrSteer(); finishErr != nil {
						return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
					} else if resume {
						continue
					}
					return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
				}
				sessionID := tracker.idValue()
				if sessionID == "" || inv.resume == nil {
					supervisor.markNudged(false, JournalNudgeMissing)
					if resume, finishErr := finishOrSteer(); finishErr != nil {
						return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
					} else if resume {
						continue
					}
					return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
				}
				completionOutput = a.output
				if err := prepareResumeOutput(inv.initial.outFile); err != nil {
					supervisor.markNudged(false, JournalNudgeMissing)
					if resume, finishErr := finishOrSteer(); finishErr != nil {
						return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
					} else if resume {
						continue
					}
					return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
				}
				completionReminderAttempted = true
				spec = inv.resume(sessionID, journalCompletionPrompt)
				spec.journalReminderReason = JournalNudgeMissing
				continue
			}
			if completionReminderAttempted {
				if resume, finishErr := finishOrSteer(); finishErr != nil {
					return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
				} else if resume {
					continue
				}
				return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
			}
			if resume, finishErr := finishOrSteer(); finishErr != nil {
				return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
			} else if resume {
				continue
			}
			return Result{Output: a.output, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
		}
		if completionReminderAttempted {
			if ctx.Err() != nil {
				return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, attemptErr
			}
			// Journaling is a progress side channel. Do not turn an already-valid
			// task result into a failure if its completion reminder cannot finish.
			supervisor.markNudged(false, JournalNudgeMissing)
			if resume, finishErr := finishOrSteer(); finishErr != nil {
				return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
			} else if resume {
				continue
			}
			return Result{Output: completionOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, nil
		}
		if recoveryErr != nil {
			out := a.output
			if out == "" {
				out = lastOutput
			}
			if resume, finishErr := finishOrSteer(); finishErr != nil {
				return Result{Output: out, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
			} else if resume {
				continue
			}
			return Result{Output: out, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges},
				fmt.Errorf("%v; same-session nudge failed: %w", recoveryErr, attemptErr)
		}

		// A fresh retry can repeat edits or other side effects. Recover only when
		// the CLI process started and exposed an exact resumable session.
		if !allowRecovery || recovered || ctx.Err() != nil || !a.started || inv.resume == nil {
			if ctx.Err() == nil {
				if resume, finishErr := finishOrSteer(); finishErr != nil {
					return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
				} else if resume {
					continue
				}
			}
			return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, attemptErr
		}
		sessionID := tracker.idValue()
		if sessionID == "" {
			if resume, finishErr := finishOrSteer(); finishErr != nil {
				return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, finishErr
			} else if resume {
				continue
			}
			return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges}, attemptErr
		}
		if err := waitForSessionNudge(ctx, nudgeDelay); err != nil {
			return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges},
				fmt.Errorf("worker %s canceled/timed out before session nudge: %w", p.Name, err)
		}
		if err := prepareResumeOutput(inv.initial.outFile); err != nil {
			return Result{Output: lastOutput, Duration: time.Since(start), Nudged: recovered, JournalNudges: journalNudges},
				fmt.Errorf("%v; prepare same-session nudge: %w", attemptErr, err)
		}
		recoveryErr = attemptErr
		recovered = true
		spec = inv.resume(sessionID, sessionNudgePrompt)
	}
}

func prepareResumeOutput(path string) error {
	if path == "" {
		return nil
	}
	return os.Truncate(path, 0)
}

func waitForSessionNudge(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runOnce(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, tracker *sessionTracker, journal *journalSupervisor, steering *steeringSupervisor, resumable bool) attempt {
	if err := validateWorkingDirectory(cwd); err != nil {
		return finishAttempt(spec, "", "", err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	cmd := exec.CommandContext(ctx, spec.argv[0], spec.argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = mergeEnv(os.Environ(), p.Env)
	if spec.stdinPrompt {
		cmd.Stdin = strings.NewReader(spec.prompt)
	}
	stdout := newObservedBuffer(tracker.observe)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	// Own process group so a timeout/cancel kills the whole worker tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return signalProcessGroup(cmd, syscall.SIGTERM)
	}

	if err := cmd.Start(); err != nil {
		if cwdErr := validateWorkingDirectory(cwd); cwdErr != nil {
			err = cwdErr
		}
		return finishAttempt(spec, stdout.String(), stderr.String(), err, false, ctx.Err() != nil, false, false, nil, nil)
	}
	if steering != nil && steering.opts.OnMessage != nil {
		for _, message := range spec.steering {
			steering.opts.OnMessage(message)
		}
	}
	if spec.journalReminderReason != "" && journal != nil {
		journal.markNudged(true, spec.journalReminderReason)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var tick <-chan time.Time
	var ticker *time.Ticker
	if journal != nil || steering != nil {
		interval := steeringPollInterval
		if steering != nil && steering.opts.pollEvery > 0 {
			interval = steering.opts.pollEvery
		}
		if journal != nil && journal.pollInterval() < interval {
			interval = journal.pollInterval()
		}
		ticker = time.NewTicker(interval)
		tick = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case runErr := <-done:
			if journal != nil {
				journal.poll()
			}
			if ctx.Err() != nil {
				return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
			}
			messages := []runstore.SteeringMessage(nil)
			if steering != nil {
				_ = steering.activateIfReady(tracker.idValue())
				steering.poll()
				if ctx.Err() != nil {
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
				}
				messages = steering.take()
			}
			return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, messages, steering.errValue())
		case <-ctx.Done():
			runErr := stopCanceledProcess(cmd, done)
			if journal != nil {
				journal.poll()
			}
			return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
		case <-tick:
			if steering != nil {
				_ = steering.activateIfReady(tracker.idValue())
				steering.poll()
				if steering.errValue() != nil {
					runErr := stopCanceledProcess(cmd, done)
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, false, nil, steering.errValue())
				}
				if steering.hasPending() && resumable && tracker.idValue() != "" {
					runErr, interrupted := interruptForContinuation(ctx, cmd, done)
					messages := steering.take()
					if ctx.Err() != nil {
						return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
					}
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, false, interrupted, messages, steering.errValue())
				}
			}
			if journal != nil {
				journal.poll()
				if !journal.reminderDue() {
					continue
				}
				if resumable {
					if tracker.idValue() == "" {
						continue
					}
					runErr, interrupted := interruptForContinuation(ctx, cmd, done)
					journal.poll()
					if ctx.Err() != nil {
						return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, true, false, false, nil, nil)
					}
					return finishAttempt(spec, stdout.String(), stderr.String(), runErr, true, false, interrupted, false, nil, nil)
				}
				journal.markNudged(false, JournalNudgeIdle)
			}
		}
	}
}

func validateWorkingDirectory(cwd string) error {
	if cwd == "" {
		return nil
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return &workingDirectoryError{dir: cwd, cause: err}
	}
	if !info.IsDir() {
		return &workingDirectoryError{dir: cwd, cause: errors.New("not a directory")}
	}
	if err := syscall.Access(cwd, 1); err != nil {
		return &workingDirectoryError{dir: cwd, cause: err}
	}
	return nil
}

// interruptForContinuation gives the CLI time to persist its exact session
// before a journal or steering continuation. A completion already waiting in
// done wins and can still be continued without interrupting it.
func interruptForContinuation(ctx context.Context, cmd *exec.Cmd, done <-chan error) (error, bool) {
	select {
	case runErr := <-done:
		return runErr, false
	default:
	}

	if err := signalProcessGroup(cmd, syscall.SIGINT); err != nil {
		if err == os.ErrProcessDone {
			return <-done, false
		}
		return stopProcess(cmd, done, err), false
	}
	if runErr, stopped, canceled := waitForProcess(ctx, done, journalInterruptGrace); stopped || canceled {
		if canceled {
			return stopCanceledProcess(cmd, done), false
		}
		return runErr, true
	}

	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err), true
	}
	if runErr, stopped, canceled := waitForProcess(ctx, done, journalTerminateGrace); stopped || canceled {
		if canceled {
			return stopCanceledProcess(cmd, done), false
		}
		return runErr, true
	}

	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err), true
	}
	return <-done, true
}

func stopCanceledProcess(cmd *exec.Cmd, done <-chan error) error {
	select {
	case runErr := <-done:
		return runErr
	default:
	}
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err)
	}
	if runErr, stopped, _ := waitForProcess(context.Background(), done, cancelTerminateGrace); stopped {
		return runErr
	}
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err)
	}
	return <-done
}

func stopProcess(cmd *exec.Cmd, done <-chan error, signalErr error) error {
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("signal worker: %v; kill worker: %w", signalErr, err)
	}
	runErr := <-done
	if runErr != nil {
		return fmt.Errorf("signal worker: %v; worker exit: %w", signalErr, runErr)
	}
	return signalErr
}

func waitForProcess(ctx context.Context, done <-chan error, delay time.Duration) (runErr error, stopped, canceled bool) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case runErr = <-done:
		return runErr, true, false
	case <-ctx.Done():
		return nil, false, true
	case <-timer.C:
		return nil, false, false
	}
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, pair := range base {
		key := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			key = pair[:i]
		}
		if _, replaced := overrides[key]; !replaced {
			out = append(out, pair)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func finishAttempt(spec commandSpec, stdout, stderr string, runErr error, started, contextDone, journalInterrupted, steeringInterrupted bool, steering []runstore.SteeringMessage, steeringErr error) attempt {
	var out string
	if spec.outFile != "" {
		// stdout may be a JSON event stream used only to discover the session.
		// The requested final-message file is authoritative even when empty.
		if b, err := os.ReadFile(spec.outFile); err == nil {
			out = strings.TrimSpace(string(b))
		}
	} else if spec.parseOutput != nil {
		out = strings.TrimSpace(spec.parseOutput(stdout))
	} else {
		out = strings.TrimSpace(stdout)
	}
	return attempt{
		output: out, stdout: stdout, stderr: stderr, runErr: runErr,
		started: started, contextDone: contextDone, journalInterrupted: journalInterrupted,
		steering: steering, steeringInterrupted: steeringInterrupted, steeringErr: steeringErr,
	}
}

// sessionTracker learns a harness session id from streaming JSON output. This
// is what makes a journal reminder safe for Codex/OpenCode: the current turn is
// not interrupted until its exact session is known.
type sessionTracker struct {
	mu    sync.RWMutex
	parse func(string) string
	id    string
}

func newSessionTracker(parse func(string) string) *sessionTracker {
	return &sessionTracker{parse: parse}
}

func (s *sessionTracker) observe(text string) {
	if s == nil || s.parse == nil {
		return
	}
	s.mu.RLock()
	hasID := s.id != ""
	s.mu.RUnlock()
	if hasID {
		return
	}
	id := s.parse(text)
	if id == "" {
		return
	}
	s.mu.Lock()
	if s.id == "" {
		s.id = id
	}
	s.mu.Unlock()
}

func (s *sessionTracker) idValue() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// observedBuffer retains stdout while forwarding complete JSONL records to a
// session-id observer as they arrive. String is safe after or during writes.
type observedBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	pending string
	onLine  func(string)
}

func newObservedBuffer(onLine func(string)) *observedBuffer {
	return &observedBuffer{onLine: onLine}
}

func (b *observedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	combined := b.pending + string(p)
	parts := strings.Split(combined, "\n")
	b.pending = parts[len(parts)-1]
	lines := append([]string(nil), parts[:len(parts)-1]...)
	b.mu.Unlock()
	if b.onLine != nil {
		for _, line := range lines {
			b.onLine(line)
		}
	}
	return n, err
}

func (b *observedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type steeringSupervisor struct {
	opts    SteeringOptions
	offset  int64
	active  bool
	pending []runstore.SteeringMessage
	err     error
}

func newSteeringSupervisor(opts SteeringOptions, resumable bool) *steeringSupervisor {
	if !resumable || opts.RunID == "" || opts.AgentID <= 0 {
		return nil
	}
	return &steeringSupervisor{opts: opts}
}

func (s *steeringSupervisor) activateIfReady(sessionID string) error {
	if s == nil || s.active || sessionID == "" {
		return nil
	}
	if err := runstore.ActivateAgentSteering(s.opts.RunID, s.opts.AgentID); err != nil {
		s.err = err
		return err
	}
	s.active = true
	return nil
}

func (s *steeringSupervisor) poll() {
	if s == nil || !s.active || s.err != nil {
		return
	}
	messages, next, err := runstore.ReadAgentSteeringFrom(s.opts.RunID, s.opts.AgentID, s.offset, false)
	if err != nil {
		s.err = err
		return
	}
	s.offset = next
	s.pending = append(s.pending, messages...)
}

func (s *steeringSupervisor) hasPending() bool {
	return s != nil && len(s.pending) > 0
}

func (s *steeringSupervisor) take() []runstore.SteeringMessage {
	if s == nil || len(s.pending) == 0 {
		return nil
	}
	messages := append([]runstore.SteeringMessage(nil), s.pending...)
	s.pending = nil
	return messages
}

func (s *steeringSupervisor) dispatch(messages []runstore.SteeringMessage) error {
	if s == nil || s.opts.OnDispatch == nil {
		return nil
	}
	for _, message := range messages {
		if err := s.opts.OnDispatch(message); err != nil {
			return err
		}
	}
	return nil
}

func (s *steeringSupervisor) errValue() error {
	if s == nil {
		return nil
	}
	return s.err
}

func (s *steeringSupervisor) finish() ([]runstore.SteeringMessage, error) {
	if s == nil || !s.active {
		return nil, s.errValue()
	}
	if s.err != nil {
		return nil, s.err
	}
	messages, next, err := runstore.ReadAgentSteeringFrom(s.opts.RunID, s.opts.AgentID, s.offset, true)
	if err != nil {
		s.err = err
		return nil, err
	}
	s.offset = next
	if len(messages) == 0 {
		s.active = false
	}
	s.pending = append(s.pending, messages...)
	return s.take(), nil
}

func (s *steeringSupervisor) close() {
	if s == nil || !s.active {
		return
	}
	_ = runstore.DeactivateAgentSteering(s.opts.RunID, s.opts.AgentID)
	s.active = false
}

func steeringPrompt(messages []runstore.SteeringMessage) string {
	var b strings.Builder
	b.WriteString("[DYNA STEERING]\nA human or parent model sent the following steering to this exact active worker session. Apply it to the original task, preserve completed work, and continue from the current state. The original final-response contract still applies.\n")
	for i, message := range messages {
		if len(messages) > 1 {
			fmt.Fprintf(&b, "\n%d. %s", i+1, message.Message)
		} else {
			b.WriteString("\n" + message.Message)
		}
	}
	return b.String()
}

type journalSupervisor struct {
	opts   JournalOptions
	offset int64
	state  *JournalState
}

func newJournalSupervisor(opts JournalOptions) *journalSupervisor {
	if opts.Path == "" {
		return nil
	}
	state := opts.State
	if state == nil {
		state = NewJournalState()
	}
	state.mu.Lock()
	if state.lastActivity.IsZero() {
		state.lastActivity = time.Now()
	}
	if !state.offsetReady || state.offsetPath != opts.Path {
		state.offset = 0
		if info, err := os.Stat(opts.Path); err == nil {
			state.offset = info.Size()
		}
		state.offsetPath = opts.Path
		state.offsetReady = true
	}
	offset := state.offset
	state.mu.Unlock()
	return &journalSupervisor{opts: opts, offset: offset, state: state}
}

func (j *journalSupervisor) pollInterval() time.Duration {
	if j == nil || j.opts.IdleAfter <= 0 {
		return 500 * time.Millisecond
	}
	d := j.opts.IdleAfter / 20
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	if d > 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	return d
}

func (j *journalSupervisor) poll() {
	if j == nil {
		return
	}
	entries, next, err := runstore.ReadAgentJournalPathFrom(j.opts.Path, j.offset)
	if err != nil {
		return
	}
	j.offset = next
	j.state.mu.Lock()
	j.state.offset = next
	j.state.offsetPath = j.opts.Path
	j.state.offsetReady = true
	j.state.mu.Unlock()
	active := false
	for _, entry := range entries {
		if !agentAuthoredJournalEntry(entry) {
			continue
		}
		active = true
		if j.opts.OnEntry != nil {
			j.opts.OnEntry(entry)
		}
	}
	if active {
		j.state.mu.Lock()
		j.state.lastActivity = time.Now()
		j.state.reminded = false
		j.state.unavailableNote = false
		j.state.hasEntry = true
		delete(j.state.reported, JournalNudgeIdle)
		j.state.mu.Unlock()
	}
}

func agentAuthoredJournalEntry(entry runstore.AgentJournalEntry) bool {
	if entry.TS <= 0 || strings.TrimSpace(entry.Kind) == "" || strings.TrimSpace(entry.Message) == "" {
		return false
	}
	switch entry.Kind {
	case "start", "nudge", "complete", "error":
		return false
	}
	if entry.Source == "agent" {
		return true
	}
	if entry.Source != "" {
		return false
	}
	return true // tolerate valid hand-written legacy entries
}

func (j *journalSupervisor) reminderDue() bool {
	if j == nil || j.opts.IdleAfter <= 0 {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	return !j.state.reminded && time.Since(j.state.lastActivity) >= j.opts.IdleAfter
}

func (j *journalSupervisor) noteExternalResume() {
	if j == nil {
		return
	}
	j.state.mu.Lock()
	j.state.lastActivity = time.Now()
	j.state.reminded = false
	j.state.unavailableNote = false
	j.state.mu.Unlock()
}

func (j *journalSupervisor) hasAgentEntry() bool {
	if j == nil {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	return j.state.hasEntry
}

func (j *journalSupervisor) claimCompletionReminder() bool {
	if j == nil {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	if j.state.completionTried {
		return false
	}
	j.state.completionTried = true
	return true
}

func (j *journalSupervisor) markNudged(delivered bool, reason string) {
	if j == nil {
		return
	}
	j.state.mu.Lock()
	bit := uint8(1)
	if !delivered {
		bit = 2
	}
	if j.state.reported == nil {
		j.state.reported = make(map[string]uint8)
	}
	if j.state.reported[reason]&bit != 0 {
		j.state.mu.Unlock()
		return
	}
	j.state.reported[reason] |= bit
	if reason == JournalNudgeIdle {
		j.state.lastActivity = time.Now()
		j.state.reminded = true
		j.state.unavailableNote = !delivered
	}
	j.state.mu.Unlock()
	if j.opts.OnNudge != nil {
		j.opts.OnNudge(JournalNudge{Delivered: delivered, Reason: reason})
	}
}

func attemptError(ctx context.Context, p profile.Profile, argv []string, a attempt) error {
	if a.steeringErr != nil {
		return fmt.Errorf("worker %s steering mailbox failed: %w", p.Name, a.steeringErr)
	}
	if a.contextDone {
		err := ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		return fmt.Errorf("worker %s canceled/timed out: %w", p.Name, err)
	}
	if a.runErr != nil {
		var cwdErr *workingDirectoryError
		if errors.As(a.runErr, &cwdErr) {
			return fmt.Errorf("worker %s cannot start: %w", p.Name, cwdErr)
		}
		errTail := tail(strings.TrimSpace(a.stderr), 2000)
		if errTail == "" {
			errTail = tail(strings.TrimSpace(a.stdout), 2000)
		}
		return fmt.Errorf("worker %s (%s) failed: %v: %s", p.Name, argv[0], a.runErr, errTail)
	}
	if a.output == "" {
		return fmt.Errorf("worker %s returned empty output", p.Name)
	}
	return nil
}

// withJournalPermissions converts an explicitly read-only Codex profile into
// a narrow custom permission profile: the target workspace stays read-only,
// while only the run-owned agent directory (which contains journal.jsonl) is
// writable. Unlike --sandbox, these -c overrides are accepted by exec resume,
// so exact-session journal reminders remain available.
func withJournalPermissions(p profile.Profile, journalPath string) (profile.Profile, error) {
	if p.Harness != profile.HarnessCodex || journalPath == "" {
		return p, nil
	}
	extra, readOnly := stripCodexReadOnlySandbox(p.ExtraArgs)
	if !readOnly {
		return p, nil
	}
	if !filepath.IsAbs(journalPath) || filepath.Clean(journalPath) != journalPath {
		return p, fmt.Errorf("codex journal path must be canonical and absolute")
	}
	if hasArg(extra, "--dangerously-bypass-approvals-and-sandbox") {
		return p, fmt.Errorf("codex profile combines a read-only sandbox with --dangerously-bypass-approvals-and-sandbox")
	}

	journalDir := filepath.Dir(journalPath)
	permissionConfig := fmt.Sprintf(`permissions.dyna-journal={extends=":read-only",filesystem={%s="write"}}`, strconv.Quote(journalDir))
	extra = append(extra,
		"-c", `default_permissions="dyna-journal"`,
		"-c", `approval_policy="never"`,
		"-c", permissionConfig,
	)
	p.ExtraArgs = extra
	p.SafeMode = true
	return p, nil
}

func stripCodexReadOnlySandbox(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	readOnly := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--sandbox" || arg == "-s") && i+1 < len(args) && strings.EqualFold(strings.Trim(args[i+1], `"'`), "read-only") {
			readOnly = true
			i++
			continue
		}
		if strings.EqualFold(arg, "--sandbox=read-only") || strings.EqualFold(arg, "-s=read-only") {
			readOnly = true
			continue
		}
		if (arg == "-c" || arg == "--config") && i+1 < len(args) && codexConfigSetsReadOnly(args[i+1]) {
			readOnly = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "-c=") && codexConfigSetsReadOnly(strings.TrimPrefix(arg, "-c=")) ||
			strings.HasPrefix(arg, "--config=") && codexConfigSetsReadOnly(strings.TrimPrefix(arg, "--config=")) {
			readOnly = true
			continue
		}
		out = append(out, arg)
	}
	return out, readOnly
}

func codexConfigSetsReadOnly(config string) bool {
	key, value, ok := strings.Cut(config, "=")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if key == "sandbox_mode" {
		return strings.EqualFold(value, "read-only")
	}
	return key == "default_permissions" &&
		(strings.EqualFold(value, ":read-only") || strings.EqualFold(value, "read-only"))
}

func codexHasExplicitPermissions(args []string) bool {
	if hasAnyOption(args,
		"--sandbox", "-s", "--profile", "-p",
		"--dangerously-bypass-approvals-and-sandbox",
	) {
		return true
	}
	for _, config := range codexConfigArgs(args) {
		key, _, ok := strings.Cut(config, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "sandbox_mode" || key == "sandbox_permissions" || key == "default_permissions" ||
			key == "permissions" || strings.HasPrefix(key, "permissions.") {
			return true
		}
	}
	return false
}

func codexConfigArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "-c" || args[i] == "--config") && i+1 < len(args):
			out = append(out, args[i+1])
			i++
		case strings.HasPrefix(args[i], "-c="):
			out = append(out, strings.TrimPrefix(args[i], "-c="))
		case strings.HasPrefix(args[i], "--config="):
			out = append(out, strings.TrimPrefix(args[i], "--config="))
		}
	}
	return out
}

func claudeHasExplicitPermissions(args []string) bool {
	return hasAnyOption(args,
		"--permission-mode", "--allowedTools", "--allowed-tools",
		"--disallowedTools", "--disallowed-tools", "--settings",
		"--dangerously-skip-permissions",
	)
}

func hasOptionValue(args []string, option, want string) bool {
	for i, arg := range args {
		switch {
		case arg == option:
			for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
				if optionValueContains(args[j], want) {
					return true
				}
			}
		case strings.HasPrefix(arg, option+"="):
			if optionValueContains(strings.TrimPrefix(arg, option+"="), want) {
				return true
			}
		}
	}
	return false
}

func optionValueContains(value, want string) bool {
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		if item == want || strings.HasPrefix(item, want+"(") {
			return true
		}
	}
	return false
}

// claudeArgsDenyingAgent folds Agent into every existing deny flag. This
// preserves user denials even if Claude treats repeated variadic flags as
// last-one-wins rather than cumulative.
func claudeArgsDenyingAgent(args []string) []string {
	out := make([]string, 0, len(args)+2)
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--disallowedTools" || arg == "--disallowed-tools":
			found = true
			out = append(out, arg)
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				out = append(out, addOptionValue(args[i], "Agent"))
			} else {
				out = append(out, "Agent")
			}
		case strings.HasPrefix(arg, "--disallowedTools=") || strings.HasPrefix(arg, "--disallowed-tools="):
			found = true
			option, value, _ := strings.Cut(arg, "=")
			out = append(out, option+"="+addOptionValue(value, "Agent"))
		default:
			out = append(out, arg)
		}
	}
	if !found {
		out = append(out, "--disallowedTools", "Agent")
	}
	return out
}

func addOptionValue(value, add string) string {
	if optionValueContains(value, add) {
		return value
	}
	if strings.TrimSpace(value) == "" {
		return add
	}
	return value + "," + add
}

func codexExplicitlyEnablesMultiAgent(args []string) bool {
	if hasOptionValue(args, "--enable", "multi_agent") {
		return true
	}
	for _, config := range codexConfigArgs(args) {
		key, value, ok := strings.Cut(config, "=")
		if ok && strings.TrimSpace(key) == "features.multi_agent" && !strings.EqualFold(strings.Trim(strings.TrimSpace(value), "\"'"), "false") {
			return true
		}
	}
	return false
}

// buildInvocation returns the initial worker command plus an optional exact-
// session continuation. Harnesses without a safe session contract remain
// single-shot rather than risking a duplicate fresh invocation.
func buildInvocation(p profile.Profile, prompt string) (inv invocation, err error) {
	switch p.Harness {
	case profile.HarnessClaudeCode:
		if p.DisableSubagents && (hasOptionValue(p.ExtraArgs, "--allowedTools", "Agent") || hasOptionValue(p.ExtraArgs, "--allowed-tools", "Agent")) {
			return invocation{}, fmt.Errorf("claude profile cannot allow the Agent tool when disableSubagents is enabled")
		}
		claudeExtraArgs := p.ExtraArgs
		if p.DisableSubagents {
			claudeExtraArgs = claudeArgsDenyingAgent(p.ExtraArgs)
		}
		argv := []string{"claude", "-p"}
		// Workers run headless and must act autonomously; permission prompts
		// would hang forever. SafeMode or explicit permission flags opt out.
		if !p.SafeMode && !claudeHasExplicitPermissions(p.ExtraArgs) {
			argv = append(argv, "--dangerously-skip-permissions")
		}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		resumable := !hasAnyOption(p.ExtraArgs, "--session-id", "--resume", "-r", "--continue", "-c", "--fork-session", "--no-session-persistence", "--max-budget-usd")
		var sessionID string
		if resumable {
			sessionID, err = newSessionID()
			if err != nil {
				return invocation{}, fmt.Errorf("create claude session id: %w", err)
			}
			argv = append(argv, "--session-id", sessionID)
		}
		argv = append(argv, claudeExtraArgs...)
		inv.initial = commandSpec{argv: argv, stdinPrompt: true, prompt: prompt}
		if resumable {
			inv.sessionID = func(string) string { return sessionID }
			inv.resume = func(id, nudge string) commandSpec {
				args := []string{"claude", "-p", "--resume", id}
				if !p.SafeMode && !claudeHasExplicitPermissions(p.ExtraArgs) {
					args = append(args, "--dangerously-skip-permissions")
				}
				if p.Model != "" {
					args = append(args, "--model", p.Model)
				}
				args = append(args, claudeExtraArgs...)
				return commandSpec{argv: args, stdinPrompt: true, prompt: nudge}
			}
		}
		return inv, nil

	case profile.HarnessCodex:
		if p.DisableSubagents && codexExplicitlyEnablesMultiAgent(p.ExtraArgs) {
			return invocation{}, fmt.Errorf("codex profile cannot enable multi_agent when disableSubagents is enabled")
		}
		if hasCodexOutputArg(p.ExtraArgs) {
			return invocation{}, fmt.Errorf("codex profile extraArgs may not set -o/--output-last-message; dyna reserves it to capture the worker's final response")
		}
		f, ferr := os.CreateTemp("", "dyna-codex-*.txt")
		if ferr != nil {
			return invocation{}, ferr
		}
		f.Close()
		outFile := f.Name()
		inv.cleanup = func() { os.Remove(outFile) }
		argv := []string{"codex", "exec"}
		if !hasArg(p.ExtraArgs, "--json") {
			argv = append(argv, "--json")
		}
		argv = append(argv, "--skip-git-repo-check", "--output-last-message", outFile)
		if !p.SafeMode && !codexHasExplicitPermissions(p.ExtraArgs) {
			argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
		}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		if p.DisableSubagents && !hasOptionValue(p.ExtraArgs, "--disable", "multi_agent") {
			argv = append(argv, "--disable", "multi_agent")
		}
		argv = append(argv, "-") // read prompt from stdin
		inv.initial = commandSpec{argv: argv, stdinPrompt: true, prompt: prompt, outFile: outFile}
		if !hasAnyOption(p.ExtraArgs, "--ephemeral") && codexResumeCompatible(p.ExtraArgs) {
			inv.sessionID = codexSessionID
			inv.resume = func(id, nudge string) commandSpec {
				args := []string{"codex", "exec", "resume"}
				if !hasArg(p.ExtraArgs, "--json") {
					args = append(args, "--json")
				}
				args = append(args, "--skip-git-repo-check", "--output-last-message", outFile)
				if !p.SafeMode && !codexHasExplicitPermissions(p.ExtraArgs) {
					args = append(args, "--dangerously-bypass-approvals-and-sandbox")
				}
				if p.Model != "" {
					args = append(args, "--model", p.Model)
				}
				args = append(args, p.ExtraArgs...)
				if p.DisableSubagents && !hasOptionValue(p.ExtraArgs, "--disable", "multi_agent") {
					args = append(args, "--disable", "multi_agent")
				}
				args = append(args, id, "-")
				return commandSpec{argv: args, stdinPrompt: true, prompt: nudge, outFile: outFile}
			}
		}
		return inv, nil

	case profile.HarnessOpenCode:
		argv := []string{"opencode", "run"}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		argv = append(argv, p.ExtraArgs...)
		format, hasFormat := argumentValue(p.ExtraArgs, "--format")
		jsonOutput := !hasFormat || format == "json"
		if !hasFormat {
			argv = append(argv, "--format", "json")
		}
		argv = append(argv, prompt)
		inv.initial = commandSpec{argv: argv, parseOutput: nil}
		if jsonOutput {
			inv.initial.parseOutput = openCodeOutput
		}
		if jsonOutput && !hasAnyOption(p.ExtraArgs, "--session", "-s", "--continue", "-c", "--fork") {
			inv.sessionID = openCodeSessionID
			inv.resume = func(id, nudge string) commandSpec {
				args := []string{"opencode", "run"}
				if p.Model != "" {
					args = append(args, "--model", p.Model)
				}
				args = append(args, p.ExtraArgs...)
				if !hasFormat {
					args = append(args, "--format", "json")
				}
				args = append(args, "--session", id, nudge)
				return commandSpec{argv: args, parseOutput: openCodeOutput}
			}
		}
		return inv, nil

	case profile.HarnessPi:
		argv := []string{"pi", "-p"}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		resumable := !hasAnyOption(p.ExtraArgs, "--session-id", "--session", "--continue", "-c", "--resume", "-r", "--fork", "--no-session")
		var sessionID string
		if resumable {
			sessionID, err = newSessionID()
			if err != nil {
				return invocation{}, fmt.Errorf("create pi session id: %w", err)
			}
			argv = append(argv, "--session-id", sessionID)
		}
		argv = append(argv, p.ExtraArgs...)
		argv = append(argv, prompt)
		inv.initial = commandSpec{argv: argv}
		if resumable {
			inv.sessionID = func(string) string { return sessionID }
			inv.resume = func(id, nudge string) commandSpec {
				args := []string{"pi", "-p", "--session", id}
				if p.Model != "" {
					args = append(args, "--model", p.Model)
				}
				args = append(args, p.ExtraArgs...)
				args = append(args, nudge)
				return commandSpec{argv: args}
			}
		}
		return inv, nil

	case profile.HarnessCustom:
		var argv []string
		hasPrompt := false
		for _, a := range p.Command {
			out := strings.ReplaceAll(a, "{{model}}", p.Model)
			if strings.Contains(out, "{{prompt}}") {
				out = strings.ReplaceAll(out, "{{prompt}}", prompt)
				hasPrompt = true
			}
			argv = append(argv, out)
		}
		inv.initial = commandSpec{argv: argv, stdinPrompt: !hasPrompt, prompt: prompt}
		return inv, nil
	}
	return invocation{}, fmt.Errorf("unknown harness %q", p.Harness)
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func codexSessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "thread.started" && ev.ThreadID != "" {
			return ev.ThreadID
		}
	}
	return ""
}

func openCodeSessionID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		var ev struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SessionID != "" {
			return ev.SessionID
		}
	}
	return ""
}

func openCodeOutput(stdout string) string {
	var lastMessage string
	parts := map[string][]string{}
	for _, line := range strings.Split(stdout, "\n") {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				MessageID string `json:"messageID"`
				Text      string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "text" || strings.TrimSpace(ev.Part.Text) == "" {
			continue
		}
		messageID := ev.Part.MessageID
		if messageID == "" {
			messageID = "_unknown"
		}
		lastMessage = messageID
		parts[messageID] = append(parts[messageID], strings.TrimSpace(ev.Part.Text))
	}
	return strings.Join(parts[lastMessage], "\n")
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
	if i := strings.Index(body, "\n[ORIGINAL TASK]\n"); i >= 0 {
		body = body[i+len("\n[ORIGINAL TASK]\n"):]
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

func hasCodexOutputArg(args []string) bool {
	for _, arg := range args {
		if arg == "-o" || strings.HasPrefix(arg, "-o=") ||
			(len(arg) > 2 && strings.HasPrefix(arg, "-o") && !strings.HasPrefix(arg, "--")) ||
			arg == "--output-last-message" || strings.HasPrefix(arg, "--output-last-message=") {
			return true
		}
	}
	return false
}

func hasAnyOption(args []string, options ...string) bool {
	for _, arg := range args {
		for _, option := range options {
			if arg == option || strings.HasPrefix(arg, option+"=") {
				return true
			}
		}
	}
	return false
}

func argumentValue(args []string, option string) (string, bool) {
	for i, arg := range args {
		if arg == option {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(arg, option+"=") {
			return strings.TrimPrefix(arg, option+"="), true
		}
	}
	return "", false
}

// codex exec resume accepts a narrower option set than codex exec. Do not
// recover when replaying a profile would either fail argument parsing or drop
// a security/provider/working-directory setting.
func codexResumeCompatible(args []string) bool {
	return !hasAnyOption(args,
		"--oss", "--local-provider", "-p", "--profile", "-s", "--sandbox",
		"-C", "--cd", "--add-dir", "--color", "-V", "--version",
	)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
