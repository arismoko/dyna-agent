// Package engine runs a workflow script: a plain-JavaScript program using
// agent()/parallel()/pipeline()/phase()/log() to orchestrate worker profiles.
// Scripts run on an embedded JS engine (goja); agent() calls fan out to real
// agent CLIs through the harness package, capped by a concurrency semaphore.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/console"
	"github.com/dop251/goja_nodejs/eventloop"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

const (
	minimumAgentTimeout = 30 * time.Minute
	defaultAgentTimeout = 5 * time.Hour
	defaultJournalIdle  = 5 * time.Minute
)

// Options configures one workflow execution.
type Options struct {
	ScriptSrc string
	Args      any
	Store     *profile.Store
	Run       *runstore.Run        // persistence sink
	OnEvent   func(runstore.Event) // optional live sink (CLI progress)
	OnWarning func(string)         // optional parse-time warning sink
	WorkDir   string               // cwd for workers
	MaxConc   int                  // 0 => min(16, cores-2)
	MaxAgents int                  // lifetime agent() cap; 0 => 1000
	Cache     *Cache               // resume cache (nil = fresh run)
	// JournalIdle controls how long a running worker may go without an
	// agent-authored progress record before an exact-session reminder. Zero
	// uses five minutes; exposed primarily as a deterministic test seam.
	JournalIdle time.Duration
	// Paused, when it returns true, blocks new worker launches (running
	// workers finish). Polled while waiting to start each agent.
	Paused func() bool
}

// Cache maps agent-call keys to prior results so a resumed run replays the
// unchanged prefix instantly. Same key called N times → N queued results,
// consumed in order.
type Cache struct {
	mu         sync.Mutex
	m          map[string][]any
	priorCalls int
	hits       int
}

// CacheStats summarizes replay use after a resumed run.
type CacheStats struct {
	Hits       int
	PriorCalls int
}

// NewCache builds a resume cache from a previous run's journal (successful,
// non-isolated calls only; failures and worktree runs re-execute).
func NewCache(entries []runstore.JournalEntry) *Cache {
	c := &Cache{m: map[string][]any{}, priorCalls: len(entries)}
	for _, e := range entries {
		if e.Error == "" && e.Key != "" && e.Dir == "" {
			c.m[e.Key] = append(c.m[e.Key], e.Result)
		}
	}
	return c
}

func (c *Cache) pop(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.m[key]
	if len(q) == 0 {
		return nil, false
	}
	c.m[key] = q[1:]
	c.hits++
	return q[0], true
}

// Stats returns the number of cache hits in this run and the total number of
// calls journaled by the prior run.
func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Hits: c.hits, PriorCalls: c.priorCalls}
}

// callKey identifies an agent call for resume matching.
func callKey(profileName, prompt, schemaJSON string) string {
	h := sha256.Sum256([]byte(profileName + "\x00" + prompt + "\x00" + schemaJSON))
	return hex.EncodeToString(h[:16])
}

type outcome struct {
	resultJSON string
	err        error
}

