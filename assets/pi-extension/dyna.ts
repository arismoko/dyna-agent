import { execFile, spawn } from "node:child_process";
import { mkdtemp, open, readFile, rm, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext, KeybindingsManager, Theme } from "@earendil-works/pi-coding-agent";
import { matchesKey, truncateToWidth, visibleWidth, wrapTextWithAnsi, type Component, type TUI } from "@earendil-works/pi-tui";

const SESSION = process.env.DYNA_SESSION ?? "";
const DYNA = process.env.DYNA_BIN || "dyna";
const MAX_OUTPUT = 16 * 1024 * 1024;
const MAX_TOOL_OUTPUT = 1024 * 1024;
const MAX_WORKFLOW_SOURCE = 512 * 1024;
const MAX_ERROR_DETAIL = 16 * 1024;
const DETACHED_REGISTRATION_GRACE_MS = 15 * 1000;
const DETACHED_REGISTRATION_POLL_MS = 300;
const MAX_EVENT_READ = 4 * 1024 * 1024;
const MAX_JOURNAL_READ = 4 * 1024 * 1024;
const MAX_OBSERVER_EVENTS = 2000;
const MAX_OBSERVER_JOURNAL_ENTRIES = 2000;
const LIST_POLL_TICKS = 5;
const ROOT_AGENT = "dyna-orchestrator";
const ACTIVATE_ALL_TOOLS = process.env.DYNA_PI_ACTIVATE_ALL_TOOLS === "1";
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
	ts?: number;
	id?: number;
	label?: string;
	profile?: string;
	phase?: string;
	title?: string;
	status?: string;
	msg?: string;
	kind?: string;
	preview?: string;
	error?: string;
	durMs?: number;
	cached?: boolean;
};

type Agent = {
	id: number;
	label: string;
	profile: string;
	phase: string;
	status: string;
	journalKind: string;
	journalPreview: string;
	journalTS: number;
	durMs: number;
	error: string;
	cached: boolean;
};

type Detail = {
	phase: string;
	agents: Agent[];
	events: RunEvent[];
	latestTS: number;
};

type EventRead = {
	events: RunEvent[];
	next: number;
	reset: boolean;
	complete: boolean;
};

type AgentJournalEntry = {
	ts: number;
	kind: string;
	message: string;
	next: string;
	source: string;
	phase: string;
	malformed: boolean;
};

type AgentJournalRead = {
	entries: AgentJournalEntry[];
	next: number;
	reset: boolean;
	missing: boolean;
};

type ObserverFocus = "runs" | "agents" | "journal";

type CLIResult = {
	ok: boolean;
	exitCode: number | null;
	signal: string | null;
	stdout: string;
	stderr: string;
	stdoutTruncated: boolean;
	stderrTruncated: boolean;
	spawnError?: string;
};

function clipped(value: string, limit = MAX_TOOL_OUTPUT): { text: string; truncated: boolean } {
	if (Buffer.byteLength(value, "utf8") <= limit) return { text: value, truncated: false };
	const suffix = "\n… output truncated by dyna pi …";
	return { text: Buffer.from(value).subarray(0, Math.max(0, limit - Buffer.byteLength(suffix))).toString("utf8") + suffix, truncated: true };
}

function redactSecrets(value: string): string {
	let redacted = value;
	for (const [key, secret] of Object.entries(process.env)) {
		if (!/(?:TOKEN|KEY|SECRET|PASSWORD|CREDENTIAL|AUTH)/i.test(key) || !secret || secret.length < 4) continue;
		redacted = redacted.split(secret).join("[REDACTED]");
	}
	return redacted
		.replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, "Bearer [REDACTED]")
		.replace(/\b((?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|AUTH)[A-Za-z0-9_-]*)\s*[:=]\s*[^\s,;]+/gi, "$1=[REDACTED]")
		.replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, "[REDACTED JWT]");
}

function runDyna(args: string[], cwd?: string, signal?: AbortSignal): Promise<CLIResult> {
	return new Promise((resolve) => {
		execFile(DYNA, args, { cwd, signal, encoding: "utf8", maxBuffer: MAX_OUTPUT, windowsHide: true }, (error, stdout, stderr) => {
			const safeStderr = redactSecrets(stderr);
			if (!error) {
				resolve({ ok: true, exitCode: 0, signal: null, stdout, stderr: safeStderr, stdoutTruncated: false, stderrTruncated: false });
				return;
			}
			const code = typeof error.code === "number" ? error.code : null;
			const signal = typeof error.signal === "string" ? error.signal : null;
			const spawnError = error.code === "ENOENT"
				? `Dyna binary or working directory not found (${DYNA})`
				: error.name === "AbortError" ? "Dyna command was canceled" : undefined;
			const bufferExceeded = error.code === "ERR_CHILD_PROCESS_STDIO_MAXBUFFER";
			resolve({ ok: false, exitCode: code, signal, stdout, stderr: safeStderr, stdoutTruncated: bufferExceeded, stderrTruncated: bufferExceeded, spawnError });
		});
	});
}

function failedCLI(operation: string, result: CLIResult): Error {
	const detail = clipped(redactSecrets(result.stderr.trim() || result.spawnError || result.stdout.trim() || "no diagnostic output"), MAX_ERROR_DETAIL).text;
	const status = result.signal ? `signal ${result.signal}` : result.exitCode === null ? "spawn error" : `exit ${result.exitCode}`;
	return new Error(`${operation} failed (${status}): ${detail}`);
}

async function dyna(args: string[], cwd?: string, signal?: AbortSignal): Promise<string> {
	const result = await runDyna(args, cwd, signal);
	if (!result.ok) throw failedCLI(`dyna ${args.slice(0, 2).join(" ")}`, result);
	return result.stdout;
}

