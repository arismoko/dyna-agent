import { execFile, spawn } from "node:child_process";
import { open, readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext, Theme } from "@earendil-works/pi-coding-agent";
import { matchesKey, truncateToWidth, type Component, type TUI } from "@earendil-works/pi-tui";

const SESSION = process.env.DYNA_SESSION ?? "";
const DYNA = process.env.DYNA_BIN || "dyna";
const MAX_OUTPUT = 16 * 1024 * 1024;
const MAX_EVENT_READ = 4 * 1024 * 1024;
const MAX_JOURNAL_TAIL = 256 * 1024;
const LIST_POLL_TICKS = 5;
const CODEX_AUTH_ENABLED = process.env.DYNA_PI_CODEX_AUTH === "1";
const CODEX = process.env.DYNA_CODEX_BIN || "codex";
const CODEX_PROVIDER = "openai-codex";
const CODEX_REFRESH_MARGIN_MS = 10 * 60 * 1000;
const CODEX_REFRESH_RETRY_MS = 30 * 1000;
const CODEX_RPC_TIMEOUT_MS = 20 * 1000;
const CODEX_RPC_MAX_LINE = 1024 * 1024;

type CodexAccess = {
	token: string;
	expiresAt: number;
};

class CodexAuthError extends Error {}

let codexHome: string | undefined;
let codexSync: Promise<CodexAccess> | undefined;
let codexRefreshTimer: ReturnType<typeof setTimeout> | undefined;
let codexAuthWarning = "";

function codexAuthMessage(error: unknown): string {
	if (error instanceof CodexAuthError) return error.message;
	return "Codex authentication could not be reused. Run `codex login` and retry.";
}

function reportCodexAuthFailure(ctx: ExtensionContext, error: unknown): void {
	const message = codexAuthMessage(error);
	const repeated = message === codexAuthWarning;
	codexAuthWarning = message;
	if (ctx.mode === "print" || ctx.mode === "json") {
		if (!repeated) process.stderr.write(`${message}\n`);
		process.exitCode = 1;
	} else if (!repeated) {
		ctx.ui.notify(message, "error");
	}
}

function readCodexRPCLine(line: string): Record<string, unknown> | undefined {
	try {
		const value: unknown = JSON.parse(line);
		return typeof value === "object" && value !== null ? value as Record<string, unknown> : undefined;
	} catch {
		return undefined;
	}
}

async function queryCodexAccount(refreshToken: boolean): Promise<string> {
	return await new Promise<string>((resolve, reject) => {
		const child = spawn(CODEX, ["app-server", "--listen", "stdio://"], {
			stdio: ["pipe", "pipe", "ignore"],
			windowsHide: true,
		});
		let buffer = "";
		let home = "";
		let settled = false;
		const timer = setTimeout(() => finish(new CodexAuthError("Codex authentication check timed out. Run `codex login` and retry.")), CODEX_RPC_TIMEOUT_MS);
		timer.unref?.();

		function finish(error?: Error): void {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			child.stdin.end();
			child.kill();
			if (error) reject(error);
			else resolve(home);
		}

		function send(value: unknown): void {
			child.stdin.write(`${JSON.stringify(value)}\n`);
		}

		child.on("error", () => finish(new CodexAuthError("Codex CLI is unavailable. Install Codex, run `codex login`, and retry.")));
		child.stdin.on("error", () => {
			// The process error/exit handler reports the actionable failure.
		});
		child.on("exit", () => {
			if (!settled) finish(new CodexAuthError("Codex authentication check exited before completing. Run `codex login` and retry."));
		});
		child.stdout.setEncoding("utf8");
		child.stdout.on("data", (chunk: string) => {
			buffer += chunk;
			if (buffer.length > CODEX_RPC_MAX_LINE) {
				finish(new CodexAuthError("Codex returned an unsupported authentication response."));
				return;
			}
			for (;;) {
				const newline = buffer.indexOf("\n");
				if (newline < 0) break;
				const line = buffer.slice(0, newline);
				buffer = buffer.slice(newline + 1);
				const message = readCodexRPCLine(line);
				if (!message) continue;
				if (message.id === 1) {
					const result = message.result;
					const candidate = typeof result === "object" && result !== null
						? (result as Record<string, unknown>).codexHome
						: undefined;
					if (typeof candidate !== "string" || !isAbsolute(candidate)) {
						finish(new CodexAuthError("Codex returned an unsupported authentication-store location."));
						return;
					}
					home = candidate;
					send({ method: "initialized" });
					send({ method: "account/read", id: 2, params: { refreshToken } });
					continue;
				}
				if (message.id !== 2) continue;
				if (message.error !== undefined) {
					finish(new CodexAuthError(refreshToken
						? "Codex could not refresh its ChatGPT authentication. Run `codex login` and retry."
						: "Codex authentication could not be read. Run `codex login` and retry."));
					return;
				}
				const result = message.result;
				const account = typeof result === "object" && result !== null
					? (result as Record<string, unknown>).account
					: undefined;
				const accountType = typeof account === "object" && account !== null
					? (account as Record<string, unknown>).type
					: undefined;
				if (accountType !== "chatgpt") {
					finish(new CodexAuthError("Codex is not authenticated with ChatGPT OAuth. Run `codex login` and retry."));
					return;
				}
				finish();
			}
		});

		send({
			method: "initialize",
			id: 1,
			params: { clientInfo: { name: "dyna_pi", title: "Dyna Pi", version: "1" } },
		});
	});
}