// Execute runs the script to completion and returns the workflow's return
// value as JSON.
func Execute(ctx context.Context, o Options) (string, error) {
	maxConc := o.MaxConc
	if maxConc <= 0 {
		maxConc = runtime.NumCPU() - 2
		if maxConc > 16 {
			maxConc = 16
		}
		if maxConc < 2 {
			maxConc = 2
		}
	}
	eng := &engine{
		opts: o, sem: make(chan struct{}, maxConc),
		profSems: map[string]chan struct{}{}, profCalls: map[string]int{},
	}

	if warning := resumeNondeterminismWarning(o.ScriptSrc); warning != "" && o.OnWarning != nil {
		o.OnWarning(warning)
	}
	wrapped, err := transform(o.ScriptSrc)
	if err != nil {
		return "", err
	}

	loop := eventloop.NewEventLoop(eventloop.EnableConsole(false))
	eng.done = make(chan outcome, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	eng.ctx = ctx
	eng.cancel = cancel
	eng.loop = loop

	loop.Start()
	defer loop.Stop()

	loop.RunOnLoop(func(vm *goja.Runtime) {
		console.Enable(vm)
		if err := eng.bind(vm); err != nil {
			eng.settle(outcome{err: err})
			return
		}
		if _, err := vm.RunScript("workflow.js", wrapped); err != nil {
			eng.settle(outcome{err: fmt.Errorf("script error: %w", err)})
		}
	})

	select {
	case res := <-eng.done:
		eng.stopWorkers()
		return res.resultJSON, res.err
	case <-ctx.Done():
		// abort() settles before canceling, so preserve its concrete diagnostic
		// instead of racing it into a generic context cancellation.
		select {
		case res := <-eng.done:
			eng.stopWorkers()
			return res.resultJSON, res.err
		default:
		}
		eng.stopWorkers()
		return "", ctx.Err()
	}
}

type engine struct {
	opts       Options
	sem        chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	loop       *eventloop.EventLoop
	vm         *goja.Runtime // valid only on the loop thread
	done       chan outcome
	settleOnce sync.Once
	mu         sync.Mutex
	curPhase   string
	agentSeq   int
	profSems   map[string]chan struct{} // per-profile concurrency limiters
	profCalls  map[string]int           // per-profile call counts this run
	workerMu   sync.Mutex
	workers    sync.WaitGroup
	stopping   bool
}

// settle delivers the run's single outcome (first caller wins).
func (e *engine) settle(o outcome) {
	e.settleOnce.Do(func() { e.done <- o })
}

// abort fails the whole run and cancels every in-flight worker. Used when
// continuing would only produce a degraded result (e.g. a profile cap hit).
func (e *engine) abort(err error) {
	e.settle(outcome{err: err})
	e.cancel()
}

func (e *engine) beginWorker() bool {
	e.workerMu.Lock()
	defer e.workerMu.Unlock()
	if e.stopping || e.ctx.Err() != nil {
		return false
	}
	e.workers.Add(1)
	return true
}

// stopWorkers prevents new launches, cancels every in-flight harness, and
// waits for their terminal journal/event writes before the caller closes Run.
func (e *engine) stopWorkers() {
	e.workerMu.Lock()
	e.stopping = true
	e.cancel()
	e.workerMu.Unlock()
	e.workers.Wait()
}

func (e *engine) emit(ev runstore.Event) {
	if e.opts.Run != nil {
		e.opts.Run.Append(ev)
	}
	if e.opts.OnEvent != nil {
		ev.TS = time.Now().UnixMilli()
		e.opts.OnEvent(ev)
	}
}

// bind installs the Go-backed globals: __spawn, __phase, __log, __finish,
// __fail, args, profiles, then evaluates the JS prelude that builds the
// public API (agent/parallel/pipeline/phase/log) on top of them.
func (e *engine) bind(vm *goja.Runtime) error {
	e.vm = vm
	vm.Set("__phase", func(title string) {
		e.mu.Lock()
		e.curPhase = title
		e.mu.Unlock()
		e.emit(runstore.Event{T: "phase", Title: title})
	})
	vm.Set("__log", func(msg string) {
		e.emit(runstore.Event{T: "log", Msg: truncate(msg, 4000)})
	})
	vm.Set("__finish", func(resultJSON string) {
		e.settle(outcome{resultJSON: resultJSON})
	})
	vm.Set("__fail", func(msg string) {
		e.settle(outcome{err: fmt.Errorf("workflow failed: %s", msg)})
	})
	vm.Set("__spawn", e.spawn)

	argsV := goja.Undefined()
	if e.opts.Args != nil {
		argsV = vm.ToValue(e.opts.Args)
	}
	vm.Set("args", argsV)

	// Expose the registry (read-only copy) so scripts can pick workers by
	// stats: profiles.find(p => p.taste >= 4) etc.
	profs := make([]map[string]any, 0, len(e.opts.Store.Profiles))
	for _, p := range e.opts.Store.Profiles {
		if p.Disabled {
			continue
		}
		profs = append(profs, map[string]any{
			"name": p.Name, "description": p.Description, "harness": p.Harness,
			"model": p.Model, "taste": p.Taste, "intelligence": p.Intelligence,
			"cost": p.Cost, "default": p.Default,
			"disableSubagents": p.DisableSubagents,
			"maxConcurrent":    p.MaxConcurrent, "maxCallsPerRun": p.MaxCallsPerRun,
		})
	}
	vm.Set("profiles", vm.ToValue(profs))

	_, err := vm.RunScript("prelude.js", prelude)
	return err
}

// spawn implements one agent() call. Called on the loop thread; returns a
// Promise resolved from a worker goroutine.
func (e *engine) spawn(call goja.FunctionCall) goja.Value {
	vm := e.vm // spawn always runs on the loop thread
	promise, resolveFn, rejectFn := vm.NewPromise()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
	}

	prompt := call.Argument(0).String()
	opts := call.Argument(1)

	profName, label, phase, cwd := "", "", "", e.opts.WorkDir
	timeout := defaultAgentTimeout
	timeoutSet := false
	var schemaJSON, isolation string
	if o, ok := opts.Export().(map[string]any); ok && o != nil {
		if v, ok := o["profile"].(string); ok {
			profName = v
		}
		if v, ok := o["label"].(string); ok {
			label = v
		}
		if v, ok := o["phase"].(string); ok {
			phase = v
		}
		if v, ok := o["cwd"].(string); ok && v != "" {
			cwd = v
		}
		if v, ok := o["isolation"].(string); ok {
			isolation = v
		}
		if v, ok := o["timeout"]; ok {
			if f, ok := toFloat(v); ok && f > 0 {
				timeout = time.Duration(f * float64(time.Second))
				timeoutSet = true
			}
		}
		if v, ok := o["schema"]; ok && v != nil {
			b, err := json.Marshal(v)
			if err == nil {
				schemaJSON = string(b)
			}
		}
	}

	var prof profile.Profile
	if profName != "" {
		p, ok := e.opts.Store.Get(profName)
		if !ok {
			reject(fmt.Errorf("unknown profile %q; run `dyna profiles list` to see registered profiles", profName))
			return vm.ToValue(promise)
		}
		if p.Disabled {
			reject(fmt.Errorf("profile %q is disabled; the user must enable it (`dyna profiles enable %s`); pick another from the profiles global", profName, profName))
			return vm.ToValue(promise)
		}
		prof = *p
	} else {
		p, ok := e.opts.Store.DefaultProfile()
		if !ok {
			reject(fmt.Errorf("no profiles registered; run `dyna profiles add` first"))
			return vm.ToValue(promise)
		}
		prof = *p
	}
	timeout = resolveAgentTimeout(timeout, timeoutSet, prof)

	if isolation != "" && isolation != "worktree" {
		reject(fmt.Errorf("unknown isolation %q (only \"worktree\" is supported)", isolation))
		return vm.ToValue(promise)
	}

	maxAgents := e.opts.MaxAgents
	if maxAgents <= 0 {
		maxAgents = 1000
	}
	e.mu.Lock()
	e.agentSeq++
	id := e.agentSeq
	if phase == "" {
		phase = e.curPhase
	}
	e.mu.Unlock()
	if id > maxAgents {
		reject(fmt.Errorf("agent cap reached (%d); raise with --max-agents", maxAgents))
		return vm.ToValue(promise)
	}
	if label == "" {
		label = fmt.Sprintf("agent-%d", id)
	}

	key := callKey(prof.Name, prompt, schemaJSON)
	e.emit(runstore.Event{T: "agent_start", ID: id, Label: label, Profile: prof.Name, Phase: phase, Preview: truncate(prompt, 160)})

	// Resume: replay a cached result without consuming a worker slot.
	if e.opts.Cache != nil {
		if cached, ok := e.opts.Cache.pop(key); ok {
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase,
				Status: "ok", Cached: true, Preview: truncate(fmt.Sprintf("%v", cached), 200)})
			if e.opts.Run != nil {
				e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Prompt: prompt, Result: cached, Cached: true})
			}
			resolve(cached)
			return vm.ToValue(promise)
		}
	}

	// Per-profile limits: users cap how hard a profile may be leaned on
	// (e.g. don't let a workflow spin up 50 concurrent expensive workers).
	// Exceeding a call cap aborts the WHOLE run: a workflow that silently
	// drops calls would produce a degraded result while still spending money.
	if prof.MaxCallsPerRun > 0 {
		e.mu.Lock()
		e.profCalls[prof.Name]++
		n := e.profCalls[prof.Name]
		e.mu.Unlock()
		if n > prof.MaxCallsPerRun {
			err := fmt.Errorf("profile %q call limit exceeded (%d per run)", prof.Name, prof.MaxCallsPerRun)
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: err.Error()})
			e.emit(runstore.Event{T: "log", Msg: "aborting run: " + err.Error()})
			reject(err)
			e.abort(fmt.Errorf("workflow aborted: %v; size the fan-out within the profile's maxCallsPerRun (see `dyna profiles list --json`) or route bulk work to an unlimited profile", err))
			return vm.ToValue(promise)
		}
	}
	var profSem chan struct{}
	if prof.MaxConcurrent > 0 {
		e.mu.Lock()
		profSem = e.profSems[prof.Name]
		if profSem == nil {
			profSem = make(chan struct{}, prof.MaxConcurrent)
			e.profSems[prof.Name] = profSem
		}
		e.mu.Unlock()
	}

	if !e.beginWorker() {
		err := e.ctx.Err()
		if err == nil {
			err = fmt.Errorf("workflow is already finishing")
		}
		e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(err.Error(), 2000)})
		reject(err)
		return vm.ToValue(promise)
	}
	failBeforeRun := func(err error) {
		e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(err.Error(), 2000)})
		reject(err)
	}
	go func() {
		defer e.workers.Done()
		// Pause gate: hold new launches while the run is paused.
		for e.opts.Paused != nil && e.opts.Paused() {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-e.ctx.Done():
				failBeforeRun(e.ctx.Err())
				return
			}
		}
		if profSem != nil {
			select {
			case profSem <- struct{}{}:
			case <-e.ctx.Done():
				failBeforeRun(e.ctx.Err())
				return
			}
			defer func() { <-profSem }()
		}
		select {
		case e.sem <- struct{}{}:
		case <-e.ctx.Done():
			failBeforeRun(e.ctx.Err())
			return
		}
		defer func() { <-e.sem }()

		e.emit(runstore.Event{T: "agent_run", ID: id, Label: label, Profile: prof.Name, Phase: phase})
		start := time.Now()

		actx, cancel := context.WithTimeout(e.ctx, timeout)
		defer cancel()

		journalPath := ""
		if e.opts.Run != nil {
			var journalErr error
			journalPath, journalErr = e.opts.Run.StartAgentJournal(id, label, prof.Name, phase, prompt)
			if journalErr != nil {
				e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(journalErr.Error(), 2000)})
				reject(fmt.Errorf("start work journal for %s: %w", label, journalErr))
				return
			}
			prof.Env = cloneEnv(prof.Env)
			prof.Env[runstore.AgentJournalEnv] = journalPath
			if runRoot, rootErr := filepath.Abs(e.opts.Run.Dir); rootErr == nil {
				prof.Env[runstore.AgentJournalRootEnv] = runRoot
			}
			if exe, exeErr := os.Executable(); exeErr == nil {
				prof.Env["DYNA_BIN"] = exe
			}
		}

		// Worktree isolation: run the worker on a detached copy of the repo;
		// keep the tree only if the worker changed something.
		keptDir := ""
		worktreeStatus := ""
		var cleanupWT func() bool
		if isolation == "worktree" {
			wt, cleanup, werr := addWorktree(actx, cwd)
			if werr != nil {
				if e.opts.Run != nil && journalPath != "" {
					_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: "error", Message: truncate(werr.Error(), 4000)})
				}
				e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(werr.Error(), 2000)})
				reject(werr)
				return
			}
			cwd = wt
			cleanupWT = cleanup
		}

		journalIdle := e.opts.JournalIdle
		if journalIdle <= 0 {
			journalIdle = defaultJournalIdle
		}
		journalOpts := harness.JournalOptions{
			Path: journalPath, IdleAfter: journalIdle, State: harness.NewJournalState(),
		}
		steeringOpts := harness.SteeringOptions{}
		if journalPath != "" {
			nudgeSent := make(map[string]bool)
			journalOpts.OnEntry = func(entry runstore.AgentJournalEntry) {
				nudgeSent[harness.JournalNudgeIdle] = false
				e.emit(runstore.Event{
					T: "agent_journal", ID: id, Label: label, Profile: prof.Name, Phase: phase,
					Kind: entry.Kind, Preview: truncate(entry.Message, 240),
				})
			}
			journalOpts.OnNudge = func(nudge harness.JournalNudge) {
				wasSent := nudgeSent[nudge.Reason]
				if nudge.Delivered {
					nudgeSent[nudge.Reason] = true
				}
				msg := "journal quiet for five minutes; exact-session write-and-continue reminder sent"
				next := "Write a progress entry, then continue the original task."
				status := "sent"
				if nudge.Reason == harness.JournalNudgeMissing {
					msg = "worker finished without an agent-authored entry; exact-session write-now reminder sent"
					next = "Write one brief entry; preserve the completed task result."
				}
				if !nudge.Delivered {
					status = "unavailable"
					if wasSent {
						status = "ignored"
						msg = "exact-session journal reminder was delivered, but the worker still wrote no agent-authored entry"
						next = "No further reminder will be sent for this completed task."
					} else if nudge.Reason == harness.JournalNudgeMissing {
						msg = "worker finished without an agent-authored entry; a safe exact-session reminder was unavailable or failed"
					} else {
						msg = "journal quiet for five minutes; live reminder unavailable because this harness/session cannot be safely resumed"
					}
				}
				_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{
					Kind: "nudge", Message: msg, Next: next,
				})
				e.emit(runstore.Event{
					T: "agent_nudge", ID: id, Label: label, Profile: prof.Name, Phase: phase,
					Kind: nudge.Reason, Status: status, Msg: msg,
				})
			}
			steeringOpts = harness.SteeringOptions{
				RunID: e.opts.Run.Meta.ID, AgentID: id,
				OnDispatch: func(message runstore.SteeringMessage) error {
					return e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{
						Kind: "steer", Message: message.Message,
					})
				},
				OnMessage: func(message runstore.SteeringMessage) {
					e.emit(runstore.Event{
						T: "agent_steer", ID: id, Label: label, Profile: prof.Name, Phase: phase,
						Status: "delivered", Msg: truncate(message.Message, 2000), Preview: truncate(message.Message, 240),
					})
				},
			}
		}
		workerTask := prompt
		if prof.DisableSubagents {
			workerTask = disableSubagentsWorkerPrompt(prompt)
		}
		workerPrompt := workerTask
		if journalPath != "" {
			workerPrompt = journalWorkerPrompt(workerTask, journalPath)
		}

		var (
			resultAny any
			rawOut    string
			nudged    bool
			err       error
		)
		if schemaJSON != "" {
			resultAny, rawOut, nudged, err = e.runWithSchema(actx, prof, workerPrompt, schemaJSON, cwd, journalOpts, steeringOpts)
		} else {
			var r harness.Result
			r, err = harness.RunWithJournalAndSteering(actx, prof, workerPrompt, cwd, true, journalOpts, steeringOpts)
			rawOut = r.Output
			resultAny = r.Output
			nudged = r.Nudged
		}
		dur := time.Since(start)
		if nudged && err == nil {
			e.emit(runstore.Event{T: "log", Msg: fmt.Sprintf("%s recovered by nudging the same %s session", label, prof.Harness)})
		}

		if cleanupWT != nil {
			if kept := cleanupWT(); kept {
				keptDir = cwd
				worktreeStatus = "kept"
				e.emit(runstore.Event{T: "log", Msg: fmt.Sprintf("%s kept worktree: %s", label, cwd)})
			} else {
				worktreeStatus = "removed"
			}
		}
		if e.opts.Run != nil && journalPath != "" {
			kind, msg := "complete", "Agent completed the task."
			if err != nil {
				kind, msg = "error", truncate(err.Error(), 4000)
			}
			_ = e.opts.Run.AppendAgentJournal(id, runstore.AgentJournalEntry{Kind: kind, Message: msg})
		}

		if e.opts.Run != nil {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Prompt: prompt, Result: resultAny, Error: errStr, Dir: keptDir, Worktree: worktreeStatus})
		}
		if err != nil {
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", DurMs: dur.Milliseconds(), Error: truncate(err.Error(), 2000), Dir: keptDir, Worktree: worktreeStatus})
			reject(err)
			return
		}
		e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "ok", DurMs: dur.Milliseconds(), Preview: truncate(rawOut, 200), Dir: keptDir, Worktree: worktreeStatus})
		resolve(resultAny)
	}()

	return vm.ToValue(promise)
}

