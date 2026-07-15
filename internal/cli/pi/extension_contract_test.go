package pi

import (
	"strings"
	"testing"
)

func TestPiExtensionRegistersModelVisibleSteeringTool(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`import { Type } from "@earendil-works/pi-ai"`,
		`pi.registerTool({`,
		`name: "dyna_steer"`,
		`run_id: Type.String`,
		`agent_id: Type.Integer`,
		`message: Type.String`,
		`maxLength: 2000`,
		`const run = await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id))`,
		`["runs", "steer", params.run_id, String(params.agent_id), params.message]`,
		`never starts a replacement`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("pi steering tool contract is missing %q", required)
		}
	}
	if strings.Contains(source, `execute(_toolCallId, params) { return pi.sendUserMessage`) {
		t.Fatal("pi steering tool delegates to prose instead of invoking the command boundary")
	}
}

func TestPiExtensionRegistersNativeWorkflowTools(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`const ROOT_AGENT = "dyna-orchestrator"`,
		`const ACTIVATE_ALL_TOOLS = process.env.DYNA_PI_ACTIVATE_ALL_TOOLS === "1"`,
		`pi.setActiveTools(pi.getAllTools().map((tool) => tool.name));`,
		"ctx.ui.setStatus(\"dyna-agent\", `agent:${ROOT_AGENT}`);",
		`name: "dyna_profiles"`,
		`name: "dyna_run"`,
		`name: "dyna_runs"`,
		`workflow_path: Type.String`,
		`args: Type.Optional(Type.Unknown`,
		`max_concurrent: Type.Optional(Type.Integer`,
		`call_cap: Type.Optional(Type.Integer`,
		`async function consumeWorkflow(workflowPath: string)`,
		`await rename(sourcePath, scriptPath)`,
		`const cliArgs = ["run", scriptPath, "--json", "--detach"]`,
		`await rm(tempDir, { recursive: true, force: true })`,
		`execFile(DYNA, args`,
		`ctx.sessionManager.getSessionId()`,
		`const session = sessionID(ctx)`,
		`sessionEnv(session)`,
		`await requireSessionRun(params.resume, session, signal)`,
		`await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id))`,
		`DETACHED_REGISTRATION_GRACE_MS = 15 * 1000`,
		`Type.Literal("cancel")`,
		`redactSecrets`,
		`["runs", "list", "--json", "--session", session]`,
		`checkedString(params.message, "message", 2000)`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi native workflow tool contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{`name: "dyna_guide"`, `exec(`, `shell: true`, `workflow: Type.String`, `detach: Type.Optional`, `if (!SESSION) return`, `await waitForSessionRunRegistration(runID, signal)`} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Pi native workflow tools contain forbidden implementation %q", forbidden)
		}
	}
}