async function readCodexAccess(home: string): Promise<CodexAccess> {
	let value: unknown;
	try {
		value = JSON.parse(await readFile(join(home, "auth.json"), "utf8"));
	} catch {
		throw new CodexAuthError("Codex ChatGPT credentials are not available in its supported file store. Run `codex login` and retry.");
	}
	if (typeof value !== "object" || value === null) {
		throw new CodexAuthError("Codex uses an unsupported credential format. Update Codex or run `codex login` and retry.");
	}
	const auth = value as Record<string, unknown>;
	if (auth.auth_mode !== "chatgpt" || typeof auth.tokens !== "object" || auth.tokens === null) {
		throw new CodexAuthError("Codex is not authenticated with a supported ChatGPT OAuth credential. Run `codex login` and retry.");
	}
	const token = (auth.tokens as Record<string, unknown>).access_token;
	if (typeof token !== "string" || token.length === 0) {
		throw new CodexAuthError("Codex uses an unsupported credential format. Update Codex or run `codex login` and retry.");
	}
	try {
		const parts = token.split(".");
		if (parts.length !== 3) throw new Error("not a JWT");
		const claims: unknown = JSON.parse(Buffer.from(parts[1]!, "base64url").toString("utf8"));
		if (typeof claims !== "object" || claims === null) throw new Error("invalid claims");
		const expires = (claims as Record<string, unknown>).exp;
		const providerClaims = (claims as Record<string, unknown>)["https://api.openai.com/auth"];
		const accountID = typeof providerClaims === "object" && providerClaims !== null
			? (providerClaims as Record<string, unknown>).chatgpt_account_id
			: undefined;
		if (typeof expires !== "number" || !Number.isFinite(expires) || typeof accountID !== "string" || accountID.length === 0) {
			throw new Error("missing claims");
		}
		return { token, expiresAt: expires * 1000 };
	} catch {
		throw new CodexAuthError("Codex uses an unsupported access-token format. Update Codex or run `codex login` and retry.");
	}
}

async function obtainCodexAccess(): Promise<CodexAccess> {
	if (!codexHome) codexHome = await queryCodexAccount(false);
	let access = await readCodexAccess(codexHome);
	if (access.expiresAt - Date.now() <= CODEX_REFRESH_MARGIN_MS) {
		codexHome = await queryCodexAccount(true);
		access = await readCodexAccess(codexHome);
	}
	if (access.expiresAt <= Date.now()) {
		throw new CodexAuthError("Codex ChatGPT authentication is expired and could not be refreshed. Run `codex login` and retry.");
	}
	return access;
}

function scheduleCodexRefresh(ctx: ExtensionContext, expiresAt: number, retryDelay?: number): void {
	if (ctx.model?.provider !== CODEX_PROVIDER) {
		clearCodexAccess(ctx);
		return;
	}
	if (codexRefreshTimer) clearTimeout(codexRefreshTimer);
	const delay = retryDelay ?? Math.max(1000, expiresAt - Date.now() - CODEX_REFRESH_MARGIN_MS);
	codexRefreshTimer = setTimeout(() => {
		void installCodexAccess(ctx).catch((error) => {
			if (ctx.model?.provider !== CODEX_PROVIDER) {
				clearCodexAccess(ctx);
				return;
			}
			reportCodexAuthFailure(ctx, error);
			if (Date.now() >= expiresAt && !ctx.isIdle()) ctx.abort();
			scheduleCodexRefresh(ctx, expiresAt, CODEX_REFRESH_RETRY_MS);
		});
	}, delay);
	codexRefreshTimer.unref?.();
}