func clampAgentTimeout(timeout time.Duration) time.Duration {
	if timeout < minimumAgentTimeout {
		return minimumAgentTimeout
	}
	return timeout
}

func resolveAgentTimeout(timeout time.Duration, timeoutSet bool, prof profile.Profile) time.Duration {
	if prof.TimeoutSec > 0 && !timeoutSet {
		timeout = time.Duration(prof.TimeoutSec) * time.Second
	}
	return clampAgentTimeout(timeout)
}

// runWithSchema wraps the prompt with JSON-output instructions, extracts and
// validates the JSON, retrying up to 2 times with the validation error.
func (e *engine) runWithSchema(ctx context.Context, prof profile.Profile, prompt, schemaJSON, cwd string, journal harness.JournalOptions, steering harness.SteeringOptions) (any, string, bool, error) {
	sch, err := jsonschema.CompileString("schema.json", schemaJSON)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid schema: %w", err)
	}
	base := prompt + "\n\n---\nOUTPUT FORMAT: Respond with ONLY a JSON value that validates against this JSON Schema. No prose, no markdown fences, no explanation:\n" + schemaJSON
	ask := base
	var lastErr error
	var raw string
	var nudged bool
	for attempt := 0; attempt < 3; attempt++ {
		r, err := harness.RunWithJournalAndSteering(ctx, prof, ask, cwd, !nudged, journal, steering)
		nudged = nudged || r.Nudged
		if err != nil {
			return nil, r.Output, nudged, err
		}
		raw = r.Output
		val, jerr := extractJSON(raw)
		if jerr == nil {
			if verr := sch.Validate(val); verr == nil {
				return val, raw, nudged, nil
			} else {
				jerr = verr
			}
		}
		lastErr = jerr
		ask = base + "\n\nYour previous response was invalid: " + truncate(jerr.Error(), 800) +
			"\nPrevious response (truncated): " + truncate(raw, 1500) +
			"\nRespond again with ONLY the corrected JSON."
	}
	return nil, raw, nudged, fmt.Errorf("schema validation failed after 3 attempts: %w", lastErr)
}