func TestPiDynaObserverStaticContract(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`type ObserverFocus = "runs" | "detail" | "agents" | "journal"`,
		`private selectedRunID = ""`,
		`private selectedAgentID: number | undefined`,
		`private journalFollow = true`,
		`private journalUnseen = 0`,
		`private refreshQueued = false`,
		`private selectionGeneration = 0`,
		`private abortController: AbortController | undefined`,
		`private journalAnchorEntry: AgentJournalEntry | undefined`,
		`function safeText(value: unknown, fallback = "")`,
		`async function readAgentJournal(id: string, agentID: number, offset: number, discardOffset?: number, signal?: AbortSignal)`,
		`signal?.throwIfAborted()`,
		`const MAX_JOURNAL_RECORD = 64 * 1024 * 1024`,
		`if (recordBytes >= MAX_JOURNAL_RECORD) discarding = true`,
		`if (recordBytes > MAX_JOURNAL_RECORD) discarding = true`,
		`discard: !foundNewline && discarding ? scanOffset : undefined`,
		`maximum ${MAX_JOURNAL_RECORD} bytes including newline`,
		`const buffer = Buffer.allocUnsafe(length)`,
		`const reset = offset < 0 || offset > stat.size`,
		`eventGeneration !== this.selectionGeneration`,
		`journalGeneration !== this.selectionGeneration`,
		`this.journalUnseen += batch.entries.length`,
		`this.keys.matches(data, "tui.select.up")`,
		`this.keys.matches(data, "tui.select.pageDown")`,
		`this.keys.matches(data, "tui.select.cancel")`,
		`this.keys.matches(data, "tui.input.tab")`,
		`class DynaRunsView implements Component`,
		`const PI_SPACER_ROWS = 1`,
		`const PI_STOCK_FOOTER_ROWS = 3`,
		`terminalRows - PI_SPACER_ROWS - PI_STOCK_FOOTER_ROWS`,
		`safeWidth >= 88`,
		`private framePane(`,
		`"Agent journals"`,
		`"Run detail"`,
		`"Agent detail"`,
		`private runDetailScroll = 0`,
		`private scrollRunDetail(delta: number)`,
		`private renderRunDetailBody(width: number)`,
		`this.runDetailScroll = Math.max(0, Math.min(maxScroll, this.runDetailScroll))`,
		`this.detail.events.filter((event) => event.t === "log" && event.msg)`,
		`wrapped.push(...wrapTextWithAnsi(line, Math.max(1, width)))`,
		`line.text === this.journalAnchorText`,
		`this.journalScroll = anchored >= 0 ? anchored : 0`,
		`private allocatedEditorRows(width: number, terminalRows: number)`,
		`componentContains(child as Component, this)`,
		`component.render(width).length`,
		`this.theme.bg("selectedBg"`,
		`this.cancelPendingRunID = run.id`,
		`await requireSessionRun(id, this.session, controller.signal)`,
		`runDyna(["runs", "cancel", id]`,
		`wrapTextWithAnsi(entry.message`,
		`visibleWidth(clipped)`,
		`map((line) => truncateToWidth(line, safeWidth, ""))`,
		`this.abortController?.abort()`,
		`this.actionAbortController?.abort()`,
		`function closeActiveView(): void`,
		`pi.on("session_shutdown", () => {`,
		`closeActiveView();`,
		`if (this.disposed) return []`,
		`const session = sessionID(ctx);`,
		`new DynaRunsView(tui, theme, keys, session`,
		`{ overlay: false }`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi Dyna observer contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`function journalTail(`,
		`this.journal.slice(-5)`,
		`_keys, done`,
		`width: "80%", maxHeight: "80%"`,
		`tui.stop();`,
		`spawn(DYNA, ["tui"`,
		`dashboardActive`,
		`class DynaRunsOverlay`,
		`overlayOptions:`,
		`overlay: true`,
		`(this.tui.terminal.rows || 24) - 2`,
		`.filter((event) => event.t === "log" && event.msg).slice(-3)`,
		`matchesKey(data, "tab")`,
		`Buffer.concat(chunks`,
		`return lines.map((line) => truncateToWidth(line, width, "…"))`,
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Pi Dyna observer retains obsolete behavior %q", forbidden)
		}
	}
	confirmation := strings.Index(source, `if (this.cancelPendingRunID) {`)
	globalClose := strings.Index(source, `if (matchesKey(data, "ctrl+c") || data === "q" || data === "Q") {`)
	if confirmation < 0 || globalClose < 0 || confirmation > globalClose {
		t.Errorf("cancellation confirmation must consume confirmation or decline before global close keys")
	}
}

func TestPiDynaTerminalDeliveryStaticContract(t *testing.T) {
	source := string(piExtensionTS)
	for _, required := range []string{
		`const launchedRunIDs = new Set<string>();`,
		`const pendingRunUpdates = new Map<string, TerminalRunUpdate>();`,
		`if (pendingRunUpdates.size === 0 || !ctx.isIdle()) return;`,
		`function watchLaunchedRun(id: string, session: string, ctx: ExtensionContext): void`,
		`run.status === "ok" || run.status === "error" || run.status === "canceled"`,
		`customType: "dyna_run_terminal"`,
		`display: false`,
		`{ triggerTurn: true, deliverAs: "followUp" }`,
		`ctx.ui.notify(completionMessage(run), completionNotificationType(run));`,
		`pi.on("agent_settled"`,
		`watchLaunchedRun(runID, session, ctx);`,
		`runCompletionAbort.abort()`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("Pi terminal delivery contract is missing %q", required)
		}
	}
}
