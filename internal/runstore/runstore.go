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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Event is one line in events.jsonl.
type Event struct {
	T       string `json:"t"`  // run_start|phase|agent_start|agent_run|agent_journal|agent_nudge|agent_end|log|run_end
	TS      int64  `json:"ts"` // unix millis
	Title   string `json:"title,omitempty"`
	ID      int    `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	Profile string `json:"profile,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Msg     string `json:"msg,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Status  string `json:"status,omitempty"` // agent_end/run_end status, or sent|unavailable|ignored for agent_nudge
	DurMs   int64  `json:"durMs,omitempty"`
	Preview string `json:"preview,omitempty"`
	Error   string `json:"error,omitempty"`
	Cached  bool   `json:"cached,omitempty"` // satisfied from a resumed run's journal
	Dir     string `json:"dir,omitempty"`    // kept worktree path, when isolated
}

// JournalEntry is one line in journal.jsonl: the full record of one agent
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

// AgentJournalEnv is injected into a worker process with the absolute path of
// that agent's append-only journal.
const AgentJournalEnv = "DYNA_AGENT_JOURNAL"

// SessionEnv attributes a workflow to the interactive harness session that
// launched it.
const SessionEnv = "DYNA_SESSION"

// AgentJournalRootEnv pins journal-path validation to the current run even if
// a worker profile overrides HOME or XDG_DATA_HOME.
const AgentJournalRootEnv = "DYNA_AGENT_JOURNAL_ROOT"

const (
	agentJournalMaxRecordBytes  = 64 * 1024 * 1024
	agentJournalMaxMessageBytes = 4096
	agentJournalMaxKindBytes    = 32
)