function toolResult(result: CLIResult, extra: Record<string, unknown> = {}) {
	const stdout = clipped(redactSecrets(result.stdout), MAX_TOOL_OUTPUT / 4);
	const stderr = clipped(result.stderr, MAX_TOOL_OUTPUT / 4);
	const details = {
		...extra,
		...result,
		stdout: stdout.text,
		stderr: stderr.text,
		stdoutTruncated: result.stdoutTruncated || stdout.truncated,
		stderrTruncated: result.stderrTruncated || stderr.truncated,
	};
	const rendered = clipped(JSON.stringify(details, null, 2));
	return {
		content: [{ type: "text" as const, text: rendered.text }],
		details,
	};
}

async function listRuns(signal?: AbortSignal): Promise<Run[]> {
	if (!SESSION) return [];
	const raw = await dyna(["runs", "list", "--json", "--session", SESSION], undefined, signal);
	const parsed: unknown = JSON.parse(raw);
	if (parsed === null) return [];
	if (!Array.isArray(parsed)) throw new Error("dyna returned an invalid run list");
	if (!parsed.every(isRun)) throw new Error("dyna returned an invalid run list");
	return parsed;
}

function requireSession(): string {
	if (!SESSION) throw new Error("Dyna model tools require a dyna pi session (DYNA_SESSION is missing)");
	return SESSION;
}

async function enabledProfiles(): Promise<Record<string, unknown>[]> {
	const raw = await dyna(["profiles", "list", "--json"]);
	if (Buffer.byteLength(raw, "utf8") > MAX_TOOL_OUTPUT) throw new Error("enabled Dyna profile JSON exceeds the Pi tool output limit");
	const parsed: unknown = JSON.parse(raw);
	if (!Array.isArray(parsed) || !parsed.every((value) => typeof value === "object" && value !== null && typeof (value as Record<string, unknown>).name === "string")) {
		throw new Error("dyna returned an invalid profile list");
	}
	return (parsed as Record<string, unknown>[])
		.filter((profile) => profile.disabled !== true)
		.map((profile) => ({
			name: profile.name,
			description: profile.description,
			harness: profile.harness,
			model: profile.model,
			taste: profile.taste,
			intelligence: profile.intelligence,
			cost: profile.cost,
			default: profile.default,
			disableSubagents: profile.disableSubagents,
			maxConcurrent: profile.maxConcurrent,
			maxCallsPerRun: profile.maxCallsPerRun,
		}));
}

function checkRunID(id: string): void {
	if (!/^wf_[A-Za-z0-9_-]+$/.test(id) || id.length > 128) throw new Error("run_id must be a valid Dyna workflow id (wf_...)");
}

async function requireSessionRun(id: string, signal?: AbortSignal): Promise<Run> {
	const session = requireSession();
	checkRunID(id);
	const run = (await listRuns(signal)).find((candidate) => candidate.id === id && candidate.session === session);
	if (!run) throw new Error(`run ${id} does not belong to this Pi session`);
	return run;
}

async function waitForSessionRunRegistration(id: string, signal?: AbortSignal): Promise<Run> {
	const session = requireSession();
	checkRunID(id);
	const timeoutSignal = AbortSignal.timeout(DETACHED_REGISTRATION_GRACE_MS);
	const registrationSignal = signal ? AbortSignal.any([signal, timeoutSignal]) : timeoutSignal;
	try {
		for (;;) {
			const run = (await listRuns(registrationSignal)).find((candidate) => candidate.id === id && candidate.session === session);
			if (run) return run;
			await sleep(DETACHED_REGISTRATION_POLL_MS, undefined, { signal: registrationSignal });
		}
	} catch (error) {
		if (!timeoutSignal.aborted || signal?.aborted) throw error;
	}
	throw new Error(`detached Dyna run ${id} started but did not register in Pi session ${session} within 15 seconds; keep run ID ${id} and inspect it with dyna runs show ${id}`);
}

function checkedString(value: string | undefined, name: string, maxBytes: number): string | undefined {
	if (value === undefined) return undefined;
	if (value.includes("\0")) throw new Error(`${name} must not contain NUL bytes`);
	if (Buffer.byteLength(value, "utf8") > maxBytes) throw new Error(`${name} exceeds the ${maxBytes}-byte limit`);
	return value;
}

function isRun(value: unknown): value is Run {
	if (typeof value !== "object" || value === null) return false;
	const run = value as Record<string, unknown>;
	if (typeof run.id !== "string" || !/^wf_[A-Za-z0-9_-]+$/.test(run.id) || run.id.length > 128) return false;
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
	if (event.ts !== undefined && (typeof event.ts !== "number" || !Number.isFinite(event.ts))) return false;
	if (event.durMs !== undefined && (typeof event.durMs !== "number" || !Number.isFinite(event.durMs))) return false;
	if (event.cached !== undefined && typeof event.cached !== "boolean") return false;
	for (const key of ["label", "profile", "phase", "title", "status", "msg", "kind", "preview", "error"]) {
		if (event[key] !== undefined && typeof event[key] !== "string") return false;
	}
	return true;
}

function blankDetail(): Detail {
	return { phase: "", agents: [], events: [], latestTS: 0 };
}

