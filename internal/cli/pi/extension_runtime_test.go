package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestInstalledPiDeliversDetachedTerminalUpdates(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}

	fixtureDir := t.TempDir()
	fakeDyna := filepath.Join(fixtureDir, "dyna")
	runCount := filepath.Join(fixtureDir, "run-count")
	statusFile := filepath.Join(fixtureDir, "status")
	launchSession := filepath.Join(fixtureDir, "launch-session")
	listCount := filepath.Join(fixtureDir, "list-count")
	cancelStarted := filepath.Join(fixtureDir, "cancel-started")
	writeExecutable(t, fakeDyna, `#!/bin/sh
case "$1:$2" in
	run:*)
		printf '%s\n' "$DYNA_SESSION" > "$PI_LAUNCH_SESSION"
		count=0
		if [ -f "$PI_RUN_COUNT" ]; then count=$(cat "$PI_RUN_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_RUN_COUNT"
		printf 'wf_fixture_%s\n' "$count"
		;;
	runs:list)
		list_count=0
		if [ -f "$PI_LIST_COUNT" ]; then list_count=$(cat "$PI_LIST_COUNT"); fi
		printf '%s\n' $((list_count + 1)) > "$PI_LIST_COUNT"
		count=$(cat "$PI_RUN_COUNT")
		status=$(cat "$PI_STATUS_FILE")
		printf '[{"id":"wf_fixture_%s","name":"fixture","status":"%s","session":"fixture-session","startedAt":"2026-07-14T00:00:00Z"}]\n' "$count" "$status"
		;;
	runs:cancel)
		printf 'started\n' > "$PI_CANCEL_STARTED"
		exec sleep 30
		;;
	*)
		printf 'unexpected fixture invocation: %s\n' "$*" >&2
		exit 98
		;;
esac
`)
	dataHome := filepath.Join(fixtureDir, "data")
	runDir := filepath.Join(dataHome, "dyna", "runs", "wf_fixture_1")
	if err := os.MkdirAll(filepath.Join(runDir, "agents", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte("{\"t\":\"agent_start\",\"id\":1,\"label\":\"worker\",\"profile\":\"fixture\",\"phase\":\"test\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(runDir, "agents", "1", "journal.jsonl")
	journalFile, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	const journalRecordLimit = 64 * 1024 * 1024
	writeSizedJournalRecord(t, journalFile, journalRecordLimit,
		`{"ts":1,"kind":"update","message":"exact-limit","padding":"`, `","source":"agent"}`+"\n")
	writeSizedJournalRecord(t, journalFile, journalRecordLimit+1,
		`{"ts":2,"kind":"update","message":"must-skip","padding":"`, "")
	if err := journalFile.Close(); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(fixtureDir, "updates.json")
	extensionJSON, err := json.Marshal(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(fixtureDir, "probe.ts")
	probeSource := `import { access, appendFile, stat, writeFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";
import dynaExtension from ` + string(extensionJSON) + `;

const tools = new Map<string, any>();
const handlers = new Map<string, any>();
const messages: any[] = [];
const notifications: any[] = [];
let command: any;
let dashboardOpen = false;
dynaExtension({
	on: (name: string, handler: any) => handlers.set(name, handler),
	registerTool: (tool: any) => tools.set(tool.name, tool),
	registerCommand: (_name: string, spec: any) => { command = spec; },
	sendMessage: (message: any, options: any) => messages.push({ message, options, duringDashboard: dashboardOpen }),
} as any);

async function waitForUpdates(count: number): Promise<void> {
	for (let attempt = 0; attempt < 150; attempt++) {
		if (messages.length === count && notifications.length === count) return;
		await sleep(20);
	}
	throw new Error("timed out waiting for terminal update");
}

export default function (pi: any) {
	pi.on("input", async () => {
		let idle = false;
		let overlay: any;
		let rendererStops = 0;
		let dashboardRendered = false;
		let layoutSafe = false;
		let extensionLayoutSafe = false;
		let tinyHeaderVisible = false;
		let remappedTab = false;
		let largeJournalRead = false;
		let oversizedCursorAdvanced = false;
		let detailWrapped = false;
		let journalAnchored = false;
		let evictedAnchorReset = false;
		let reflowedAnchorReset = false;
		let replacementClosed = false;
		let confirmationDeclined = false;
		let cancelWorkStarted = false;
		let shutdownDisposed = false;
		let whileStreaming: { messages: number; notifications: number } | undefined;
		let afterSettledWhileStreaming: { messages: number; notifications: number } | undefined;
		let whileDashboard: { messages: number; notifications: number } | undefined;
		try {
			const tui = {
				terminal: { rows: 40 },
				requestRender: () => {},
				stop: () => { rendererStops++; },
			};
			const theme = {
				fg: (_color: string, text: string) => text,
				bg: (_color: string, text: string) => text,
				bold: (text: string) => text,
			};
			const keys = { matches: (data: string, action: string) => data === "remapped-tab" && action === "tui.input.tab" };
			const context = {
				mode: "tui",
				isIdle: () => idle,
				sessionManager: { getSessionId: () => "fixture-session" },
				ui: {
					notify: (message: string, type: string) => notifications.push({ message, type }),
					setStatus: () => {},
					custom: async (factory: any, options: any) => await new Promise((resolve) => {
						overlay = factory(tui, theme, keys, (result: unknown) => {
							dashboardOpen = false;
							resolve(result);
						});
						dashboardOpen = true;
						(tui as any).children = [
							{ render: () => ["above-widget-1", "above-widget-2"] },
							{ children: [overlay], render: (width: number) => overlay.render(width) },
							{ render: () => ["below-widget"] },
							{ render: () => ["custom-footer-1", "custom-footer-2", "custom-footer-3", "custom-footer-4", "custom-footer-5"] },
						];
						const rendered = overlay.render(100);
						const screen = Array.isArray(rendered) ? rendered.join("\n") : "";
						dashboardRendered = options?.overlay === false && screen.includes("⬡ dyna") && screen.includes("╭") && screen.includes("Run detail");
					}),
				},
			};
			const signal = new AbortController().signal;
			const run = tools.get("dyna_run");
			const settled = handlers.get("agent_settled");
			for (const status of ["ok", "error", "canceled"]) {
				const expectedUpdates = messages.length + 1;
				const workflowPath = "/tmp/dyna-workflow-fixture-" + status + ".js";
				await writeFile(workflowPath, "return 1");
				await writeFile(process.env.PI_STATUS_FILE!, "running");
				await run.execute("run-" + status, { workflow_path: workflowPath }, signal, undefined, context);
				await writeFile(process.env.PI_STATUS_FILE!, status);
				if (status === "ok") {
					await sleep(1300);
					whileStreaming = { messages: messages.length, notifications: notifications.length };
					await settled({}, context);
					afterSettledWhileStreaming = { messages: messages.length, notifications: notifications.length };
					idle = true;
					const dashboard = command.handler("", context);
					for (let attempt = 0; attempt < 100 && !dashboardOpen; attempt++) await sleep(10);
					if (!dashboardOpen) throw new Error("Pi-native dashboard did not open");
					await settled({}, context);
					await waitForUpdates(expectedUpdates);
					whileDashboard = { messages: messages.length, notifications: notifications.length };
					for (let attempt = 0; attempt < 300 && !(overlay.journal.length === 1 && overlay.journalDiscardOffset !== undefined); attempt++) await sleep(20);
					const partialStat = await stat(process.env.PI_JOURNAL_PATH!);
					oversizedCursorAdvanced = overlay.journal.length === 1 &&
						overlay.journal[0]?.message === "exact-limit" &&
						overlay.journalDiscardOffset === partialStat.size &&
							overlay.journalOffset === 64 * 1024 * 1024 &&
						overlay.journalError.includes("discarding oversized journal record");
					await appendFile(process.env.PI_JOURNAL_PATH!, '","source":"agent"}\n{"ts":3,"kind":"verification","message":"after-oversized","source":"agent"}\n');
					for (let attempt = 0; attempt < 300 && overlay.journal.length !== 2; attempt++) await sleep(20);
					const journalStat = await stat(process.env.PI_JOURNAL_PATH!);
					largeJournalRead = overlay.journal.length === 2 &&
						overlay.journal[0]?.message === "exact-limit" &&
						overlay.journal[1]?.message === "after-oversized" &&
						!overlay.journal.some((entry: any) => entry.message === "must-skip") &&
						overlay.journalOffset === journalStat.size &&
						overlay.journalError.includes("skipped 1 oversized journal record") &&
						overlay.journalDiscardOffset === undefined;
					const normal = overlay.render(100);
					layoutSafe = normal.length === 32 && normal[0]?.includes("⬡ dyna");
					extensionLayoutSafe = normal.length === 40 - 2 - 1 - 5;
					tui.terminal.rows = 3;
					const tiny = overlay.render(100);
					tinyHeaderVisible = tiny.length === 1 && tiny[0]?.includes("⬡ dyna");
					tui.terminal.rows = 40;
					overlay.handleInput("remapped-tab");
					remappedTab = overlay.focus === "detail";
					const worker = overlay.detail.agents[0];
					worker.status = "error";
					worker.error = "error-" + "e".repeat(80) + "-error-tail";
					overlay.detail.events.push({ t: "log", msg: "log-" + "l".repeat(80) + "-log-tail" });
					const detailLines = overlay.renderRunDetailBody(24);
					const detailText = detailLines.join("");
					detailWrapped = detailLines.length > 10 && detailText.includes("-error-tail") && detailText.includes("-log-tail") && !detailText.includes("…");
					overlay.journal = Array.from({ length: 2000 }, (_, index) => ({
						ts: index + 1, kind: "update", message: "entry-" + index, next: "", source: "agent", phase: "", malformed: false,
					}));
					overlay.journalLoaded = true;
					overlay.journalFollow = false;
					overlay.journalScroll = 500;
					overlay.renderJournalDetailContent(60, 18);
					const anchor = overlay.journalAnchorEntry;
					overlay.applyJournalRead({ entries: Array.from({ length: 10 }, (_, index) => ({
						ts: 3000 + index, kind: "update", message: "new-" + index, next: "", source: "agent", phase: "", malformed: false,
					})), next: overlay.journalOffset + 10, reset: false, missing: false });
					overlay.renderJournalDetailContent(60, 18);
					journalAnchored = anchor !== undefined && overlay.journalAnchorEntry === anchor;
					overlay.journal = Array.from({ length: 2000 }, (_, index) => ({
						ts: index + 1, kind: "update", message: "evict-" + index, next: "", source: "agent", phase: "", malformed: false,
					}));
					overlay.journalFollow = false;
					overlay.journalScroll = 0;
					overlay.journalAnchorEntry = undefined;
					overlay.renderJournalDetailContent(60, 18);
					const evicted = overlay.journalAnchorEntry;
					overlay.applyJournalRead({ entries: [{
						ts: 4000, kind: "update", message: "replacement", next: "", source: "agent", phase: "", malformed: false,
					}], next: overlay.journalOffset + 1, reset: false, missing: false });
					overlay.renderJournalDetailContent(60, 18);
					evictedAnchorReset = evicted !== undefined && !overlay.journal.includes(evicted) &&
						overlay.journalScroll === 0 && overlay.journalAnchorEntry === overlay.journal[0];
					overlay.journal = [{
						ts: 5000, kind: "update", message: Array.from({ length: 300 }, () => "wrapped").join(" "),
						next: "", source: "agent", phase: "", malformed: false,
					}];
					overlay.journalScroll = 5;
					overlay.journalAnchorEntry = undefined;
					overlay.renderJournalDetailContent(60, 18);
					const wideAnchorText = overlay.journalAnchorText;
					overlay.renderJournalDetailContent(20, 18);
					reflowedAnchorReset = wideAnchorText !== overlay.journalAnchorText &&
						overlay.journalScroll === 0 && overlay.journalAnchorEntry === overlay.journal[0];
					await handlers.get("session_start")({ reason: "switch" }, context);
					await dashboard;
					replacementClosed = !dashboardOpen && overlay.disposed && overlay.timer === undefined && overlay.render(100).length === 0;
				} else {
					await waitForUpdates(expectedUpdates);
				}
			}
			await writeFile(process.env.PI_STATUS_FILE!, "running");
			const shutdownDashboard = command.handler("", context);
			for (let attempt = 0; attempt < 100 && !dashboardOpen; attempt++) await sleep(10);
			if (!dashboardOpen) throw new Error("shutdown dashboard did not open");
			for (let attempt = 0; attempt < 100 && overlay.runs.length === 0; attempt++) await sleep(10);
			overlay.handleInput("x");
			const pendingFooter = overlay.renderFooter(120);
			overlay.handleInput("q");
			confirmationDeclined = dashboardOpen && overlay.cancelPendingRunID === "" && pendingFooter.includes("any other key keep running");
			overlay.handleInput("x");
			overlay.handleInput("y");
			for (let attempt = 0; attempt < 100; attempt++) {
				try { await access(process.env.PI_CANCEL_STARTED!); cancelWorkStarted = true; break; } catch { await sleep(10); }
			}
			await handlers.get("session_shutdown")({}, context);
			await shutdownDashboard;
			await sleep(50);
			shutdownDisposed = !dashboardOpen && overlay.disposed && overlay.timer === undefined && overlay.abortController === undefined && overlay.actionAbortController === undefined && overlay.render(100).length === 0;
			await writeFile(process.env.PI_UPDATE_PROBE_MARKER!, JSON.stringify({
				ok: true, messages, notifications, whileStreaming, afterSettledWhileStreaming,
					whileDashboard, dashboardOpen, dashboardRendered, rendererStops, layoutSafe, extensionLayoutSafe, tinyHeaderVisible,
					remappedTab, largeJournalRead, oversizedCursorAdvanced, detailWrapped, journalAnchored, evictedAnchorReset, reflowedAnchorReset, replacementClosed,
				confirmationDeclined, cancelWorkStarted, shutdownDisposed,
			}));
		} catch (error) {
			await writeFile(process.env.PI_UPDATE_PROBE_MARKER!, JSON.stringify({
				ok: false, error: error instanceof Error ? error.message : String(error), messages, notifications,
				whileStreaming, afterSettledWhileStreaming, whileDashboard, dashboardOpen, dashboardRendered, rendererStops,
				layoutSafe, extensionLayoutSafe, tinyHeaderVisible, remappedTab, largeJournalRead, oversizedCursorAdvanced, detailWrapped, journalAnchored,
				evictedAnchorReset, reflowedAnchorReset,
				replacementClosed, confirmationDeclined, cancelWorkStarted, shutdownDisposed,
			}));
		}
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "exercise terminal updates",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"PI_OFFLINE":                 "1",
		"DYNA_SESSION":               "fixture-session",
		"DYNA_BIN":                   fakeDyna,
		"DYNA_PI_CODEX_AUTH":         "0",
		"DYNA_PI_ACTIVATE_ALL_TOOLS": "0",
		"PI_RUN_COUNT":               runCount,
		"PI_STATUS_FILE":             statusFile,
		"PI_LAUNCH_SESSION":          launchSession,
		"PI_LIST_COUNT":              listCount,
		"PI_CANCEL_STARTED":          cancelStarted,
		"PI_UPDATE_PROBE_MARKER":     marker,
		"PI_JOURNAL_PATH":            journalPath,
		"XDG_DATA_HOME":              dataHome,
		"DYNA_NO_AUTO_UPDATE":        "1",
		"OPENAI_API_KEY":             "",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi terminal-update probe timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi terminal-update probe: %v\n%s", err, output)
	}

	var probe struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Messages []struct {
			Message struct {
				Details struct {
					Status string `json:"status"`
				} `json:"details"`
			} `json:"message"`
			Options struct {
				TriggerTurn bool   `json:"triggerTurn"`
				DeliverAs   string `json:"deliverAs"`
			} `json:"options"`
			DuringDashboard bool `json:"duringDashboard"`
		}
		Notifications []struct {
			Type string `json:"type"`
		}
		WhileStreaming struct {
			Messages      int `json:"messages"`
			Notifications int `json:"notifications"`
		} `json:"whileStreaming"`
		AfterSettledWhileStreaming struct {
			Messages      int `json:"messages"`
			Notifications int `json:"notifications"`
		} `json:"afterSettledWhileStreaming"`
		WhileDashboard struct {
			Messages      int `json:"messages"`
			Notifications int `json:"notifications"`
		} `json:"whileDashboard"`
		DashboardOpen           bool `json:"dashboardOpen"`
		DashboardRendered       bool `json:"dashboardRendered"`
		RendererStops           int  `json:"rendererStops"`
		LayoutSafe              bool `json:"layoutSafe"`
		ExtensionLayoutSafe     bool `json:"extensionLayoutSafe"`
		TinyHeaderVisible       bool `json:"tinyHeaderVisible"`
		RemappedTab             bool `json:"remappedTab"`
		LargeJournalRead        bool `json:"largeJournalRead"`
		OversizedCursorAdvanced bool `json:"oversizedCursorAdvanced"`
		DetailWrapped           bool `json:"detailWrapped"`
		JournalAnchored         bool `json:"journalAnchored"`
		EvictedAnchorReset      bool `json:"evictedAnchorReset"`
		ReflowedAnchorReset     bool `json:"reflowedAnchorReset"`
		ReplacementClosed       bool `json:"replacementClosed"`
		ConfirmationDeclined    bool `json:"confirmationDeclined"`
		CancelWorkStarted       bool `json:"cancelWorkStarted"`
		ShutdownDisposed        bool `json:"shutdownDisposed"`
	}
	if err := json.Unmarshal([]byte(readFile(t, marker)), &probe); err != nil {
		t.Fatal(err)
	}
	if !probe.OK || len(probe.Messages) != 3 || len(probe.Notifications) != 3 {
		t.Fatalf("terminal-update probe = %#v", probe)
	}
	if got := strings.TrimSpace(readFile(t, launchSession)); got != "fixture-session" {
		t.Fatalf("detached Dyna run session = %q, want persisted Pi session", got)
	}
	if probe.WhileStreaming.Messages != 0 || probe.WhileStreaming.Notifications != 0 ||
		probe.AfterSettledWhileStreaming.Messages != 0 || probe.AfterSettledWhileStreaming.Notifications != 0 {
		t.Fatalf("terminal update was injected while Pi was streaming: before=%#v after-settled=%#v", probe.WhileStreaming, probe.AfterSettledWhileStreaming)
	}
	if probe.WhileDashboard.Messages != 1 || probe.WhileDashboard.Notifications != 1 {
		t.Fatalf("terminal update did not deliver while the Pi-native dashboard was open: %#v", probe.WhileDashboard)
	}
	if probe.DashboardOpen || !probe.DashboardRendered || probe.RendererStops != 0 {
		t.Fatalf("Pi-native dashboard lifecycle = open:%v rendered:%v renderer-stops:%d", probe.DashboardOpen, probe.DashboardRendered, probe.RendererStops)
	}
	if !probe.LayoutSafe || !probe.ExtensionLayoutSafe || !probe.TinyHeaderVisible || !probe.RemappedTab || !probe.LargeJournalRead || !probe.OversizedCursorAdvanced || !probe.DetailWrapped || !probe.JournalAnchored || !probe.EvictedAnchorReset || !probe.ReflowedAnchorReset {
		t.Fatalf("Pi-native dashboard correctness probe = %#v", probe)
	}
	if !probe.ReplacementClosed || !probe.ConfirmationDeclined || !probe.CancelWorkStarted || !probe.ShutdownDisposed {
		t.Fatalf("Pi-native dashboard teardown/cancel probe = %#v", probe)
	}
	for i, want := range []struct {
		status, notification string
	}{{"ok", "info"}, {"error", "error"}, {"canceled", "warning"}} {
		if probe.Messages[i].Message.Details.Status != want.status || !probe.Messages[i].Options.TriggerTurn || probe.Messages[i].Options.DeliverAs != "followUp" {
			t.Fatalf("terminal message %d = %#v, want %s with a follow-up turn", i, probe.Messages[i], want.status)
		}
		if i == 0 && !probe.Messages[i].DuringDashboard {
			t.Fatalf("first terminal message = %#v, want delivery while Pi-native dashboard remained open", probe.Messages[i])
		}
		if probe.Notifications[i].Type != want.notification {
			t.Fatalf("terminal notification %d = %#v, want %s", i, probe.Notifications[i], want.notification)
		}
	}
}

func TestInstalledPiLoadsNativeWorkflowToolSchemasOffline(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "tools.json")
	probePath := filepath.Join(t.TempDir(), "probe.ts")
	probeSource := `import { writeFile } from "node:fs/promises";
export default function (pi: any) {
	pi.on("input", async () => {
		const wanted = new Set(["dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer"]);
		const tools = pi.getAllTools()
			.filter((tool: any) => wanted.has(tool.name) || tool.name === "dyna_guide")
			.map((tool: any) => ({ name: tool.name, description: tool.description, parameters: tool.parameters }));
		await writeFile(process.env.TOOL_PROBE_MARKER, JSON.stringify(tools));
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "probe native tools",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"HOME":                home,
		"PI_CODING_AGENT_DIR": filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE":          "1",
		"DYNA_SESSION":        "fixture-session",
		"DYNA_BIN":            "/bin/false",
		"DYNA_PI_CODEX_AUTH":  "0",
		"TOOL_PROBE_MARKER":   marker,
		"OPENAI_API_KEY":      "",
		"CODEX_HOME":          filepath.Join(home, ".codex-fixture"),
		"DYNA_CODEX_BIN":      "/bin/false",
		"DYNA_NO_AUTO_UPDATE": "1",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi tool registration timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi tool registration: %v\n%s", err, output)
	}
	var tools []struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(readFile(t, marker)), &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("native Pi tools = %#v", tools)
	}
	byName := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		if tool.Name == "dyna_guide" {
			t.Fatal("dyna_guide was registered")
		}
		byName[tool.Name] = tool.Parameters
	}
	for _, name := range []string{"dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer"} {
		if byName[name] == nil {
			t.Errorf("installed Pi did not load %s", name)
		}
	}
	runProperties, _ := byName["dyna_run"]["properties"].(map[string]any)
	for _, field := range []string{"workflow_path", "cwd", "args", "name", "resume", "max_concurrent", "call_cap"} {
		if runProperties[field] == nil {
			t.Errorf("installed dyna_run schema is missing %s", field)
		}
	}

	missingSessionMarker := filepath.Join(t.TempDir(), "tools.json")
	missingCtx, cancelMissing := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelMissing()
	missing := exec.CommandContext(missingCtx, piPath,
		"--offline", "--no-extensions",
		"--extension", extensionPath,
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "probe tools without session",
	)
	missing.Env = cmd.Env
	missing.Env = setEnv(missing.Env, "DYNA_SESSION", "")
	missing.Env = setEnv(missing.Env, "TOOL_PROBE_MARKER", missingSessionMarker)
	if output, err := missing.CombinedOutput(); err != nil {
		t.Fatalf("offline Pi missing-session registration: %v\n%s", err, output)
	}
	var withoutSession []any
	if err := json.Unmarshal([]byte(readFile(t, missingSessionMarker)), &withoutSession); err != nil {
		t.Fatal(err)
	}
	if len(withoutSession) != 4 {
		t.Fatalf("Pi tools must derive ownership from SessionManager, not DYNA_SESSION: %#v", withoutSession)
	}
}

func TestInstalledPiActivatesRootPresetToolsWithoutOverridingExplicitControls(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(t.TempDir(), "active-tools.ts")
	probeSource := `import { writeFile } from "node:fs/promises";
import { Type } from "@earendil-works/pi-ai";
export default function (pi: any) {
	pi.registerTool({
		name: "fixture_extension_tool",
		description: "Fixture extension tool",
		parameters: Type.Object({}),
		async execute() { return { content: [{ type: "text", text: "fixture" }] }; },
	});
	pi.on("session_start", async () => {
		await writeFile(process.env.TOOL_PROBE_MARKER!, JSON.stringify({
			active: pi.getActiveTools(),
			all: pi.getAllTools().map((tool: any) => tool.name),
		}));
	});
	pi.on("input", async () => ({ action: "handled" }));
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	runProbe := func(t *testing.T, activeAll bool, args ...string) struct {
		Active []string `json:"active"`
		All    []string `json:"all"`
	} {
		t.Helper()
		marker := filepath.Join(t.TempDir(), "active-tools.json")
		piArgs := []string{
			"--offline", "--no-extensions",
			"--extension", extensionPath,
			"--extension", probePath,
			"--provider", "openai-codex",
			"--model", "gpt-5.6-terra",
			"--api-key", "fixture-only",
		}
		piArgs = append(piArgs, args...)
		piArgs = append(piArgs, "-p", "probe active tools")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, piPath, piArgs...)
		cmd.Env = os.Environ()
		for key, value := range map[string]string{
			"HOME":                       home,
			"PI_CODING_AGENT_DIR":        filepath.Join(home, ".pi-fixture"),
			"PI_OFFLINE":                 "1",
			"DYNA_SESSION":               "fixture-session",
			"DYNA_BIN":                   "/bin/false",
			"DYNA_PI_CODEX_AUTH":         "0",
			"TOOL_PROBE_MARKER":          marker,
			"OPENAI_API_KEY":             "",
			"CODEX_HOME":                 filepath.Join(home, ".codex-fixture"),
			"DYNA_CODEX_BIN":             "/bin/false",
			"DYNA_NO_AUTO_UPDATE":        "1",
			"DYNA_PI_ACTIVATE_ALL_TOOLS": map[bool]string{true: "1", false: ""}[activeAll],
		} {
			cmd.Env = setEnv(cmd.Env, key, value)
		}
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("offline Pi active-tool probe timed out: %v", ctx.Err())
		}
		if err != nil {
			t.Fatalf("offline Pi active-tool probe: %v\n%s", err, output)
		}
		var result struct {
			Active []string `json:"active"`
			All    []string `json:"all"`
		}
		if err := json.Unmarshal([]byte(readFile(t, marker)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	defaultTools := runProbe(t, true)
	for _, name := range []string{"read", "bash", "edit", "write", "grep", "find", "ls", "dyna_profiles", "dyna_run", "dyna_runs", "dyna_steer", "fixture_extension_tool"} {
		if !slices.Contains(defaultTools.Active, name) {
			t.Errorf("default root preset active tools = %#v, missing %s", defaultTools.Active, name)
		}
	}
	if !slices.Contains(defaultTools.All, "fixture_extension_tool") {
		t.Fatalf("root preset did not register fixture extension tool: %#v", defaultTools.All)
	}

	restrictedTools := runProbe(t, false, "--tools", "read")
	if strings.Join(restrictedTools.Active, ",") != "read" {
		t.Fatalf("explicit --tools selection was overridden: %#v", restrictedTools.Active)
	}
}

func TestInstalledPiExecutesNativeToolsWithRegistrationGraceAndRedaction(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	extensionPath, err := provisionPiExtension()
	if err != nil {
		t.Fatal(err)
	}

	fixtureDir := t.TempDir()
	fakeDyna := filepath.Join(fixtureDir, "dyna")
	listCount := filepath.Join(fixtureDir, "list-count")
	runCount := filepath.Join(fixtureDir, "run-count")
	secret := "fixture-success-secret"
	writeExecutable(t, fakeDyna, `#!/bin/sh
case "$1:$2" in
	run:*)
		count=0
		if [ -f "$PI_RUN_COUNT" ]; then count=$(cat "$PI_RUN_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_RUN_COUNT"
		if [ "$count" -eq 1 ]; then
			printf '%s\n' 'wf_fixture_detached'
		else
			printf 'malformed-%s\n' "$PI_RUNTIME_SECRET"
		fi
		;;
	runs:list)
		if [ "$3" != "--json" ] || [ "$4" != "--session" ] || [ -z "$5" ]; then
			printf '%s\n' 'missing exact session filter' >&2
			exit 97
		fi
		count=0
		if [ -f "$PI_LIST_COUNT" ]; then count=$(cat "$PI_LIST_COUNT"); fi
		count=$((count + 1))
		printf '%s\n' "$count" > "$PI_LIST_COUNT"
		printf '[{"id":"wf_fixture_detached","name":"owned","status":"running","session":"%s","startedAt":"2026-07-14T00:00:00Z"}]\n' "$5"
		;;
	runs:show)
		printf '{"output":"%s"}\n' "$PI_RUNTIME_SECRET"
		;;
	runs:steer)
		printf 'queued %s\n' "$PI_RUNTIME_SECRET"
		;;
	*)
		printf 'unexpected fixture invocation: %s\n' "$*" >&2
		exit 98
		;;