// AgentJournalEntry is one line in agents/<numeric-id>/journal.jsonl. Start
// records carry the system metadata needed to understand the worker in
// isolation; subsequent agent and system records normally use only the first
// five fields.
type AgentJournalEntry struct {
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
	Source  string `json:"source"`

	AgentID int    `json:"agentId,omitempty"`
	Label   string `json:"label,omitempty"`
	Profile string `json:"profile,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// Meta is meta.json.
type Meta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running|ok|error|canceled
	Session   string    `json:"session,omitempty"`
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
	if err := validateRunID(id); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	r := &Run{Meta: Meta{ID: id, Name: name, Status: "running", Pid: os.Getpid(), Args: args, StartedAt: time.Now()}}
	if session := os.Getenv(SessionEnv); session != "" {
		if len(session) > 128 {
			session = session[:128]
		}
		r.Meta.Session = session
	}
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

// AgentJournalPath returns the canonical absolute journal path for an agent in
// a stored run. Both identifiers are validated before any filesystem access.
func AgentJournalPath(runID string, agentID int) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(RunsDir(), runID, "agents", strconv.Itoa(agentID), "journal.jsonl"))
}

func (r *Run) agentJournalPath(agentID int) (string, error) {
	if r == nil || r.Dir == "" {
		return "", fmt.Errorf("run has no directory")
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(r.Dir, "agents", strconv.Itoa(agentID), "journal.jsonl"))
	if err != nil {
		return "", err
	}
	if _, err := agentIDFromJournalPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// StartAgentJournal creates an agent journal and writes its first, system-
// authored record. Prompt is stored in full; it is deliberately not the
// truncated preview used by events.jsonl.
func (r *Run) StartAgentJournal(agentID int, label, profile, phase, prompt string) (string, error) {
	path, err := r.agentJournalPath(agentID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create agent journal directory: %w", err)
	}
	entry := AgentJournalEntry{
		TS: time.Now().UnixMilli(), Kind: "start", Message: "Agent started", Source: "system",
		AgentID: agentID, Label: label, Profile: profile, Phase: phase, Prompt: prompt,
	}
	if err := writeAgentJournalRecord(path, entry, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY); err != nil {
		return "", err
	}
	return path, nil
}

// AppendAgentJournal appends a timestamped, system-authored record to an
// existing agent journal.
func (r *Run) AppendAgentJournal(agentID int, entry AgentJournalEntry) error {
	path, err := r.agentJournalPath(agentID)
	if err != nil {
		return err
	}
	entry.TS = time.Now().UnixMilli()
	entry.Source = "system"
	return AppendAgentJournalPath(path, entry)
}

// AppendAgentJournalPath appends a caller-authored record to an existing
// absolute agent-journal path, adding the current timestamp when TS is zero.
// The complete JSONL record is issued in one O_APPEND write so concurrent
// worker and monitor entries cannot overwrite one another.
func AppendAgentJournalPath(path string, entry AgentJournalEntry) error {
	if entry.TS == 0 {
		entry.TS = time.Now().UnixMilli()
	}
	return writeAgentJournalRecord(path, entry, os.O_APPEND|os.O_WRONLY)
}

// AppendAgentJournalFromEnv appends one agent-authored update to the journal
// path injected into the worker environment.
func AppendAgentJournalFromEnv(kind, message, next string) error {
	path := os.Getenv(AgentJournalEnv)
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s is not set; `dyna journal` is only available inside a dyna worker", AgentJournalEnv)
	}
	kind = strings.TrimSpace(kind)
	switch kind {
	case "update", "finding", "decision", "verification", "blocker":
	default:
		return fmt.Errorf("agent journal kind must be one of: update, finding, decision, verification, blocker")
	}
	entry := AgentJournalEntry{
		TS: time.Now().UnixMilli(), Kind: kind, Message: message, Next: next, Source: "agent",
	}
	return writeAgentJournalRecord(path, entry, os.O_APPEND|os.O_WRONLY)
}

// ReadAgentJournalFrom parses complete agent-journal records starting at
// offset. Invalid complete records are skipped and committed; a partial
// trailing record remains uncommitted until its newline arrives.
func ReadAgentJournalFrom(runID string, agentID int, offset int64) ([]AgentJournalEntry, int64, error) {
	path, err := AgentJournalPath(runID, agentID)
	if err != nil {
		return nil, offset, err
	}
	return ReadAgentJournalPathFrom(path, offset)
}

// ReadAgentJournalPathFrom is ReadAgentJournalFrom for the absolute path held
// by a harness monitor.
func ReadAgentJournalPathFrom(path string, offset int64) ([]AgentJournalEntry, int64, error) {
	if _, err := agentIDFromJournalPath(path); err != nil {
		return nil, offset, err
	}
	if err := validateAgentJournalFile(path); err != nil {
		return nil, offset, err
	}
	return readJSONLinesFrom[AgentJournalEntry](path, offset, agentJournalMaxRecordBytes)
}

func writeAgentJournalRecord(path string, entry AgentJournalEntry, flags int) error {
	pathAgentID, err := agentIDFromJournalPath(path)
	if err != nil {
		return err
	}
	if entry.AgentID != 0 && entry.AgentID != pathAgentID {
		return fmt.Errorf("agent journal entry id %d does not match path id %d", entry.AgentID, pathAgentID)
	}
	if err := normalizeAgentJournalEntry(&entry); err != nil {
		return err
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode agent journal entry: %w", err)
	}
	b = append(b, '\n')
	if len(b) > agentJournalMaxRecordBytes {
		return fmt.Errorf("agent journal record is too large (maximum %d bytes)", agentJournalMaxRecordBytes)
	}
	if err := validateAgentJournalParent(path); err != nil {
		return err
	}
	if flags&os.O_CREATE == 0 {
		if err := validateAgentJournalFile(path); err != nil {
			return err
		}
	}

	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("open agent journal: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("open agent journal: invalid file descriptor")
	}
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if statErr != nil {
			return fmt.Errorf("inspect agent journal: %w", statErr)
		}
		return fmt.Errorf("agent journal must be a regular file")
	}
	n, writeErr := f.Write(b)
	if writeErr == nil && n != len(b) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("append agent journal: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close agent journal: %w", closeErr)
	}
	return nil
}

func normalizeAgentJournalEntry(entry *AgentJournalEntry) error {
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.Message = strings.TrimSpace(entry.Message)
	entry.Next = strings.TrimSpace(entry.Next)
	if entry.TS <= 0 {
		return fmt.Errorf("agent journal timestamp must be positive")
	}
	if entry.Source != "agent" && entry.Source != "system" {
		return fmt.Errorf("agent journal source must be agent or system")
	}
	if err := validateJournalKind(entry.Kind); err != nil {
		return err
	}
	if entry.Message == "" {
		return fmt.Errorf("journal message must not be empty")
	}
	if !utf8.ValidString(entry.Message) {
		return fmt.Errorf("journal message must be valid UTF-8")
	}
	if len(entry.Message) > agentJournalMaxMessageBytes {
		return fmt.Errorf("journal message is too long (maximum %d bytes)", agentJournalMaxMessageBytes)
	}
	if !utf8.ValidString(entry.Next) {
		return fmt.Errorf("journal next step must be valid UTF-8")
	}
	if len(entry.Next) > agentJournalMaxMessageBytes {
		return fmt.Errorf("journal next step is too long (maximum %d bytes)", agentJournalMaxMessageBytes)
	}
	return nil
}

func validateJournalKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("journal kind must not be empty")
	}
	if len(kind) > agentJournalMaxKindBytes {
		return fmt.Errorf("journal kind is too long (maximum %d bytes)", agentJournalMaxKindBytes)
	}
	for i, c := range []byte(kind) {
		if (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') || (i > 0 && (c == '-' || c == '_')) {
			continue
		}
		return fmt.Errorf("journal kind must start with a lowercase letter and contain only lowercase letters, digits, '-' or '_'")
	}
	return nil
}

func validateRunID(id string) error {
	if !strings.HasPrefix(id, "wf_") || id == "wf_" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) || strings.ContainsRune(id, 0) {
		return fmt.Errorf("invalid run id %q", id)
	}
	return nil
}

func validateAgentID(id int) error {
	if id <= 0 {
		return fmt.Errorf("agent id must be a positive integer")
	}
	return nil
}

func agentIDFromJournalPath(path string) (int, error) {
	if path == "" || !filepath.IsAbs(path) {
		return 0, fmt.Errorf("agent journal path must be absolute")
	}
	if filepath.Clean(path) != path {
		return 0, fmt.Errorf("agent journal path must be canonical")
	}
	if filepath.Base(path) != "journal.jsonl" {
		return 0, fmt.Errorf("agent journal path must end in journal.jsonl")
	}
	agentDir := filepath.Dir(path)
	agentIDText := filepath.Base(agentDir)
	agentID, err := strconv.Atoi(agentIDText)
	if err != nil || agentID <= 0 || strconv.Itoa(agentID) != agentIDText {
		return 0, fmt.Errorf("agent journal path must contain a positive numeric agent id")
	}
	agentsDir := filepath.Dir(agentDir)
	if filepath.Base(agentsDir) != "agents" {
		return 0, fmt.Errorf("agent journal path must use an agents/<id>/journal.jsonl layout")
	}
	if err := validateRunID(filepath.Base(filepath.Dir(agentsDir))); err != nil {
		return 0, fmt.Errorf("agent journal path: %w", err)
	}
	runsRoot, err := agentJournalValidationRoot()
	if err != nil {
		return 0, fmt.Errorf("resolve runs directory: %w", err)
	}
	rel, err := filepath.Rel(runsRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("agent journal path must be inside %s", runsRoot)
	}
	return agentID, nil
}

func validateAgentJournalParent(path string) error {
	runsRoot, err := agentJournalValidationRoot()
	if err != nil {
		return fmt.Errorf("resolve runs directory: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(runsRoot)
	if err != nil {
		return fmt.Errorf("resolve runs directory: %w", err)
	}
	parent := filepath.Dir(path)
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve agent journal directory: %w", err)
	}
	rel, err := filepath.Rel(runsRoot, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("agent journal directory must be inside %s", runsRoot)
	}
	if filepath.Clean(parentReal) != filepath.Clean(filepath.Join(rootReal, rel)) {
		return fmt.Errorf("agent journal directory must not traverse a symlink")
	}
	return nil
}

func agentJournalValidationRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv(AgentJournalRootEnv))
	if root == "" {
		root = RunsDir()
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("agent journal validation root must be absolute")
	}
	root = filepath.Clean(root)
	return root, nil
}

func validateAgentJournalFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("inspect agent journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agent journal must be a regular file, not a symlink or special file")
	}
	if err := validateAgentJournalParent(path); err != nil {
		return err
	}
	return nil
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

// ReadJournalFrom parses complete records appended to journal.jsonl starting
// at offset. The returned offset is the byte immediately after the last
// newline-terminated record and can be passed to the next call. A partial
// trailing record is left uncommitted until a later call observes its newline.
func ReadJournalFrom(id string, offset int64) ([]JournalEntry, int64, error) {
	return readJSONLinesFrom[JournalEntry](filepath.Join(RunsDir(), id, "journal.jsonl"), offset, 64*1024*1024)
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

// ReadEventsFrom parses complete records appended to events.jsonl starting at
// offset. The returned offset is the byte immediately after the last
// newline-terminated record and can be passed to the next call. A partial
// trailing record is left uncommitted until a later call observes its newline.
func ReadEventsFrom(id string, offset int64) ([]Event, int64, error) {
	return readJSONLinesFrom[Event](filepath.Join(RunsDir(), id, "events.jsonl"), offset, 4*1024*1024)
}

func readJSONLinesFrom[T any](path string, offset int64, maxRecordBytes int) ([]T, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	// Run files are append-only. If a retained offset lies past EOF (for
	// example after an unexpected truncation), restart from the beginning.
	// Replacements that have already regrown past offset are intentionally not
	// detected; normal run lifecycle never replaces these files.
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	next := offset
	var out []T
	r := bufio.NewReaderSize(f, 64*1024)
	line := make([]byte, 0, min(maxRecordBytes, 64*1024))
	lineBytes := 0
	oversized := false
	for {
		fragment, readErr := r.ReadSlice('\n')
		lineBytes += len(fragment)
		if !oversized {
			if lineBytes > maxRecordBytes {
				// Bound memory for corrupt or unexpectedly huge records. A
				// newline-terminated oversized record is committed and skipped,
				// just like other malformed complete records.
				oversized = true
				line = line[:0]
			} else {
				line = append(line, fragment...)
			}
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if len(fragment) > 0 && fragment[len(fragment)-1] == '\n' {
			// A malformed complete record is committed too: advancing past it
			// avoids attempting to decode it on every subsequent poll.
			if !oversized {
				var record T
				if json.Unmarshal(line, &record) == nil {
					out = append(out, record)
				}
			}
			next += int64(lineBytes)
			line = line[:0]
			lineBytes = 0
			oversized = false
		}
		if readErr != nil {
			if readErr == io.EOF {
				return out, next, nil
			}
			return out, next, readErr
		}
	}
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
		return fmt.Errorf("run %s is still running; cancel it first", id)
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