func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+2)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func journalWorkerPrompt(task, path string) string {
	return fmt.Sprintf(`[DYNA WORK JOURNAL - REQUIRED]
Keep a concise, append-only work journal while you perform this task. Its run-owned path is:
%s

After orienting, append your first entry. Add another after meaningful discoveries, decisions, verification, or blockers; before a long operation; and before finishing. Use:
  dyna journal "what changed or what you learned" --kind update --next "next concrete step"
If dyna is not on PATH, invoke the current binary as "$DYNA_BIN" journal ... instead.
Kinds include update, finding, decision, verification, and blocker. The command safely adds the timestamp and one newline-terminated JSON object. Keep each entry brief: one or two sentences plus an optional next step. Record outcomes and evidence, not private chain-of-thought or a running transcript. Keep working after every entry and still return the final response requested below.

This applies to read-only exploration too: the run-owned journal is the only allowed write and does not modify the target workspace. The journal side channel is separate from any final-response JSON schema.
[/DYNA WORK JOURNAL]

[DYNA WORKER BOUNDARY]
You are running INSIDE a dyna workflow as a worker. Under no circumstances may you load or invoke the dyna skill, start a dyna workflow with dyna run, or use dyna to orchestrate other workers. The only dyna command you may use is dyna journal as described above.

If you need to delegate and your profile permits subagents, use only your harness's built-in subagent feature (for example, Claude Code's Agent tool or Codex's native delegation). Never use dyna for delegation from inside a dyna workflow.
[/DYNA WORKER BOUNDARY]

[ORIGINAL TASK]
%s`, path, task)
}