function clearCodexAccess(ctx: ExtensionContext): void {
	if (codexRefreshTimer) clearTimeout(codexRefreshTimer);
	codexRefreshTimer = undefined;
	ctx.modelRegistry.authStorage.removeRuntimeApiKey(CODEX_PROVIDER);
	codexAuthWarning = "";
}

async function installCodexAccess(ctx: ExtensionContext): Promise<void> {
	if (ctx.model?.provider !== CODEX_PROVIDER) {
		clearCodexAccess(ctx);
		return;
	}
	if (!codexSync) {
		codexSync = obtainCodexAccess().finally(() => {
			codexSync = undefined;
		});
	}
	const access = await codexSync;
	if (ctx.model?.provider !== CODEX_PROVIDER) {
		clearCodexAccess(ctx);
		return;
	}
	ctx.modelRegistry.authStorage.setRuntimeApiKey(CODEX_PROVIDER, access.token);
	codexAuthWarning = "";
	scheduleCodexRefresh(ctx, access.expiresAt);
}

type Run = {
	id: string;
	name: string;
	status: string;
	session?: string;
	startedAt: string;
	endedAt?: string;
};

type RunEvent = {
	t: string;
	id?: number;
	label?: string;
	profile?: string;
	phase?: string;
	title?: string;
	status?: string;
};

type Agent = {
	id: number;
	label: string;
	profile: string;
	phase: string;
	status: string;
};

type Detail = {
	phase: string;
	agents: Agent[];
};

type EventRead = {
	events: RunEvent[];
	next: number;
	reset: boolean;
	complete: boolean;
};

function dyna(args: string[]): Promise<string> {
	return new Promise((resolve, reject) => {
		execFile(DYNA, args, { encoding: "utf8", maxBuffer: MAX_OUTPUT }, (error, stdout) => {
			if (error) {
				reject(error);
				return;
			}
			resolve(stdout);
		});
	});
}

async function listRuns(): Promise<Run[]> {
	if (!SESSION) return [];
	const raw = await dyna(["runs", "list", "--json", "--session", SESSION]);
	const parsed: unknown = JSON.parse(raw);
	if (parsed === null) return [];
	if (!Array.isArray(parsed)) throw new Error("dyna returned an invalid run list");
	if (!parsed.every(isRun)) throw new Error("dyna returned an invalid run list");
	return parsed;
}

function isRun(value: unknown): value is Run {
	if (typeof value !== "object" || value === null) return false;
	const run = value as Record<string, unknown>;
	if (typeof run.id !== "string" || !run.id.startsWith("wf_") || run.id.length === 3 || /[\\/]/.test(run.id) || run.id.includes("\0")) return false;
	if (typeof run.name !== "string" || typeof run.status !== "string" || !["running", "ok", "error", "canceled"].includes(run.status) || typeof run.startedAt !== "string") return false;
	if (run.session !== undefined && typeof run.session !== "string") return false;
	if (run.endedAt !== undefined && typeof run.endedAt !== "string") return false;
	return true;
}

function isRunEvent(value: unknown): value is RunEvent {
	if (typeof value !== "object" || value === null) return false;
	const event = value as Record<string, unknown>;
	if (typeof event.t !== "string") return false;
	if (event.id !== undefined && (typeof event.id !== "number" || !Number.isInteger(event.id) || event.id <= 0)) return false;
	for (const key of ["label", "profile", "phase", "title", "status"]) {
		if (event[key] !== undefined && typeof event[key] !== "string") return false;
	}
	return true;
}

function applyEvents(detail: Detail, events: RunEvent[]): void {
	const agents = new Map(detail.agents.map((agent) => [agent.id, agent]));

	for (const event of events) {
		if (event.t === "phase" && event.title) {
			detail.phase = event.title;
			continue;
		}
		if (event.t === "agent_start" && event.id !== undefined) {
			if (event.phase) detail.phase = event.phase;
			agents.set(event.id, {
				id: event.id,
				label: event.label || `agent ${event.id}`,
				profile: event.profile || "",
				phase: event.phase || detail.phase,
				status: "queued",
			});
			continue;
		}
		if (event.id === undefined) continue;
		const agent = agents.get(event.id);
		if (!agent) continue;
		if (event.t === "agent_run") agent.status = "running";
		if (event.t === "agent_end") agent.status = event.status || "error";
	}

	detail.agents = [...agents.values()];
}

