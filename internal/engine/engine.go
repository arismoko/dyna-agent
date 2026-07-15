// Package engine runs a workflow script: a plain-JavaScript program using
// agent()/workflow()/parallel()/pipeline()/phase()/log() to orchestrate worker profiles.
// Scripts run on an embedded JS engine (goja); agent() calls fan out to real
// agent CLIs through the harness package, capped by a concurrency semaphore.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	ScriptSrc   string
	ScriptPath  string // invoking script path; relative workflow refs use its directory
	Args        any
	Store       *profile.Store
	Run         *runstore.Run        // persistence sink
	OnEvent     func(runstore.Event) // optional live sink (CLI progress)
	WorkDir     string               // cwd for workers
	MaxConc     int                  // 0 => min(16, cores-2)
	MaxAgents   int                  // lifetime agent() cap; 0 => 1000
	Cache       *Cache               // resume cache (nil = fresh run)
	WorkflowDir string               // optional name-keyed workflow registry directory
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
	mu sync.Mutex
	m  map[string][]any
}

// NewCache builds a resume cache from a previous run's journal (successful,
// non-isolated calls only; failures and worktree runs re-execute).
func NewCache(entries []runstore.JournalEntry) *Cache {
	c := &Cache{m: map[string][]any{}}
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
	return q[0], true
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
	return execute(ctx, o, nil, 0, "")
}

// sharedState owns every limit whose meaning is "per run", including calls
// made by nested workflows. Each script still gets an isolated JS runtime.
type sharedState struct {
	sem chan struct{}

	mu          sync.Mutex
	agentSeq    int
	workflowSeq int
	profSems    map[string]chan struct{}
	profCalls   map[string]int
	abortRoot   func(error)
}

func execute(ctx context.Context, o Options, shared *sharedState, depth int, workflowID string) (string, error) {
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
	if shared == nil {
		shared = &sharedState{
			sem: make(chan struct{}, maxConc), profSems: map[string]chan struct{}{}, profCalls: map[string]int{},
		}
	}
	eng := &engine{opts: o, shared: shared, depth: depth, workflowID: workflowID}

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
	if depth == 0 {
		shared.abortRoot = func(err error) {
			eng.settle(outcome{err: err})
			eng.cancel()
		}
	}

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
	shared     *sharedState
	depth      int
	workflowID string
	ctx        context.Context
	cancel     context.CancelFunc
	loop       *eventloop.EventLoop
	vm         *goja.Runtime // valid only on the loop thread
	done       chan outcome
	settleOnce sync.Once
	phaseMu    sync.Mutex
	curPhase   string
	workerMu   sync.Mutex
	workers    sync.WaitGroup
	stopping   bool
}

// settle delivers the run's single outcome (first caller wins).
func (e *engine) settle(o outcome) {
	e.settleOnce.Do(func() { e.done <- o })
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
	if ev.Workflow == "" {
		ev.Workflow = e.workflowID
	}
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
// public API (agent/workflow/parallel/pipeline/phase/log) on top of them.
func (e *engine) bind(vm *goja.Runtime) error {
	e.vm = vm
	vm.Set("__phase", func(title string) {
		e.phaseMu.Lock()
		e.curPhase = title
		e.phaseMu.Unlock()
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
	vm.Set("__workflow", e.workflow)

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
	e.shared.mu.Lock()
	e.shared.agentSeq++
	id := e.shared.agentSeq
	e.shared.mu.Unlock()
	e.phaseMu.Lock()
	if phase == "" {
		phase = e.curPhase
	}
	e.phaseMu.Unlock()
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
				e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Prompt: prompt, Result: cached, Cached: true, Workflow: e.workflowID})
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
		e.shared.mu.Lock()
		e.shared.profCalls[prof.Name]++
		n := e.shared.profCalls[prof.Name]
		e.shared.mu.Unlock()
		if n > prof.MaxCallsPerRun {
			err := fmt.Errorf("profile %q call limit exceeded (%d per run)", prof.Name, prof.MaxCallsPerRun)
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: err.Error()})
			e.emit(runstore.Event{T: "log", Msg: "aborting run: " + err.Error()})
			reject(err)
			e.shared.abortRoot(fmt.Errorf("workflow aborted: %v; size the fan-out within the profile's maxCallsPerRun (see `dyna profiles list --json`) or route bulk work to an unlimited profile", err))
			return vm.ToValue(promise)
		}
	}
	var profSem chan struct{}
	if prof.MaxConcurrent > 0 {
		e.shared.mu.Lock()
		profSem = e.shared.profSems[prof.Name]
		if profSem == nil {
			profSem = make(chan struct{}, prof.MaxConcurrent)
			e.shared.profSems[prof.Name] = profSem
		}
		e.shared.mu.Unlock()
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
		case e.shared.sem <- struct{}{}:
		case <-e.ctx.Done():
			failBeforeRun(e.ctx.Err())
			return
		}
		defer func() { <-e.shared.sem }()

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
				e.emit(runstore.Event{T: "log", Msg: fmt.Sprintf("%s kept worktree with changes: %s", label, cwd)})
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
			e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Prompt: prompt, Result: resultAny, Error: errStr, Dir: keptDir, Workflow: e.workflowID})
		}
		if err != nil {
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", DurMs: dur.Milliseconds(), Error: truncate(err.Error(), 2000), Dir: keptDir})
			reject(err)
			return
		}
		e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "ok", DurMs: dur.Milliseconds(), Preview: truncate(rawOut, 200), Dir: keptDir})
		resolve(resultAny)
	}()

	return vm.ToValue(promise)
}