esac
`)

	marker := filepath.Join(fixtureDir, "probe.json")
	probePath := filepath.Join(fixtureDir, "probe.ts")
	extensionJSON, err := json.Marshal(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	probeSource := `import { readFile, writeFile } from "node:fs/promises";
import dynaExtension from ` + string(extensionJSON) + `;

const tools = new Map<string, any>();
dynaExtension({
	on: () => {},
	registerTool: (tool: any) => tools.set(tool.name, tool),
	registerCommand: () => {},
} as any);

export default function (pi: any) {
	pi.on("input", async (_event: any, ctx: any) => {
		try {
			const signal = new AbortController().signal;
			const firstPath = "/tmp/dyna-workflow-runtime-first.js";
			await writeFile(firstPath, "return 1");
			const run = await tools.get("dyna_run").execute("run-call", { workflow_path: firstPath }, signal, undefined, ctx);
			const show = await tools.get("dyna_runs").execute("show-call", { action: "show", run_id: "wf_fixture_detached" }, signal, undefined, ctx);
			const steer = await tools.get("dyna_steer").execute("steer-call", { run_id: "wf_fixture_detached", agent_id: 1, message: "continue" }, signal, undefined, ctx);
			let malformedError = "";
			try {
				const secondPath = "/tmp/dyna-workflow-runtime-second.js";
				await writeFile(secondPath, "return 2");
				await tools.get("dyna_run").execute("bad-run-call", { workflow_path: secondPath }, signal, undefined, ctx);
			} catch (error) {
				malformedError = error instanceof Error ? error.message : String(error);
			}
			await writeFile(process.env.PI_TOOL_PROBE_MARKER, JSON.stringify({ ok: true, run, show, steer, malformedError }));
		} catch (error) {
			await writeFile(process.env.PI_TOOL_PROBE_MARKER, JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
		}
		return { action: "handled" };
	});
}
`
	if err := os.WriteFile(probePath, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, piPath,
		"--offline", "--no-extensions",
		"--extension", probePath,
		"--provider", "openai-codex",
		"--model", "gpt-5.6-terra",
		"--api-key", "fixture-only",
		"-p", "exercise native tools",
	)
	cmd.Env = os.Environ()
	for key, value := range map[string]string{
		"HOME":                 home,
		"PI_CODING_AGENT_DIR":  filepath.Join(home, ".pi-fixture"),
		"PI_OFFLINE":           "1",
		"DYNA_SESSION":         "fixture-session",
		"DYNA_BIN":             fakeDyna,
		"DYNA_PI_CODEX_AUTH":   "0",
		"PI_LIST_COUNT":        listCount,
		"PI_RUN_COUNT":         runCount,
		"PI_RUNTIME_SECRET":    secret,
		"PI_TOOL_PROBE_MARKER": marker,
		"OPENAI_API_KEY":       "",
		"CODEX_HOME":           filepath.Join(home, ".codex-fixture"),
		"DYNA_CODEX_BIN":       "/bin/false",
		"DYNA_NO_AUTO_UPDATE":  "1",
	} {
		cmd.Env = setEnv(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("offline Pi native tool execution timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("offline Pi native tool execution: %v\n%s", err, output)
	}

	raw := readFile(t, marker)
	if strings.Contains(raw, secret) || bytes.Contains(output, []byte(secret)) {
		t.Fatal("successful native tool output exposed the fixture secret")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatal(err)
	}
	if probe["ok"] != true {
		t.Fatalf("native tool runtime probe failed: %s", raw)
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("native tool runtime did not redact successful output: %s", raw)
	}
	run, _ := probe["run"].(map[string]any)
	runDetails, _ := run["details"].(map[string]any)
	if runDetails["runId"] != "wf_fixture_detached" || runDetails["detached"] != true {
		t.Fatalf("detached run details = %#v", runDetails)
	}
	show, _ := probe["show"].(map[string]any)
	showDetails, _ := show["details"].(map[string]any)
	if showDetails["action"] != "show" || showDetails["runId"] != "wf_fixture_detached" || showDetails["priorStatus"] != "running" {
		t.Fatalf("show details = %#v", showDetails)
	}
	if showDetails["stdoutTruncated"] != false || showDetails["stderrTruncated"] != false {
		t.Fatalf("show truncation flags = stdout:%v stderr:%v", showDetails["stdoutTruncated"], showDetails["stderrTruncated"])
	}
	if malformed, _ := probe["malformedError"].(string); !strings.Contains(malformed, "invalid run ID") || !strings.Contains(malformed, "[REDACTED]") {
		t.Fatalf("malformed detached ID error = %q", malformed)
	}
}