function applyEvents(detail: Detail, events: RunEvent[]): void {
	const agents = new Map(detail.agents.map((agent) => [agent.id, agent]));

	for (const event of events) {
		if (event.ts && event.ts > detail.latestTS) detail.latestTS = event.ts;
		if (event.t === "phase" && event.title) {
			detail.phase = event.title;
			continue;
		}
		if (event.t === "agent_start" && event.id !== undefined) {
			if (event.phase) detail.phase = event.phase;
			const existing = agents.get(event.id);
			if (existing) {
				existing.label = event.label || existing.label;
				existing.profile = event.profile || existing.profile;
				existing.phase = event.phase || existing.phase || detail.phase;
				existing.status = "queued";
			} else {
				agents.set(event.id, {
					id: event.id,
					label: event.label || `agent ${event.id}`,
					profile: event.profile || "",
					phase: event.phase || detail.phase,
					status: "queued",
					journalKind: "",
					journalPreview: "",
					journalTS: 0,
					durMs: 0,
					error: "",
					cached: false,
				});
			}
			continue;
		}
		if (event.id === undefined) continue;
		const agent = agents.get(event.id);
		if (!agent) continue;
		if (event.t === "agent_run") agent.status = "running";
		if (event.t === "agent_journal") {
			agent.journalKind = event.kind || agent.journalKind;
			agent.journalPreview = event.preview || agent.journalPreview;
			agent.journalTS = event.ts || agent.journalTS;
		}
		if (event.t === "agent_end") {
			agent.status = event.status || "error";
			agent.durMs = event.durMs || 0;
			agent.error = event.error || "";
			agent.cached = event.cached || false;
			if (event.preview) agent.journalPreview = event.preview;
		}
	}

	detail.agents = [...agents.values()];
	detail.events = [...detail.events, ...events].slice(-MAX_OBSERVER_EVENTS);
}

function runsDir(): string {
	const xdg = process.env.XDG_DATA_HOME;
	return xdg ? join(xdg, "dyna", "runs") : join(homedir(), ".local", "share", "dyna", "runs");
}

async function readEvents(id: string, offset: number): Promise<EventRead> {
	checkRunID(id);
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

function displayValue(value: unknown): string {
	if (typeof value === "string") return value;
	if (value === undefined || value === null) return "";
	try {
		return JSON.stringify(value) || String(value);
	} catch {
		return String(value);
	}
}

function safeText(value: unknown, fallback = ""): string {
	const text = displayValue(value)
		.replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
		.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
		.replace(/[\x00-\x1f\x7f-\x9f]/g, " ")
		.replace(/\s+/g, " ")
		.trim();
	return text || fallback;
}

function parseAgentJournalLine(line: string): AgentJournalEntry {
	try {
		const value: unknown = JSON.parse(line);
		if (typeof value !== "object" || value === null) throw new Error("not an object");
		const entry = value as Record<string, unknown>;
		const message = safeText(entry.message ?? entry.error ?? entry.result ?? value, "(empty entry)");
		return {
			ts: typeof entry.ts === "number" && Number.isFinite(entry.ts) ? entry.ts : 0,
			kind: safeText(entry.kind, "note"),
			message,
			next: safeText(entry.next),
			source: safeText(entry.source),
			phase: safeText(entry.phase),
			malformed: false,
		};
	} catch {
		return { ts: 0, kind: "raw", message: safeText(line, "(malformed journal record)"), next: "", source: "", phase: "", malformed: true };
	}
}

async function readAgentJournal(id: string, agentID: number, offset: number): Promise<AgentJournalRead> {
	checkRunID(id);
	if (!Number.isInteger(agentID) || agentID <= 0) throw new Error("invalid Dyna agent ID");
	const path = join(runsDir(), id, "agents", String(agentID), "journal.jsonl");
	let handle: Awaited<ReturnType<typeof open>>;
	try {
		handle = await open(path, "r");
	} catch (error) {
		if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") {
			return { entries: [], next: offset, reset: false, missing: true };
		}
		throw error;
	}
	try {
		const stat = await handle.stat();
		if (!stat.isFile()) throw new Error("dyna agent journal path is not a regular file");
		const reset = offset < 0 || offset > stat.size;
		if (reset) offset = 0;
		const length = Math.min(MAX_JOURNAL_READ, stat.size - offset);
		if (length <= 0) return { entries: [], next: offset, reset, missing: false };

		const buffer = Buffer.alloc(length);
		const { bytesRead } = await handle.read(buffer, 0, length, offset);
		const lastNewline = buffer.lastIndexOf(0x0a, bytesRead - 1);
		if (lastNewline < 0) return { entries: [], next: offset, reset, missing: false };
		const entries = buffer.subarray(0, lastNewline).toString("utf8")
			.split("\n")
			.filter((line) => line.length > 0)
			.map(parseAgentJournalLine);
		return { entries, next: offset + lastNewline + 1, reset, missing: false };
	} finally {
		await handle.close();
	}
}

function errorText(error: unknown): string {
	if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") {
		return "file not found";
	}
	if (error instanceof Error) return safeText(error.message.split("\n")[0], "unknown error");
	return safeText(error, "unknown error");
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

