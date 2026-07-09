// Package runstore persists workflow runs: metadata, an append-only event
// journal (tailed live by the TUI), a journal of full agent results, and the
// final result.
package runstore

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Event is one line in events.jsonl.
type Event struct {
	T       string `json:"t"`  // run_start|phase|agent_start|agent_run|agent_end|log|run_end
	TS      int64  `json:"ts"` // unix millis
	Title   string `json:"title,omitempty"`
	ID      int    `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	Profile string `json:"profile,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Msg     string `json:"msg,omitempty"`
	Status  string `json:"status,omitempty"` // ok|error (agent_end), ok|error|canceled (run_end)
	DurMs   int64  `json:"durMs,omitempty"`
	Preview string `json:"preview,omitempty"`
	Error   string `json:"error,omitempty"`
	Cached  bool   `json:"cached,omitempty"` // satisfied from a resumed run's journal
	Dir     string `json:"dir,omitempty"`    // kept worktree path, when isolated
}

// JournalEntry is one line in journal.jsonl — the full record of one agent
// call, also used as the cache source for --resume.
type JournalEntry struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	Profile string `json:"profile"`
	Key     string `json:"key"` // hash of (profile, prompt, schema) for resume matching
	Prompt  string `json:"prompt"`
	Result  any    `json:"result"`
	Error   string `json:"error,omitempty"`
	Cached  bool   `json:"cached,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

// Meta is meta.json.
type Meta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running|ok|error|canceled
	Pid       int       `json:"pid,omitempty"`
	Args      any       `json:"args,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Run is an open, writable run directory.
type Run struct {
	Dir  string
	Meta Meta

	mu     sync.Mutex
	events *os.File
	journ  *os.File
}

func DataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "dyna")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "dyna")
}

func RunsDir() string { return filepath.Join(DataDir(), "runs") }

// NewID mints a run id; exported so a detaching parent can pre-assign one.
func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("wf_%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
}

// Create makes a new run directory and copies the script into it. A preset
// id via DYNA_RUN_ID (set by `dyna run --detach`) is honored.
func Create(name, scriptSrc string, args any) (*Run, error) {
	if name == "" {
		name = "workflow"
	}
	id := os.Getenv("DYNA_RUN_ID")
	if id == "" {
		id = NewID()
	}
	r := &Run{Meta: Meta{ID: id, Name: name, Status: "running", Pid: os.Getpid(), Args: args, StartedAt: time.Now()}}
	r.Dir = filepath.Join(RunsDir(), r.Meta.ID)
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "script.js"), []byte(scriptSrc), 0o644); err != nil {
		return nil, err
	}
	var err error
	if r.events, err = os.OpenFile(filepath.Join(r.Dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, err
	}
	if r.journ, err = os.OpenFile(filepath.Join(r.Dir, "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, err
	}
	if err := r.saveMeta(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Run) saveMeta() error {
	b, _ := json.MarshalIndent(r.Meta, "", "  ")
	return os.WriteFile(filepath.Join(r.Dir, "meta.json"), append(b, '\n'), 0o644)
}

func (r *Run) Append(e Event) {
	e.TS = time.Now().UnixMilli()
	b, _ := json.Marshal(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		r.events.Write(append(b, '\n'))
		r.events.Sync()
	}
}

// Journal records the full prompt/result of one agent call.
func (r *Run) Journal(e JournalEntry) {
	b, _ := json.Marshal(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.journ != nil {
		r.journ.Write(append(b, '\n'))
		r.journ.Sync()
	}
}

// ReadJournal parses journal.jsonl for a run (resume cache source).
func ReadJournal(id string) ([]JournalEntry, error) {
	f, err := os.Open(filepath.Join(RunsDir(), id, "journal.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []JournalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e JournalEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// ReadMeta reads one run's meta.json.
func ReadMeta(id string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(RunsDir(), id, "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// Finish closes the run, writing result.json and final meta.
func (r *Run) Finish(status string, resultJSON string, runErr error) {
	r.Meta.Status = status
	r.Meta.EndedAt = time.Now()
	if runErr != nil {
		r.Meta.Error = runErr.Error()
	}
	if resultJSON != "" {
		os.WriteFile(filepath.Join(r.Dir, "result.json"), []byte(resultJSON+"\n"), 0o644)
	}
	r.saveMeta()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		r.events.Close()
	}
	if r.journ != nil {
		r.journ.Close()
	}
}

// List returns metadata for all runs, newest first.
func List() ([]Meta, error) {
	entries, err := os.ReadDir(RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(RunsDir(), e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(b, &m) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// ReadEvents parses events.jsonl for a run.
func ReadEvents(id string) ([]Event, error) {
	f, err := os.Open(filepath.Join(RunsDir(), id, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// ReadResult returns result.json contents if present.
func ReadResult(id string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(RunsDir(), id, "result.json"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ---- lifecycle management (cancel / pause / remove) ----

func pausePath(id string) string { return filepath.Join(RunsDir(), id, "paused") }

// SetPaused toggles the pause flag; the engine blocks new worker launches
// while it exists (already-running workers finish).
func SetPaused(id string, paused bool) error {
	if paused {
		return os.WriteFile(pausePath(id), []byte("paused\n"), 0o644)
	}
	err := os.Remove(pausePath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func IsPaused(id string) bool {
	_, err := os.Stat(pausePath(id))
	return err == nil
}

// Cancel stops a running workflow: signals its process (the run's SIGTERM
// handler cancels the context, which kills worker process groups). If the
// process is already gone (crash, reboot), the run is finalized as canceled.
func Cancel(id string) error {
	m, err := ReadMeta(id)
	if err != nil {
		return err
	}
	if m.Status != "running" {
		return fmt.Errorf("run %s is not running (status %s)", id, m.Status)
	}
	SetPaused(id, false) // a paused run must be unblocked to observe cancellation
	if m.Pid > 0 && syscall.Kill(m.Pid, 0) == nil {
		return syscall.Kill(m.Pid, syscall.SIGTERM)
	}
	return ForceStatus(id, "canceled", "canceled (process no longer alive)")
}

// ForceStatus rewrites a run's terminal status directly (stale-run cleanup).
func ForceStatus(id, status, errMsg string) error {
	m, err := ReadMeta(id)
	if err != nil {
		return err
	}
	m.Status = status
	m.EndedAt = time.Now()
	m.Error = errMsg
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(RunsDir(), id, "meta.json"), append(b, '\n'), 0o644)
}

// Remove deletes a run's directory. Running runs must be canceled first.
func Remove(id string) error {
	m, err := ReadMeta(id)
	if err == nil && m.Status == "running" && m.Pid > 0 && syscall.Kill(m.Pid, 0) == nil {
		return fmt.Errorf("run %s is still running — cancel it first", id)
	}
	dir := filepath.Join(RunsDir(), id)
	if !strings.HasPrefix(id, "wf_") || strings.ContainsAny(id, "/.") {
		return fmt.Errorf("suspicious run id %q", id)
	}
	return os.RemoveAll(dir)
}

// ClearCompleted removes every non-running run and returns how many.
func ClearCompleted() (int, error) {
	runs, err := List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Status == "running" && r.Pid > 0 && syscall.Kill(r.Pid, 0) == nil {
			continue
		}
		if err := Remove(r.ID); err == nil {
			n++
		}
	}
	return n, nil
}