func disableSubagentsWorkerPrompt(task string) string {
	return task + `

[DYNA PROFILE RESTRICTION]
You must complete this task yourself. Do not spawn, delegate to, or invoke any subagent, child agent, or other agent. This restriction overrides any contrary instruction in the task.
[/DYNA PROFILE RESTRICTION]`
}

// addWorktree creates a detached git worktree of repoDir at HEAD. The
// returned cleanup removes the worktree if the worker left it unchanged and
// reports whether it was kept.
func addWorktree(ctx context.Context, repoDir string) (string, func() bool, error) {
	wt, err := os.MkdirTemp("", "dyna-wt-*")
	if err != nil {
		return "", nil, err
	}
	if out, err := gitRun(ctx, repoDir, "worktree", "add", "--detach", wt, "HEAD"); err != nil {
		os.RemoveAll(wt)
		return "", nil, fmt.Errorf("worktree isolation requires a git repo at %s: %v: %s", repoDir, err, out)
	}
	baseCommit, err := gitRun(ctx, wt, "rev-parse", "HEAD")
	if err != nil {
		gitRun(context.Background(), repoDir, "worktree", "remove", "--force", wt)
		os.RemoveAll(wt)
		return "", nil, fmt.Errorf("resolve isolated worktree base at %s: %w", wt, err)
	}
	baseCommit = bytes.TrimSpace(baseCommit)
	cleanup := func() bool {
		status, err := gitRun(context.Background(), wt, "status", "--porcelain")
		if err != nil || len(bytes.TrimSpace(status)) != 0 {
			return true // keep: worker changed files, or status failed
		}
		head, err := gitRun(context.Background(), wt, "rev-parse", "HEAD")
		if err != nil || !bytes.Equal(bytes.TrimSpace(head), baseCommit) {
			return true // keep: worker committed changes, or HEAD inspection failed
		}
		if _, err := gitRun(context.Background(), repoDir, "worktree", "remove", "--force", wt); err != nil {
			return true // removal failed, so the worktree still exists
		}
		return false
	}
	return wt, cleanup, nil
}

func gitRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	return cmd.CombinedOutput()
}

// extractJSON finds the first JSON value in model output, tolerating prose
// and markdown fences around it.
func extractJSON(s string) (any, error) {
	s = strings.TrimSpace(s)
	// A response that is already a JSON value wins: markdown fences inside its
	// strings (e.g. code examples in a report field) must not be re-extracted.
	if v, ok := decodeWholeJSON(s); ok {
		return v, nil
	}
	// Otherwise prefer fenced blocks over prose.
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		if v, ok := decodeWholeJSON(strings.TrimSpace(m[1])); ok {
			return v, nil
		}
	}
	return decodeJSONFrom(s)
}

func decodeWholeJSON(s string) (any, bool) {
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

func decodeJSONFrom(s string) (any, error) {
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, fmt.Errorf("no JSON object/array found in output")
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}
	return v, nil
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace for one-line previews
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// transform rewrites `export const meta = {...}` into a global assignment and
// wraps the whole script in an async IIFE whose settlement finishes the run.
func transform(src string) (string, error) {
	src = metaRe.ReplaceAllString(src, "const meta = globalThis.__meta =")
	return "(async () => {\n" + src + "\n})().then(" +
		"r => __finish(JSON.stringify(r === undefined ? null : r) ?? 'null'))" +
		".catch(e => __fail(String((e && e.stack) || e)));", nil
}

var metaRe = regexp.MustCompile(`(?m)^\s*export\s+const\s+meta\s*=`)

var resumeNondeterminismPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{name: "Date.now()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])Date\s*\.\s*now\s*\(`)},
	{name: "new Date()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])new\s+Date\s*\(`)},
	{name: "Math.random()", re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}_$])Math\s*\.\s*random\s*\(`)},
}

func resumeNondeterminismWarning(src string) string {
	var found []string
	for _, pattern := range resumeNondeterminismPatterns {
		if pattern.re.MatchString(src) {
			found = append(found, pattern.name)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("workflow uses resume-unstable JavaScript APIs (%s); --resume cache hits may not work as expected for agent() calls whose prompt or schema depends on those values; pass timestamps or random seeds through args instead", strings.Join(found, ", "))
}