function journalTime(ts: number): string {
	if (!Number.isFinite(ts) || ts <= 0) return "time unknown";
	const date = new Date(ts);
	if (!Number.isFinite(date.getTime())) return "time unknown";
	return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function freshness(ts: number): string {
	if (!Number.isFinite(ts) || ts <= 0) return "";
	const seconds = Math.max(0, Math.floor((Date.now() - ts) / 1000));
	if (seconds < 5) return "now";
	if (seconds < 60) return `${seconds}s ago`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m ago`;
	return `${Math.floor(minutes / 60)}h ago`;
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

function statusColor(status: string): "success" | "error" | "warning" | "dim" {
	if (status === "ok") return "success";
	if (status === "error") return "error";
	if (status === "running" || status === "queued") return "warning";
	return "dim";
}

function padLine(line: string, width: number): string {
	const clipped = truncateToWidth(line, Math.max(0, width), "");
	return clipped + " ".repeat(Math.max(0, width - visibleWidth(clipped)));
}

function selectionWindow(selected: number, total: number, height: number): number {
	if (height <= 0 || total <= height) return 0;
	return Math.max(0, Math.min(selected - Math.floor(height / 2), total - height));
}

class DynaRunsOverlay implements Component {
	private timer: ReturnType<typeof setInterval> | undefined;
	private runs: Run[] = [];
	private selectedRunID = "";
	private selectedAgentID: number | undefined;
	private focus: ObserverFocus = "runs";
	private detail = blankDetail();
	private journal: AgentJournalEntry[] = [];
	private loading = true;
	private refreshing = false;
	private refreshQueued = false;
	private forceListQueued = false;
	private disposed = false;
	private abortController: AbortController | undefined;
	private listError = "";
	private eventError = "";
	private journalError = "";
	private listPolls = 0;
	private detailRunID = "";
	private eventOffset = 0;
	private eventsComplete = false;
	private journalOffset = 0;
	private journalLoaded = false;
	private journalMissing = false;
	private journalFollow = true;
	private journalScroll = 0;
	private journalUnseen = 0;
	private selectionGeneration = 0;
	private lastRunsPage = 5;
	private lastAgentsPage = 5;
	private lastJournalViewport = 5;
	private lastJournalLineCount = 0;

	constructor(
		private tui: TUI,
		private theme: Theme,
		private keys: KeybindingsManager,
		private closeOverlay: () => void,
	) {
		this.requestRefresh(true);
		this.timer = setInterval(() => this.requestRefresh(), 1000);
	}

	private requestRefresh(forceList = false): void {
		if (this.disposed) return;
		if (forceList) this.listPolls = 0;
		if (this.refreshing) {
			this.refreshQueued = true;
			this.forceListQueued = this.forceListQueued || forceList;
			return;
		}
		void this.refresh(forceList);
	}

	private async refresh(forceList: boolean): Promise<void> {
		this.refreshing = true;
		const controller = new AbortController();
		this.abortController = controller;
		try {
			if (forceList || this.listPolls === 0) {
				try {
					const runs = await listRuns(controller.signal);
					if (this.disposed) return;
					this.applyRunList(runs);
					this.listError = "";
					this.listPolls = LIST_POLL_TICKS - 1;
				} catch (error) {
					if (this.disposed || controller.signal.aborted) return;
					this.listError = errorText(error);
				}
			} else {
				this.listPolls--;
			}

			const run = this.selectedRun();
			if (!run) return;
			if (run.id !== this.detailRunID) this.activateRun(run.id);
			const eventGeneration = this.selectionGeneration;
			if (!this.eventsComplete) {
				try {
					const batch = await readEvents(run.id, this.eventOffset);
					if (this.disposed || eventGeneration !== this.selectionGeneration || run.id !== this.selectedRunID) return;
					if (batch.reset) {
						this.detail = blankDetail();
						this.eventOffset = 0;
					}
					applyEvents(this.detail, batch.events);
					this.eventOffset = batch.next;
					this.eventsComplete = run.status !== "running" && batch.complete;
					this.eventError = "";
					this.reconcileAgentSelection();
				} catch (error) {
					if (this.disposed) return;
					this.eventError = errorText(error);
				}
			}

			const agentID = this.selectedAgentID;
			const journalGeneration = this.selectionGeneration;
			if (agentID !== undefined) {
				try {
					const batch = await readAgentJournal(run.id, agentID, this.journalOffset);
					if (this.disposed || journalGeneration !== this.selectionGeneration || run.id !== this.selectedRunID || agentID !== this.selectedAgentID) return;
					this.applyJournalRead(batch);
					this.journalError = "";
				} catch (error) {
					if (this.disposed) return;
					this.journalError = errorText(error);
				}
			}
		} finally {
			if (this.abortController === controller) this.abortController = undefined;
			this.loading = false;
			this.refreshing = false;
			if (this.disposed) return;
			const queued = this.refreshQueued;
			const queuedForceList = this.forceListQueued;
			this.refreshQueued = false;
			this.forceListQueued = false;
			this.tui.requestRender();
			if (queued) this.requestRefresh(queuedForceList);
		}
	}

	private applyRunList(runs: Run[]): void {
		this.runs = runs;
		if (runs.length === 0) {
			if (this.selectedRunID) this.activateRun("");
			return;
		}
		if (!this.selectedRunID || !runs.some((run) => run.id === this.selectedRunID)) {
			this.activateRun(runs[0]!.id);
		}
	}

	private selectedRun(): Run | undefined {
		return this.runs.find((run) => run.id === this.selectedRunID);
	}

	private selectedAgent(): Agent | undefined {
		return this.detail.agents.find((agent) => agent.id === this.selectedAgentID);
	}

	private activateRun(id: string): void {
		if (id === this.detailRunID && id === this.selectedRunID) return;
		this.selectedRunID = id;
		this.detailRunID = id;
		this.detail = blankDetail();
		this.eventOffset = 0;
		this.eventsComplete = false;
		this.selectedAgentID = undefined;
		this.eventError = "";
		this.resetJournal();
		this.selectionGeneration++;
	}

	private reconcileAgentSelection(): void {
		const current = this.selectedAgentID;
		if (current !== undefined && this.detail.agents.some((agent) => agent.id === current)) return;
		const next = this.detail.agents[0]?.id;
		if (next !== current) {
			this.selectedAgentID = next;
			this.resetJournal();
			this.selectionGeneration++;
		}
	}

	private resetJournal(): void {
		this.journal = [];
		this.journalOffset = 0;
		this.journalLoaded = false;
		this.journalMissing = false;
		this.journalFollow = true;
		this.journalScroll = 0;
		this.journalUnseen = 0;
		this.journalError = "";
	}

	private applyJournalRead(batch: AgentJournalRead): void {
		if (batch.reset) this.resetJournal();
		this.journalLoaded = true;
		this.journalMissing = batch.missing;
		this.journalOffset = batch.next;
		if (batch.entries.length === 0) return;
		this.journal = [...this.journal, ...batch.entries].slice(-MAX_OBSERVER_JOURNAL_ENTRIES);
		this.journalMissing = false;
		if (this.journalFollow) {
			this.journalUnseen = 0;
		} else {
			this.journalUnseen += batch.entries.length;
		}
	}

	private moveRun(delta: number): void {
		if (this.runs.length === 0) return;
		const current = Math.max(0, this.runs.findIndex((run) => run.id === this.selectedRunID));
		const next = Math.max(0, Math.min(this.runs.length - 1, current + delta));
		if (next === current) return;
		this.activateRun(this.runs[next]!.id);
		this.requestRefresh();
	}

	private moveAgent(delta: number): void {
		if (this.detail.agents.length === 0) return;
		const current = Math.max(0, this.detail.agents.findIndex((agent) => agent.id === this.selectedAgentID));
		const next = Math.max(0, Math.min(this.detail.agents.length - 1, current + delta));
		if (next === current) return;
		this.selectedAgentID = this.detail.agents[next]!.id;
		this.resetJournal();
		this.selectionGeneration++;
		this.requestRefresh();
	}

	private close(): void {
		this.dispose();
		this.closeOverlay();
	}

	handleInput(data: string): void {
		if (matchesKey(data, "ctrl+c") || data === "q" || data === "Q") {
			this.close();
			return;
		}
		if (this.keys.matches(data, "tui.select.cancel")) {
			if (this.focus === "journal") this.focus = "agents";
			else if (this.focus === "agents") this.focus = "runs";
			else {
				this.close();
				return;
			}
			this.tui.requestRender();
			return;
		}
		if (data === "r" || data === "R") {
			this.requestRefresh(true);
			this.tui.requestRender();
			return;
		}
		if (matchesKey(data, "tab")) {
			this.focus = this.focus === "runs" ? "agents" : this.focus === "agents" ? "journal" : "runs";
			this.tui.requestRender();
			return;
		}
		if (data === "h" || data === "H" || matchesKey(data, "left") || matchesKey(data, "backspace")) {
			if (this.focus === "journal") this.focus = "agents";
			else if (this.focus === "agents") this.focus = "runs";
			this.tui.requestRender();
			return;
		}
		if (this.keys.matches(data, "tui.select.confirm") || data === "l" || data === "L" || matchesKey(data, "right")) {
			if (this.focus === "runs") this.focus = "agents";
			else if (this.focus === "agents" && this.selectedAgentID !== undefined) this.focus = "journal";
			this.tui.requestRender();
			return;
		}

		const up = this.keys.matches(data, "tui.select.up") || data === "k";
		const down = this.keys.matches(data, "tui.select.down") || data === "j";
		const pageUp = this.keys.matches(data, "tui.select.pageUp");
		const pageDown = this.keys.matches(data, "tui.select.pageDown");
		if (this.focus === "runs") {
			if (up) this.moveRun(-1);
			else if (down) this.moveRun(1);
			else if (pageUp) this.moveRun(-this.lastRunsPage);
			else if (pageDown) this.moveRun(this.lastRunsPage);
			else if (matchesKey(data, "home")) this.moveRun(-this.runs.length);
			else if (matchesKey(data, "end")) this.moveRun(this.runs.length);
		} else if (this.focus === "agents") {
			if (up) this.moveAgent(-1);
			else if (down) this.moveAgent(1);
			else if (pageUp) this.moveAgent(-this.lastAgentsPage);
			else if (pageDown) this.moveAgent(this.lastAgentsPage);
			else if (matchesKey(data, "home")) this.moveAgent(-this.detail.agents.length);
			else if (matchesKey(data, "end")) this.moveAgent(this.detail.agents.length);
		} else {
			if (data === "f" || data === "F") {
				this.journalFollow = !this.journalFollow;
				if (this.journalFollow) {
					this.journalScroll = Math.max(0, this.lastJournalLineCount - this.lastJournalViewport);
					this.journalUnseen = 0;
				}
			} else if (data === "g" || matchesKey(data, "home")) {
				this.journalFollow = false;
				this.journalScroll = 0;
			} else if (data === "G" || matchesKey(data, "end")) {
				this.journalFollow = true;
				this.journalScroll = Math.max(0, this.lastJournalLineCount - this.lastJournalViewport);
				this.journalUnseen = 0;
			} else if (up) this.scrollJournal(-1);
			else if (down) this.scrollJournal(1);
			else if (pageUp) this.scrollJournal(-this.lastJournalViewport);
			else if (pageDown) this.scrollJournal(this.lastJournalViewport);
		}
		this.tui.requestRender();
	}

	private scrollJournal(delta: number): void {
		this.journalFollow = false;
		const max = Math.max(0, this.lastJournalLineCount - this.lastJournalViewport);
		this.journalScroll = Math.max(0, Math.min(max, this.journalScroll + delta));
	}

	render(width: number): string[] {
		const safeWidth = Math.max(1, width);
		const rowBudget = Math.max(1, Math.floor(this.tui.terminal.rows * 0.9));
		const header = this.renderHeader(safeWidth);
		const footer = this.theme.fg("dim", this.focus === "journal"
			? "j/k scroll · PgUp/PgDn page · g/G ends · f follow · h back · r refresh · q close"
			: "j/k move · PgUp/PgDn page · Enter/l open · h back · Tab pane · r refresh · q close");
		const warning = this.renderWarning(safeWidth);
		const fixed = header.length + warning.length + 1;
		const available = Math.max(1, rowBudget - fixed);
		let body: string[];
		if (safeWidth >= 72 && available >= 8) {
			const topHeight = Math.max(3, Math.min(7, Math.floor(available * 0.42)));
			const journalHeight = Math.max(1, available - topHeight);
			const leftWidth = Math.max(24, Math.floor((safeWidth - 2) * 0.46));
			const rightWidth = Math.max(1, safeWidth - 2 - leftWidth);
			const runs = this.renderRunsPane(leftWidth, topHeight);
			const agents = this.renderAgentsPane(rightWidth, topHeight);
			const top: string[] = [];
			for (let i = 0; i < topHeight; i++) {
				top.push(`${padLine(runs[i] || "", leftWidth)}  ${padLine(agents[i] || "", rightWidth)}`);
			}
			body = [...top, ...this.renderJournalPane(safeWidth, journalHeight)];
		} else if (this.focus === "runs") {
			body = this.renderRunsPane(safeWidth, available);
		} else if (this.focus === "agents") {
			body = this.renderAgentsPane(safeWidth, available);
		} else {
			body = this.renderJournalPane(safeWidth, available);
		}
		return [...header, ...warning, ...body, footer]
			.slice(0, rowBudget)
			.map((line) => truncateToWidth(line, safeWidth, ""));
	}

	private renderHeader(width: number): string[] {
		const th = this.theme;
		const title = `${th.fg("accent", th.bold("dyna observer"))}${th.fg("dim", ` · session ${safeText(SESSION, "unknown")}`)}`;
		const run = this.selectedRun();
		if (!run) {
			return [title, th.fg("dim", this.loading ? "Loading session runs…" : "No workflow run selected"), ""];
		}
		const status = `${th.fg(statusColor(run.status), statusGlyph(run.status))} ${safeText(run.status, "unknown")}`;
		const phase = safeText(this.detail.phase, "waiting for phase");
		const summary = `${status}  ${th.bold(safeText(run.name, run.id))}  ${th.fg("dim", `${safeText(run.id)} · ${elapsed(run)} · ${phase}`)}`;
		const counts = { queued: 0, running: 0, completed: 0, error: 0 };
		for (const agent of this.detail.agents) {
			if (agent.status === "queued") counts.queued++;
			else if (agent.status === "running") counts.running++;
			else if (agent.status === "error") counts.error++;
			else counts.completed++;
		}
		const fresh = freshness(this.detail.latestTS);
		const facts = [
			th.fg("warning", `● ${counts.running} running`),
			th.fg("dim", `◌ ${counts.queued} queued`),
			th.fg("success", `✓ ${counts.completed} done`),
			th.fg("error", `✗ ${counts.error} error`),
			fresh ? th.fg("dim", `updated ${fresh}`) : "",
		].filter(Boolean).join("  ");
		return [truncateToWidth(title, width, ""), truncateToWidth(summary, width, "…"), truncateToWidth(facts, width, "…")];
	}

	private renderWarning(width: number): string[] {
		const warnings = [
			this.listError ? `runs: ${this.listError}` : "",
			this.eventError ? `events: ${this.eventError}` : "",
			this.journalError ? `journal: ${this.journalError}` : "",
		].filter(Boolean);
		return warnings.length > 0 ? [this.theme.fg("error", truncateToWidth(`retrying · ${warnings.join(" · ")}`, width, "…"))] : [];
	}

	private paneTitle(label: string, active: boolean, suffix = ""): string {
		const title = `${label}${suffix ? `  ${suffix}` : ""}`;
		return active ? this.theme.fg("accent", this.theme.bold(title)) : this.theme.fg("dim", this.theme.bold(title));
	}

	private renderRunsPane(width: number, height: number): string[] {
		if (height <= 0) return [];
		const selected = Math.max(0, this.runs.findIndex((run) => run.id === this.selectedRunID));
		const bodyHeight = Math.max(0, height - 1);
		this.lastRunsPage = Math.max(1, bodyHeight);
		const lines = [this.paneTitle("RUNS", this.focus === "runs", this.runs.length ? `${selected + 1}/${this.runs.length}` : "")];
		if (bodyHeight === 0) return lines;
		if (this.loading && this.runs.length === 0) lines.push(this.theme.fg("dim", "Loading…"));
		else if (this.runs.length === 0) lines.push(this.theme.fg("dim", "No runs in this Pi session yet."));
		else {
			const startIndex = selectionWindow(selected, this.runs.length, bodyHeight);
			for (let i = startIndex; i < Math.min(this.runs.length, startIndex + bodyHeight); i++) {
				const run = this.runs[i]!;
				const marker = run.id === this.selectedRunID ? this.theme.fg("accent", "›") : " ";
				const state = this.theme.fg(statusColor(run.status), statusGlyph(run.status));
				const meta = this.theme.fg("dim", ` · ${elapsed(run)} · ${started(run)}`);
				lines.push(`${marker} ${state} ${safeText(run.name, run.id)}${meta}`);
			}
		}
		return lines.slice(0, height).map((line) => truncateToWidth(line, width, "…"));
	}

	private renderAgentsPane(width: number, height: number): string[] {
		if (height <= 0) return [];
		const selected = Math.max(0, this.detail.agents.findIndex((agent) => agent.id === this.selectedAgentID));
		const bodyHeight = Math.max(0, height - 1);
		this.lastAgentsPage = Math.max(1, bodyHeight);
		const lines = [this.paneTitle("AGENTS", this.focus === "agents", this.detail.agents.length ? `${selected + 1}/${this.detail.agents.length}` : "")];
		if (bodyHeight === 0) return lines;
		if (!this.selectedRun()) lines.push(this.theme.fg("dim", "Select a run first."));
		else if (this.detail.agents.length === 0) lines.push(this.theme.fg("dim", this.eventError ? "Agent roster unavailable." : "No agents have started yet."));
		else {
			const startIndex = selectionWindow(selected, this.detail.agents.length, bodyHeight);
			for (let i = startIndex; i < Math.min(this.detail.agents.length, startIndex + bodyHeight); i++) {
				const agent = this.detail.agents[i]!;
				const marker = agent.id === this.selectedAgentID ? this.theme.fg("accent", "›") : " ";
				const state = this.theme.fg(statusColor(agent.status), statusGlyph(agent.status));
				const profile = agent.profile ? ` [${safeText(agent.profile)}]` : "";
				const fresh = freshness(agent.journalTS);
				const meta = fresh ? this.theme.fg("dim", ` · ${fresh}`) : "";
				lines.push(`${marker} ${state} ${safeText(agent.label, `agent ${agent.id}`)}${this.theme.fg("dim", profile)}${meta}`);
			}
		}
		return lines.slice(0, height).map((line) => truncateToWidth(line, width, "…"));
	}

	private renderJournalPane(width: number, height: number): string[] {
		if (height <= 0) return [];
		const agent = this.selectedAgent();
		const identity = agent ? safeText(agent.label, `agent ${agent.id}`) : "no agent selected";
		const follow = this.journalFollow ? this.theme.fg("success", "● FOLLOW") : this.theme.fg("dim", "FOLLOW OFF");
		const unseen = this.journalUnseen > 0 ? this.theme.fg("warning", `${this.journalUnseen} unseen`) : "";
		const suffix = [identity, follow, unseen].filter(Boolean).join(" · ");
		const lines = [this.paneTitle("JOURNAL", this.focus === "journal", suffix)];
		const viewport = Math.max(0, height - 1);
		this.lastJournalViewport = Math.max(1, viewport);
		if (viewport === 0) return lines;
		const body = this.renderJournalBody(width);
		this.lastJournalLineCount = body.length;
		const maxScroll = Math.max(0, body.length - viewport);
		if (this.journalFollow) {
			this.journalScroll = maxScroll;
			this.journalUnseen = 0;
		} else {
			this.journalScroll = Math.max(0, Math.min(maxScroll, this.journalScroll));
		}
		return [...lines, ...body.slice(this.journalScroll, this.journalScroll + viewport)]
			.slice(0, height)
			.map((line) => truncateToWidth(line, width, ""));
	}

	private renderJournalBody(width: number): string[] {
		const agent = this.selectedAgent();
		if (!agent) return [this.theme.fg("dim", this.detail.agents.length ? "Select an agent to read its journal." : "Waiting for an agent to start…")];
		if (this.journal.length === 0) {
			if (!this.journalLoaded) return [this.theme.fg("dim", "Loading agent journal…")];
			if (agent.cached) return [this.theme.fg("warning", "Cached completion · no live journal was produced.")];
			if (agent.status === "ok" || agent.status === "error" || agent.status === "canceled") {
				return [this.theme.fg("dim", "This agent completed without a recorded work journal.")];
			}
			return [this.theme.fg("dim", this.journalMissing ? "Waiting for the journal file…" : "Waiting for the first journal entry…")];
		}

		const lines: string[] = [];
		let lastPhase = "";
		for (const entry of this.journal) {
			if (entry.phase && entry.phase !== lastPhase) {
				if (lines.length > 0) lines.push("");
				lines.push(this.theme.fg("accent", `▮ ${entry.phase}`));
				lastPhase = entry.phase;
			}
			const kind = entry.malformed ? "RAW" : entry.kind.toUpperCase();
			const meta = [journalTime(entry.ts), entry.source].filter(Boolean).join(" · ");
			lines.push(`${this.theme.fg(entry.malformed ? "warning" : "accent", this.theme.bold(kind))}${this.theme.fg("dim", `  ${meta}`)}`);
			for (const line of wrapTextWithAnsi(entry.message, Math.max(1, width - 2))) lines.push(`  ${line}`);
			if (entry.next) {
				const prefix = this.theme.fg("dim", "next → ");
				const wrapped = wrapTextWithAnsi(entry.next, Math.max(1, width - 7));
				for (let i = 0; i < wrapped.length; i++) lines.push(`${i === 0 ? prefix : "       "}${wrapped[i]}`);
			}
			lines.push("");
		}
		if (lines.at(-1) === "") lines.pop();
		return lines;
	}

	invalidate(): void {}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		if (this.timer) clearInterval(this.timer);
		this.timer = undefined;
		this.abortController?.abort();
		this.abortController = undefined;
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
		name: "dyna_profiles",
		label: "Dyna Profiles",
		description: "Return the enabled Dyna worker profiles with routing stats and per-run limits. Call this before authoring a workflow.",
		promptSnippet: "List enabled Dyna worker profiles",
		promptGuidelines: ["Call dyna_profiles before dyna_run, then choose profiles by cost, intelligence, taste, description, and limits."],
		parameters: Type.Object({}),
		async execute() {
			requireSession();
			const profiles = await enabledProfiles();
			const rendered = clipped(JSON.stringify(profiles, null, 2));
			return {
				content: [{ type: "text" as const, text: rendered.text }],
				details: { profiles, count: profiles.length, truncated: rendered.truncated },
			};
		},
	});

	pi.registerTool({
		name: "dyna_run",
		label: "Run Dyna Workflow",
		description: "Run a bounded inline JavaScript Dyna workflow with typed options. The extension uses the exact Dyna binary without a shell and removes its private temporary script after launch.",
		promptSnippet: "Run an inline Dyna JavaScript workflow",
		promptGuidelines: ["Pass the complete workflow inline after calling dyna_profiles. Prefer detach for work that should continue while this Pi session remains interactive."],
		parameters: Type.Object({
			workflow: Type.String({ description: "Complete inline Dyna JavaScript workflow", minLength: 1, maxLength: MAX_WORKFLOW_SOURCE }),
			cwd: Type.Optional(Type.String({ description: "Working directory for workers", minLength: 1, maxLength: 4096 })),
			args: Type.Optional(Type.Unknown({ description: "JSON value exposed to the workflow as args" })),
			name: Type.Optional(Type.String({ description: "Run display name", minLength: 1, maxLength: 200 })),
			detach: Type.Optional(Type.Boolean({ description: "Start in the background and return its run id" })),
			resume: Type.Optional(Type.String({ description: "Session-owned run id whose successful calls may be reused", pattern: "^wf_[A-Za-z0-9_-]+$", maxLength: 128 })),
			max_concurrent: Type.Optional(Type.Integer({ description: "Maximum simultaneous workers for this run", minimum: 1, maximum: 64 })),
			call_cap: Type.Optional(Type.Integer({ description: "Maximum lifetime agent calls for this run", minimum: 1, maximum: 1000 })),
		}),
		async execute(_toolCallId, params, signal) {
			requireSession();
			checkedString(params.workflow, "workflow", MAX_WORKFLOW_SOURCE);
			const cwd = checkedString(params.cwd, "cwd", 4096);
			const name = checkedString(params.name, "name", 1024);
			if (params.resume) await requireSessionRun(params.resume, signal);

			let argsJSON: string | undefined;
			if (params.args !== undefined) {
				argsJSON = JSON.stringify(params.args);
				checkedString(argsJSON, "args", MAX_WORKFLOW_SOURCE);
			}

			const tempDir = await mkdtemp(join(tmpdir(), "dyna-pi-"));
			const scriptPath = join(tempDir, "workflow.js");
			try {
				await writeFile(scriptPath, params.workflow, { mode: 0o600, flag: "wx" });
				const cliArgs = ["run", scriptPath, "--json"];
				if (cwd) cliArgs.push("--dir", cwd);
				if (argsJSON !== undefined) cliArgs.push("--args", argsJSON);
				if (name) cliArgs.push("--name", name);
				if (params.detach) cliArgs.push("--detach");
				if (params.resume) cliArgs.push("--resume", params.resume);
				if (params.max_concurrent !== undefined) cliArgs.push("--max-concurrent", String(params.max_concurrent));
				if (params.call_cap !== undefined) cliArgs.push("--max-agents", String(params.call_cap));

				const result = await runDyna(cliArgs, undefined, signal);
				if (!result.ok) throw failedCLI("dyna run", result);
				let runID: string | undefined;
				if (params.detach) {
					const detachedRunID = result.stdout.trim();
					try {
						checkRunID(detachedRunID);
					} catch {
						const safeOutput = clipped(redactSecrets(detachedRunID), MAX_ERROR_DETAIL).text;
						throw new Error(`dyna run --detach returned an invalid run ID ${JSON.stringify(safeOutput)}; the detached child may still be running`);
					}
					await waitForSessionRunRegistration(detachedRunID, signal);
					runID = detachedRunID;
				} else {
					try {
						const value = JSON.parse(result.stdout) as Record<string, unknown>;
						runID = typeof value.runId === "string" ? value.runId : undefined;
					} catch {
						// Non-detached output may not carry a run ID; preserve the successful result.
					}
				}
				return toolResult(result, { runId: runID, detached: params.detach === true });
			} finally {
				await rm(tempDir, { recursive: true, force: true });
			}
		},
	});

	pi.registerTool({
		name: "dyna_runs",
		label: "Manage Dyna Runs",
		description: "List, inspect, wait for, or cancel Dyna runs owned by this Pi launch. Run-id operations reject runs from every other session.",
		promptSnippet: "Manage Dyna runs from this Pi session",
		promptGuidelines: ["Use dyna_runs instead of shell commands for session-scoped list, show, wait, and cancel operations."],
		parameters: Type.Object({
			action: Type.Union([Type.Literal("list"), Type.Literal("show"), Type.Literal("wait"), Type.Literal("cancel")], { description: "Run operation" }),
			run_id: Type.Optional(Type.String({ description: "Required for show, wait, and cancel", pattern: "^wf_[A-Za-z0-9_-]+$", maxLength: 128 })),
			timeout_seconds: Type.Optional(Type.Integer({ description: "Wait timeout without canceling the run", minimum: 1, maximum: 86400 })),
		}),
		async execute(_toolCallId, params, signal) {
			const session = requireSession();
			if (params.action === "list") {
				if (params.run_id || params.timeout_seconds !== undefined) throw new Error("dyna_runs list does not accept run_id or timeout_seconds");
				const result = await runDyna(["runs", "list", "--json", "--session", session], undefined, signal);
				if (!result.ok) throw failedCLI("dyna runs list", result);
				return toolResult(result, { action: params.action, session });
			}
			if (!params.run_id) throw new Error(`run_id is required for dyna_runs ${params.action}`);
			if (params.action !== "wait" && params.timeout_seconds !== undefined) throw new Error(`timeout_seconds is only valid for dyna_runs wait`);
			const run = await requireSessionRun(params.run_id, signal);
			const cliArgs = ["runs", params.action, params.run_id];
			if (params.action === "show") cliArgs.push("--json");
			if (params.action === "wait" && params.timeout_seconds !== undefined) cliArgs.push("--timeout", String(params.timeout_seconds));
			const result = await runDyna(cliArgs, undefined, signal);
			if (!result.ok) throw failedCLI(`dyna runs ${params.action}`, result);
			return toolResult(result, { action: params.action, runId: params.run_id, priorStatus: run.status });
		},
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
		async execute(_toolCallId, params, signal) {
			requireSession();
			checkedString(params.message, "message", 2000);
			const run = await requireSessionRun(params.run_id, signal);
			if (run.status !== "running") throw new Error(`run ${params.run_id} is not running (status ${run.status})`);
			const result = await runDyna(["runs", "steer", params.run_id, String(params.agent_id), params.message], undefined, signal);
			if (!result.ok) throw failedCLI("dyna runs steer", result);
			const output = redactSecrets(result.stdout.trim());
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
				(tui, theme, keys, done) => new DynaRunsOverlay(tui, theme, keys, () => done(undefined)),
				{ overlay: true, overlayOptions: { anchor: "center", width: "92%", maxHeight: "90%", margin: 1 } },
			);
		},
	});

	pi.on("session_start", async (_event, ctx) => {
		if (ACTIVATE_ALL_TOOLS) {
			pi.setActiveTools(pi.getAllTools().map((tool) => tool.name));
		}
		ctx.ui.setStatus("dyna-agent", `agent:${ROOT_AGENT}`);

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