function runsDir(): string {
	const xdg = process.env.XDG_DATA_HOME;
	return xdg ? join(xdg, "dyna", "runs") : join(homedir(), ".local", "share", "dyna", "runs");
}

async function readEvents(id: string, offset: number): Promise<EventRead> {
	const handle = await open(join(runsDir(), id, "events.jsonl"), "r");
	try {
		const stat = await handle.stat();
		if (!stat.isFile()) throw new Error("dyna events path is not a regular file");
		const reset = offset < 0 || offset > stat.size;
		if (reset) offset = 0;
		const length = Math.min(MAX_EVENT_READ, stat.size - offset);
		if (length <= 0) return { events: [], next: offset, reset, complete: true };

		const buffer = Buffer.alloc(length);
		const { bytesRead } = await handle.read(buffer, 0, length, offset);
		const lastNewline = buffer.lastIndexOf(0x0a, bytesRead - 1);
		if (lastNewline < 0) return { events: [], next: offset, reset, complete: false };

		const events: RunEvent[] = [];
		for (const line of buffer.subarray(0, lastNewline).toString("utf8").split("\n")) {
			try {
				const event: unknown = JSON.parse(line);
				if (isRunEvent(event)) events.push(event);
			} catch {
				// Complete malformed records are skipped and committed.
			}
		}
		const next = offset + lastNewline + 1;
		return { events, next, reset, complete: next >= stat.size };
	} finally {
		await handle.close();
	}
}

async function journalTail(id: string, agent?: number): Promise<string[]> {
	const path = agent === undefined
		? join(runsDir(), id, "journal.jsonl")
		: join(runsDir(), id, "agents", String(agent), "journal.jsonl");
	try {
		const handle = await open(path, "r");
		try {
			const stat = await handle.stat();
			if (!stat.isFile() || stat.size === 0) return [];
			const start = Math.max(0, stat.size - MAX_JOURNAL_TAIL);
			const buffer = Buffer.alloc(stat.size - start);
			const { bytesRead } = await handle.read(buffer, 0, buffer.length, start);
			let tail = buffer.subarray(0, bytesRead);
			if (start > 0) {
				const firstNewline = tail.indexOf(0x0a);
				if (firstNewline < 0) return [];
				tail = tail.subarray(firstNewline + 1);
			}
			return tail.toString("utf8").trimEnd().split("\n").filter(Boolean).slice(-10).map(formatJournalLine);
		} finally {
			await handle.close();
		}
	} catch {
		return [];
	}
}

function formatJournalLine(line: string): string {
	try {
		const entry = JSON.parse(line) as Record<string, unknown>;
		const label = typeof entry.label === "string" ? `${entry.label}: ` : "";
		const kind = typeof entry.kind === "string" ? `[${entry.kind}] ` : "";
		const value = entry.message ?? entry.error ?? entry.result;
		const text = typeof value === "string" ? value : JSON.stringify(value);
		return `${label}${kind}${text || "completed"}`.replace(/\s+/g, " ");
	} catch {
		return line.replace(/\s+/g, " ");
	}
}

function errorText(error: unknown): string {
	if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") {
		return "dyna CLI not found on PATH";
	}
	if (error instanceof Error) return error.message.split("\n")[0] || "unknown error";
	return String(error);
}