// workflow implements one nested workflow() call. The child has its own JS
// runtime and phase state, but all paid-work limits live in sharedState.
func (e *engine) workflow(call goja.FunctionCall) goja.Value {
	vm := e.vm
	promise, resolveFn, rejectFn := vm.NewPromise()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
	}

	if e.depth >= 1 {
		reject(fmt.Errorf("workflow nesting limit exceeded: a nested workflow cannot call workflow() (maximum depth is 1)"))
		return vm.ToValue(promise)
	}
	ref := strings.TrimSpace(call.Argument(0).String())
	path, src, err := e.resolveWorkflow(ref)
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	var workflowArgs any
	if encoded := call.Argument(1); !goja.IsUndefined(encoded) {
		if err := json.Unmarshal([]byte(encoded.String()), &workflowArgs); err != nil {
			reject(fmt.Errorf("workflow args must be valid JSON: %w", err))
			return vm.ToValue(promise)
		}
	}

	e.shared.mu.Lock()
	e.shared.workflowSeq++
	workflowID := fmt.Sprintf("nested-%d", e.shared.workflowSeq)
	e.shared.mu.Unlock()
	e.phaseMu.Lock()
	parentPhase := e.curPhase
	e.phaseMu.Unlock()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	if e.opts.Run != nil {
		if err := e.opts.Run.StartWorkflow(workflowID, name, path, src, workflowArgs, parentPhase); err != nil {
			reject(fmt.Errorf("start nested workflow %q: %w", ref, err))
			return vm.ToValue(promise)
		}
	}
	e.emit(runstore.Event{
		T: "workflow_start", Workflow: workflowID, Parent: parentRunID(e.opts.Run),
		Title: name, Ref: path, Phase: parentPhase,
	})
	if !e.beginWorker() {
		err := e.ctx.Err()
		if err == nil {
			err = fmt.Errorf("workflow is already finishing")
		}
		if e.opts.Run != nil {
			if finishErr := e.opts.Run.FinishWorkflow(workflowID, "error", "", err); finishErr != nil {
				err = errors.Join(err, fmt.Errorf("finish nested workflow %q: %w", ref, finishErr))
			}
		}
		e.emit(runstore.Event{
			T: "workflow_end", Workflow: workflowID, Title: name, Phase: parentPhase,
			Status: "error", Error: errorString(err),
		})
		reject(err)
		return vm.ToValue(promise)
	}

	go func() {
		defer e.workers.Done()
		started := time.Now()
		childOpts := e.opts
		childOpts.ScriptSrc = src
		childOpts.ScriptPath = path
		childOpts.Args = workflowArgs
		resultJSON, runErr := execute(e.ctx, childOpts, e.shared, e.depth+1, workflowID)
		var result any
		if runErr == nil {
			if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
				runErr = fmt.Errorf("nested workflow %q returned invalid JSON: %w", ref, err)
			}
		}
		status := "ok"
		if runErr != nil {
			status = "error"
		}
		if e.opts.Run != nil {
			if finishErr := e.opts.Run.FinishWorkflow(workflowID, status, resultJSON, runErr); finishErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("finish nested workflow %q: %w", ref, finishErr))
				status = "error"
			}
		}
		e.emit(runstore.Event{
			T: "workflow_end", Workflow: workflowID, Title: name, Phase: parentPhase,
			Status: status, DurMs: time.Since(started).Milliseconds(), Error: errorString(runErr),
		})
		if runErr != nil {
			reject(fmt.Errorf("nested workflow %q failed: %w", ref, runErr))
			return
		}
		resolve(result)
	}()

	return vm.ToValue(promise)
}

func parentRunID(run *runstore.Run) string {
	if run == nil {
		return ""
	}
	return run.Meta.ID
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 2000)
}

func (e *engine) resolveWorkflow(ref string) (string, string, error) {
	if ref == "" {
		return "", "", fmt.Errorf("workflow reference must not be empty")
	}
	var candidates []string
	seen := map[string]bool{}
	add := func(base string) {
		if base == "" {
			return
		}
		for _, candidate := range []string{base, base + ".js"} {
			if filepath.Ext(base) == ".js" && candidate != base {
				continue
			}
			abs, err := filepath.Abs(candidate)
			if err == nil && !seen[abs] {
				seen[abs] = true
				candidates = append(candidates, abs)
			}
		}
	}

	if filepath.IsAbs(ref) {
		add(ref)
	} else {
		if e.opts.ScriptPath != "" {
			add(filepath.Join(filepath.Dir(e.opts.ScriptPath), ref))
		}
		add(filepath.Join(e.opts.WorkDir, ref))
		if filepath.Base(ref) == ref {
			registry := e.opts.WorkflowDir
			if registry == "" {
				registry = defaultWorkflowDir()
			}
			add(filepath.Join(registry, ref))
			add(filepath.Join(e.opts.WorkDir, "examples", ref))
		}
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(candidate)
		if err != nil {
			return "", "", fmt.Errorf("read nested workflow %s: %w", candidate, err)
		}
		return candidate, string(b), nil
	}
	return "", "", fmt.Errorf("workflow %q was not found as a script path or in %s or %s", ref, workflowRegistryLabel(e.opts.WorkflowDir), filepath.Join(e.opts.WorkDir, "examples"))
}

func defaultWorkflowDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "workflows")
}

func workflowRegistryLabel(dir string) string {
	if dir != "" {
		return dir
	}
	return defaultWorkflowDir()
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
	cleanup := func() bool {
		status, err := gitRun(context.Background(), wt, "status", "--porcelain")
		if err == nil && len(bytes.TrimSpace(status)) == 0 {
			gitRun(context.Background(), repoDir, "worktree", "remove", "--force", wt)
			return false
		}
		return true // keep: worker made changes (or status failed; do not destroy work)
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
