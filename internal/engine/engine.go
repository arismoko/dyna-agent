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

const defaultAgentTimeout = 30 * time.Minute

// Options configures one workflow execution.
type Options struct {
	ScriptSrc string
	Args      any
	Store     *profile.Store
	Run       *runstore.Run        // persistence sink
	OnEvent   func(runstore.Event) // optional live sink (CLI progress)
	WorkDir   string               // cwd for workers
	MaxConc   int                  // 0 => min(16, cores-2)
	MaxAgents int                  // lifetime agent() cap; 0 => 1000
	Cache     *Cache               // resume cache (nil = fresh run)
}

// Cache maps agent-call keys to prior results so a resumed run replays the
// unchanged prefix instantly. Same key called N times → N queued results,
// consumed in order.
type Cache struct {
	mu sync.Mutex
	m  map[string][]any
}

// NewCache builds a resume cache from a previous run's journal (successful,
// non-isolated calls only — failures and worktree runs re-execute).
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

	wrapped, err := transform(o.ScriptSrc)
	if err != nil {
		return "", err
	}

	loop := eventloop.NewEventLoop(eventloop.EnableConsole(false))
	done := make(chan outcome, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	eng.ctx = ctx
	eng.loop = loop

	loop.Start()
	defer loop.Stop()

	loop.RunOnLoop(func(vm *goja.Runtime) {
		console.Enable(vm)
		if err := eng.bind(vm, done); err != nil {
			done <- outcome{err: err}
			return
		}
		if _, err := vm.RunScript("workflow.js", wrapped); err != nil {
			done <- outcome{err: fmt.Errorf("script error: %w", err)}
		}
	})

	select {
	case res := <-done:
		return res.resultJSON, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type engine struct {
	opts      Options
	sem       chan struct{}
	ctx       context.Context
	loop      *eventloop.EventLoop
	vm        *goja.Runtime // valid only on the loop thread
	mu        sync.Mutex
	curPhase  string
	agentSeq  int
	profSems  map[string]chan struct{} // per-profile concurrency limiters
	profCalls map[string]int           // per-profile call counts this run
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
// __fail, args, profiles — then evaluates the JS prelude that builds the
// public API (agent/parallel/pipeline/phase/log) on top of them.
func (e *engine) bind(vm *goja.Runtime, done chan outcome) error {
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
	var once sync.Once
	vm.Set("__finish", func(resultJSON string) {
		once.Do(func() { done <- outcome{resultJSON: resultJSON} })
	})
	vm.Set("__fail", func(msg string) {
		once.Do(func() { done <- outcome{err: fmt.Errorf("workflow failed: %s", msg)} })
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
		profs = append(profs, map[string]any{
			"name": p.Name, "description": p.Description, "harness": p.Harness,
			"model": p.Model, "taste": p.Taste, "intelligence": p.Intelligence,
			"cost": p.Cost, "default": p.Default,
			"maxConcurrent": p.MaxConcurrent, "maxCallsPerRun": p.MaxCallsPerRun,
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
			reject(fmt.Errorf("unknown profile %q — run `dyna profiles list` to see registered profiles", profName))
			return vm.ToValue(promise)
		}
		prof = *p
	} else {
		p, ok := e.opts.Store.DefaultProfile()
		if !ok {
			reject(fmt.Errorf("no profiles registered — run `dyna profiles add` first"))
			return vm.ToValue(promise)
		}
		prof = *p
	}
	if prof.TimeoutSec > 0 && timeout == defaultAgentTimeout {
		timeout = time.Duration(prof.TimeoutSec) * time.Second
	}

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
		reject(fmt.Errorf("agent cap reached (%d) — raise with --max-agents", maxAgents))
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
	if prof.MaxCallsPerRun > 0 {
		e.mu.Lock()
		e.profCalls[prof.Name]++
		n := e.profCalls[prof.Name]
		e.mu.Unlock()
		if n > prof.MaxCallsPerRun {
			err := fmt.Errorf("profile %q call limit reached (%d per run)", prof.Name, prof.MaxCallsPerRun)
			e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: err.Error()})
			reject(err)
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

	go func() {
		if profSem != nil {
			select {
			case profSem <- struct{}{}:
			case <-e.ctx.Done():
				reject(e.ctx.Err())
				return
			}
			defer func() { <-profSem }()
		}
		select {
		case e.sem <- struct{}{}:
		case <-e.ctx.Done():
			reject(e.ctx.Err())
			return
		}
		defer func() { <-e.sem }()

		e.emit(runstore.Event{T: "agent_run", ID: id, Label: label, Profile: prof.Name, Phase: phase})
		start := time.Now()

		actx, cancel := context.WithTimeout(e.ctx, timeout)
		defer cancel()

		// Worktree isolation: run the worker on a detached copy of the repo;
		// keep the tree only if the worker changed something.
		keptDir := ""
		var cleanupWT func() bool
		if isolation == "worktree" {
			wt, cleanup, werr := addWorktree(actx, cwd)
			if werr != nil {
				e.emit(runstore.Event{T: "agent_end", ID: id, Label: label, Profile: prof.Name, Phase: phase, Status: "error", Error: truncate(werr.Error(), 2000)})
				reject(werr)
				return
			}
			cwd = wt
			cleanupWT = cleanup
		}

		var (
			resultAny any
			rawOut    string
			err       error
		)
		if schemaJSON != "" {
			resultAny, rawOut, err = e.runWithSchema(actx, prof, prompt, schemaJSON, cwd)
		} else {
			var r harness.Result
			r, err = harness.Run(actx, prof, prompt, cwd)
			rawOut = r.Output
			resultAny = r.Output
		}
		dur := time.Since(start)

		if cleanupWT != nil {
			if kept := cleanupWT(); kept {
				keptDir = cwd
				e.emit(runstore.Event{T: "log", Msg: fmt.Sprintf("%s kept worktree with changes: %s", label, cwd)})
			}
		}

		if e.opts.Run != nil {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Profile: prof.Name, Key: key, Prompt: prompt, Result: resultAny, Error: errStr, Dir: keptDir})
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

// runWithSchema wraps the prompt with JSON-output instructions, extracts and
// validates the JSON, retrying up to 2 times with the validation error.
func (e *engine) runWithSchema(ctx context.Context, prof profile.Profile, prompt, schemaJSON, cwd string) (any, string, error) {
	sch, err := jsonschema.CompileString("schema.json", schemaJSON)
	if err != nil {
		return nil, "", fmt.Errorf("invalid schema: %w", err)
	}
	base := prompt + "\n\n---\nOUTPUT FORMAT: Respond with ONLY a JSON value that validates against this JSON Schema — no prose, no markdown fences, no explanation:\n" + schemaJSON
	ask := base
	var lastErr error
	var raw string
	for attempt := 0; attempt < 3; attempt++ {
		r, err := harness.Run(ctx, prof, ask, cwd)
		if err != nil {
			return nil, r.Output, err
		}
		raw = r.Output
		val, jerr := extractJSON(raw)
		if jerr == nil {
			if verr := sch.Validate(val); verr == nil {
				return val, raw, nil
			} else {
				jerr = verr
			}
		}
		lastErr = jerr
		ask = base + "\n\nYour previous response was invalid: " + truncate(jerr.Error(), 800) +
			"\nPrevious response (truncated): " + truncate(raw, 1500) +
			"\nRespond again with ONLY the corrected JSON."
	}
	return nil, raw, fmt.Errorf("schema validation failed after 3 attempts: %w", lastErr)
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
		return true // keep: worker made changes (or status failed — don't destroy work)
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
	// Prefer fenced blocks if present.
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
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
		"r => __finish(JSON.stringify(r === undefined ? null : r) ?? 'null')," +
		" e => __fail(String((e && e.stack) || e)));", nil
}

var metaRe = regexp.MustCompile(`(?m)^\s*export\s+const\s+meta\s*=`)