function elapsed(run: Run): string {
	const start = new Date(run.startedAt).getTime();
	const end = run.endedAt ? new Date(run.endedAt).getTime() : Date.now();
	if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
	const seconds = Math.max(0, Math.floor((end - start) / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m${seconds % 60}s`;
	return `${Math.floor(minutes / 60)}h${minutes % 60}m`;
}

function started(run: Run): string {
	const date = new Date(run.startedAt);
	if (!Number.isFinite(date.getTime())) return "";
	return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function statusGlyph(status: string): string {
	switch (status) {
		case "running": return "●";
		case "ok": return "✓";
		case "error": return "✗";
		case "canceled": return "■";
		default: return "·";
	}
}

class DynaRunsOverlay implements Component {
	private timer: ReturnType<typeof setInterval> | undefined;
	private runs: Run[] = [];
	private selected = 0;
	private detail: Detail | undefined;
	private journal: string[] = [];
	private journalLabel = "journal";
	private expanded = true;
	private loading = true;
	private refreshing = false;
	private error = "";
	private listPolls = 0;
	private detailRunID = "";
	private eventOffset = 0;
	private eventsComplete = false;

	constructor(private tui: TUI, private theme: Theme, private closeOverlay: () => void) {
		void this.refresh();
		this.timer = setInterval(() => void this.refresh(), 1000);
	}

	private async refresh(): Promise<void> {
		if (this.refreshing) return;
		this.refreshing = true;
		const selectedID = this.runs[this.selected]?.id;
		try {
			if (this.listPolls === 0) {
				this.runs = await listRuns();
				this.listPolls = LIST_POLL_TICKS - 1;
			} else {
				this.listPolls--;
			}
			this.error = "";
			if (selectedID) {
				const index = this.runs.findIndex((run) => run.id === selectedID);
				if (index >= 0) this.selected = index;
			}
			this.selected = Math.max(0, Math.min(this.selected, this.runs.length - 1));
			const run = this.runs[this.selected];
			if (run) {
				if (run.id !== this.detailRunID) {
					this.detailRunID = run.id;
					this.eventOffset = 0;
					this.eventsComplete = false;
					this.detail = { phase: "", agents: [] };
				}
				let detail = this.detail;
				if (!detail) {
					detail = { phase: "", agents: [] };
					this.detail = detail;
				}
				if (!this.eventsComplete) {
					const batch = await readEvents(run.id, this.eventOffset);
					if (batch.reset) {
						detail = { phase: "", agents: [] };
						this.detail = detail;
					}
					applyEvents(detail, batch.events);
					this.eventOffset = batch.next;
					this.eventsComplete = run.status !== "running" && batch.complete;
				}
				const active = [...detail.agents].reverse().find((agent) => agent.status === "running");
				this.journal = await journalTail(run.id, active?.id);
				this.journalLabel = active ? `${active.label} journal` : "completion journal";
			} else {
				this.detailRunID = "";
				this.eventOffset = 0;
				this.eventsComplete = false;
				this.detail = undefined;
				this.journal = [];
			}
		} catch (error) {
			this.error = errorText(error);
			this.detailRunID = "";
			this.eventOffset = 0;
			this.eventsComplete = false;
			this.detail = undefined;
			this.journal = [];
		} finally {
			this.loading = false;
			this.refreshing = false;
			this.tui.requestRender();
		}
	}

	handleInput(data: string): void {
		if (matchesKey(data, "escape") || data === "q" || data === "Q") {
			this.dispose();
			this.closeOverlay();
			return;
		}
		if (matchesKey(data, "up") || data === "k") {
			this.selected = Math.max(0, this.selected - 1);
			void this.refresh();
		} else if (matchesKey(data, "down") || data === "j") {
			this.selected = Math.max(0, Math.min(this.runs.length - 1, this.selected + 1));
			void this.refresh();
		} else if (matchesKey(data, "return")) {
			this.expanded = !this.expanded;
		}
		this.tui.requestRender();
	}

	render(width: number): string[] {
		const th = this.theme;
		const lines = [
			th.fg("accent", th.bold(`dyna runs — session ${SESSION}`)),
			th.fg("dim", "↑/↓ or j/k select • Enter details • q/Esc close"),
			"",
		];
		if (this.loading) lines.push(th.fg("dim", "Loading dyna runs…"));
		else if (this.error) lines.push(th.fg("error", `dyna unavailable: ${this.error}`));
		else if (this.runs.length === 0) {
			lines.push("No workflow runs in this session yet.");
			lines.push(th.fg("dim", "Ask the model to start one, or see /skill dyna."));
		} else {
			const start = Math.max(0, Math.min(this.selected - 4, this.runs.length - 9));
			for (let i = start; i < Math.min(this.runs.length, start + 9); i++) {
				const run = this.runs[i]!;
				const marker = i === this.selected ? th.fg("accent", "›") : " ";
				const color = run.status === "ok" ? "success" : run.status === "error" ? "error" : run.status === "running" ? "warning" : "dim";
				lines.push(`${marker} ${th.fg(color, statusGlyph(run.status))} ${run.name}  ${th.fg("dim", `${run.id}  ${started(run)}  ${elapsed(run)}`)}`);
			}
			if (this.expanded) this.renderDetail(lines);
		}
		return lines.map((line) => truncateToWidth(line, width));
	}

	private renderDetail(lines: string[]): void {
		if (!this.detail) return;
		const counts = { queued: 0, running: 0, ok: 0, error: 0 };
		for (const agent of this.detail.agents) {
			if (agent.status in counts) counts[agent.status as keyof typeof counts]++;
		}
		lines.push("");
		lines.push(this.theme.fg("accent", this.theme.bold("selected run")));
		lines.push(`phase: ${this.detail.phase || "(none)"}`);
		lines.push(`agents: ${counts.running} running • ${counts.queued} queued • ${counts.ok} ok • ${counts.error} error`);
		if (this.journal.length > 0) {
			lines.push(this.theme.fg("dim", `${this.journalLabel}:`));
			for (const entry of this.journal.slice(-5)) lines.push(`  ${entry}`);
		}
	}

	invalidate(): void {}

	dispose(): void {
		if (this.timer) clearInterval(this.timer);
		this.timer = undefined;
	}
}

export default function (pi: ExtensionAPI) {
	if (!SESSION) return;

	pi.on("input", async (_event, ctx) => {
		if (!CODEX_AUTH_ENABLED || ctx.model?.provider !== CODEX_PROVIDER) return;
		try {
			await installCodexAccess(ctx);
		} catch (error) {
			reportCodexAuthFailure(ctx, error);
			return { action: "handled" as const };
		}
	});

	pi.on("model_select", async (event, ctx) => {
		if (!CODEX_AUTH_ENABLED) return;
		if (event.model.provider !== CODEX_PROVIDER) {
			clearCodexAccess(ctx);
			return;
		}
		try {
			await installCodexAccess(ctx);
		} catch (error) {
			reportCodexAuthFailure(ctx, error);
		}
	});

	pi.on("session_shutdown", () => {
		if (codexRefreshTimer) clearTimeout(codexRefreshTimer);
		codexRefreshTimer = undefined;
	});

	pi.registerTool({
		name: "dyna_steer",
		label: "Steer Dyna Worker",
		description: "Send a short steering message to an active worker in a Dyna workflow launched by this pi session. Dyna continues the existing resumable worker session and never starts a replacement.",
		promptSnippet: "Steer an active Dyna workflow worker in its existing session",
		promptGuidelines: ["Use dyna_steer when the user asks to redirect or clarify work for a running Dyna worker; provide the run ID and numeric agent ID shown by Dyna."],
		parameters: Type.Object({
			run_id: Type.String({ description: "Dyna workflow run ID (wf_...)", minLength: 4 }),
			agent_id: Type.Integer({ description: "Numeric ID of the running worker", minimum: 1 }),
			message: Type.String({ description: "Short instruction to apply to the worker's current task", minLength: 1, maxLength: 2000 }),
		}),
		async execute(_toolCallId, params) {
			const run = (await listRuns()).find((candidate) => candidate.id === params.run_id);
			if (!run) throw new Error(`run ${params.run_id} does not belong to this pi session`);
			if (run.status !== "running") throw new Error(`run ${params.run_id} is not running (status ${run.status})`);
			const output = (await dyna(["runs", "steer", params.run_id, String(params.agent_id), params.message])).trim();
			return {
				content: [{ type: "text", text: output || `Queued steering for ${params.run_id} agent ${params.agent_id}.` }],
				details: { runId: params.run_id, agentId: params.agent_id, queued: true },
			};
		},
	});

	pi.registerCommand("dyna", {
		description: "Show live dyna workflow runs from this session",
		handler: async (_args, ctx) => {
			if (ctx.mode !== "tui") {
				ctx.ui.notify("/dyna requires the interactive TUI", "error");
				return;
			}
			await ctx.ui.custom(
				(tui, theme, _keys, done) => new DynaRunsOverlay(tui, theme, () => done(undefined)),
				{ overlay: true, overlayOptions: { anchor: "center", width: "80%", maxHeight: "80%", margin: 1 } },
			);
		},
	});

	pi.on("session_start", async (_event, ctx) => {
		if (CODEX_AUTH_ENABLED && ctx.model?.provider === CODEX_PROVIDER) {
			try {
				await installCodexAccess(ctx);
			} catch (error) {
				reportCodexAuthFailure(ctx, error);
			}
		}
		try {
			const runs = await listRuns();
			const running = runs.filter((run) => run.status === "running").length;
			if (running > 0) ctx.ui.setStatus("dyna", `${running} dyna run(s) — /dyna`);
		} catch {
			// The on-demand overlay reports CLI errors; startup stays quiet.
		}
	});
}
