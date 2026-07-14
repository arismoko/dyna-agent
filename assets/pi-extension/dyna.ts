import { execFile, spawn } from "node:child_process";
import { chmod, lstat, mkdtemp, readFile, rename, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const DYNA = process.env.DYNA_BIN || "dyna";
const DYNA_SESSION_ENV = "DYNA_SESSION";
const MAX_OUTPUT = 16 * 1024 * 1024;
const MAX_TOOL_OUTPUT = 1024 * 1024;
const MAX_WORKFLOW_SOURCE = 512 * 1024;
const MAX_ERROR_DETAIL = 16 * 1024;
const DETACHED_REGISTRATION_GRACE_MS = 15 * 1000;
const DETACHED_REGISTRATION_POLL_MS = 300;
const WORKFLOW_FILE_PREFIX = "dyna-workflow-";
const RUN_COMPLETION_POLL_MS = 1000;
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

type TerminalRunUpdate = {
	run: Run;
	messageSent: boolean;
	uiNotified: boolean;
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

function runDyna(args: string[], cwd?: string, signal?: AbortSignal, env?: NodeJS.ProcessEnv): Promise<CLIResult> {
	return new Promise((resolve) => {
		execFile(DYNA, args, { cwd, signal, env, encoding: "utf8", maxBuffer: MAX_OUTPUT, windowsHide: true }, (error, stdout, stderr) => {
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

function sessionID(ctx: ExtensionContext): string {
	const session = ctx.sessionManager.getSessionId();
	if (typeof session !== "string" || session.length === 0 || session.includes("\0") || Buffer.byteLength(session, "utf8") > 128) {
		throw new Error("Dyna model tools require a valid persisted Pi session");
	}
	return session;
}

function sessionEnv(session: string): NodeJS.ProcessEnv {
	return { ...process.env, [DYNA_SESSION_ENV]: session };
}

async function listRuns(session: string, signal?: AbortSignal): Promise<Run[]> {
	const raw = await dyna(["runs", "list", "--json", "--session", session], undefined, signal);
	const parsed: unknown = JSON.parse(raw);
	if (parsed === null) return [];
	if (!Array.isArray(parsed)) throw new Error("dyna returned an invalid run list");
	if (!parsed.every(isRun)) throw new Error("dyna returned an invalid run list");
	return parsed.filter((run) => run.session === session);
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

async function requireSessionRun(id: string, session: string, signal?: AbortSignal, waitForRegistration = false): Promise<Run> {
	checkRunID(id);
	const run = (await listRuns(session, signal)).find((candidate) => candidate.id === id);
	if (run) return run;
	if (waitForRegistration) return await waitForSessionRunRegistration(id, session, signal);
	throw new Error(`run ${id} does not belong to this Pi session`);
}

async function waitForSessionRunRegistration(id: string, session: string, signal?: AbortSignal): Promise<Run> {
	checkRunID(id);
	const timeoutSignal = AbortSignal.timeout(DETACHED_REGISTRATION_GRACE_MS);
	const registrationSignal = signal ? AbortSignal.any([signal, timeoutSignal]) : timeoutSignal;
	try {
		for (;;) {
			const run = (await listRuns(session, registrationSignal)).find((candidate) => candidate.id === id);
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

async function consumeWorkflow(workflowPath: string): Promise<{ tempDir: string; scriptPath: string }> {
	checkedString(workflowPath, "workflow_path", 4096);
	if (!isAbsolute(workflowPath)) throw new Error("workflow_path must be absolute");
	const tempRoot = resolve(tmpdir());
	const sourcePath = resolve(workflowPath);
	const sourceName = basename(sourcePath);
	if (dirname(sourcePath) !== tempRoot || !sourceName.startsWith(WORKFLOW_FILE_PREFIX) || !sourceName.endsWith(".js")) {
		throw new Error(`workflow_path must name a ${WORKFLOW_FILE_PREFIX}*.js file directly under ${tempRoot}`);
	}

	const tempDir = await mkdtemp(join(tempRoot, "dyna-pi-"));
	const scriptPath = join(tempDir, "workflow.js");
	try {
		const source = await lstat(sourcePath);
		if (!source.isFile() || source.isSymbolicLink() || source.size === 0 || source.size > MAX_WORKFLOW_SOURCE) {
			throw new Error(`workflow_path must be a non-empty regular file no larger than ${MAX_WORKFLOW_SOURCE} bytes`);
		}
		// Moving into an extension-owned directory consumes the path before the
		// detached CLI copies it into the durable run directory.
		await rename(sourcePath, scriptPath);
		const staged = await lstat(scriptPath);
		if (!staged.isFile() || staged.isSymbolicLink() || staged.size === 0 || staged.size > MAX_WORKFLOW_SOURCE) {
			throw new Error("workflow file changed while it was being staged");
		}
		await chmod(scriptPath, 0o600);
		return { tempDir, scriptPath };
	} catch (error) {
		await rm(tempDir, { recursive: true, force: true });
		throw error;
	}
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

export default function (pi: ExtensionAPI) {
	// Only runs launched through this extension are eligible for delivery. This
	// avoids treating pre-existing runs from the same Pi session as new work.
	const launchedRunIDs = new Set<string>();
	const pendingRunUpdates = new Map<string, TerminalRunUpdate>();
	const runCompletionAbort = new AbortController();

	function isTerminalRun(run: Run): boolean {
		return run.status === "ok" || run.status === "error" || run.status === "canceled";
	}

	function completionMessage(run: Run): string {
		return `Dyna workflow "${run.name}" (${run.id}) ${run.status}.`;
	}

	function completionNotificationType(run: Run): "info" | "warning" | "error" {
		if (run.status === "error") return "error";
		if (run.status === "canceled") return "warning";
		return "info";
	}

	// Never inject a message while Pi is streaming: its default delivery mode
	// would steer the current agent and cause an unwanted follow-up turn.
	function flushTerminalRunUpdates(ctx: ExtensionContext): void {
		if (!ctx.isIdle()) return;
		for (const [id, update] of pendingRunUpdates) {
			const { run } = update;
			try {
				if (!update.messageSent) {
					pi.sendMessage({
						customType: "dyna_run_terminal",
						content: completionMessage(run),
						display: false,
						details: {
							runId: run.id,
							name: run.name,
							status: run.status,
							session: run.session,
							startedAt: run.startedAt,
							endedAt: run.endedAt,
						},
					}, { triggerTurn: false });
					update.messageSent = true;
				}
				if (!update.uiNotified) {
					ctx.ui.notify(completionMessage(run), completionNotificationType(run));
					update.uiNotified = true;
				}
				if (update.messageSent && update.uiNotified) pendingRunUpdates.delete(id);
			} catch {
				// Keep the partially delivered update for the next safe flush.
			}
		}
	}

	function watchLaunchedRun(id: string, session: string, ctx: ExtensionContext): void {
		if (launchedRunIDs.has(id)) return;
		launchedRunIDs.add(id);
		void (async () => {
			while (!runCompletionAbort.signal.aborted) {
				try {
					const run = (await listRuns(session, runCompletionAbort.signal)).find((candidate) => candidate.id === id);
					if (run && isTerminalRun(run)) {
						pendingRunUpdates.set(id, { run, messageSent: false, uiNotified: false });
						flushTerminalRunUpdates(ctx);
						return;
					}
				} catch {
					if (runCompletionAbort.signal.aborted) return;
					// Polling is best-effort; a transient CLI failure must not lose a completion.
				}
				try {
					await sleep(RUN_COMPLETION_POLL_MS, undefined, { signal: runCompletionAbort.signal, ref: false });
				} catch {
					return;
				}
			}
		})();
	}

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

	pi.on("agent_settled", async (_event, ctx) => {
		flushTerminalRunUpdates(ctx);
	});

	pi.on("session_shutdown", () => {
		runCompletionAbort.abort();
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
		description: "Start a bounded JavaScript Dyna workflow from a temporary file. Every run is detached and promptly returns its run id; completion sends one Pi session update. The extension invokes the exact Dyna binary without a shell and consumes the source file after launch.",
		promptSnippet: "Start a detached Dyna workflow from a temporary JavaScript file",
		promptGuidelines: ["Before dyna_run, use write to create a unique /tmp/dyna-workflow-*.js file, then pass its workflow_path. dyna_run always starts in the background; use its returned run ID with dyna_runs or dyna_steer."],
		parameters: Type.Object({
			workflow_path: Type.String({ description: "Absolute /tmp/dyna-workflow-*.js file containing the complete workflow; dyna_run consumes it", minLength: 1, maxLength: 4096 }),
			cwd: Type.Optional(Type.String({ description: "Working directory for workers", minLength: 1, maxLength: 4096 })),
			args: Type.Optional(Type.Unknown({ description: "JSON value exposed to the workflow as args" })),
			name: Type.Optional(Type.String({ description: "Run display name", minLength: 1, maxLength: 200 })),
			resume: Type.Optional(Type.String({ description: "Session-owned run id whose successful calls may be reused", pattern: "^wf_[A-Za-z0-9_-]+$", maxLength: 128 })),
			max_concurrent: Type.Optional(Type.Integer({ description: "Maximum simultaneous workers for this run", minimum: 1, maximum: 64 })),
			call_cap: Type.Optional(Type.Integer({ description: "Maximum lifetime agent calls for this run", minimum: 1, maximum: 1000 })),
		}),
		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			const session = sessionID(ctx);
			const cwd = checkedString(params.cwd, "cwd", 4096);
			const name = checkedString(params.name, "name", 1024);
			if (params.resume) await requireSessionRun(params.resume, session, signal);

			let argsJSON: string | undefined;
			if (params.args !== undefined) {
				argsJSON = JSON.stringify(params.args);
				checkedString(argsJSON, "args", MAX_WORKFLOW_SOURCE);
			}

			const { tempDir, scriptPath } = await consumeWorkflow(params.workflow_path);
			try {
				const cliArgs = ["run", scriptPath, "--json", "--detach"];
				if (cwd) cliArgs.push("--dir", cwd);
				if (argsJSON !== undefined) cliArgs.push("--args", argsJSON);
				if (name) cliArgs.push("--name", name);
				if (params.resume) cliArgs.push("--resume", params.resume);
				if (params.max_concurrent !== undefined) cliArgs.push("--max-concurrent", String(params.max_concurrent));
				if (params.call_cap !== undefined) cliArgs.push("--max-agents", String(params.call_cap));

				const result = await runDyna(cliArgs, undefined, signal, sessionEnv(session));
				if (!result.ok) throw failedCLI("dyna run", result);
				const runID = result.stdout.trim();
				try {
					checkRunID(runID);
				} catch {
					const safeOutput = clipped(redactSecrets(runID), MAX_ERROR_DETAIL).text;
					throw new Error(`dyna run --detach returned an invalid run ID ${JSON.stringify(safeOutput)}; the detached child may still be running`);
				}
				// Registration is deliberately not awaited: the caller can keep using Pi
				// immediately, while management calls give this just-launched ID grace.
				watchLaunchedRun(runID, session, ctx);
				return toolResult(result, { runId: runID, detached: true, session });
			} finally {
				await rm(tempDir, { recursive: true, force: true });
			}
		},
	});

	pi.registerTool({
		name: "dyna_runs",
		label: "Manage Dyna Runs",
		description: "List, inspect, wait for, or cancel Dyna runs owned by this Pi session. Run-id operations reject runs from every other session.",
		promptSnippet: "Manage Dyna runs from this Pi session",
		promptGuidelines: ["Use dyna_runs instead of shell commands for session-scoped list, show, wait, and cancel operations."],
		parameters: Type.Object({
			action: Type.Union([Type.Literal("list"), Type.Literal("show"), Type.Literal("wait"), Type.Literal("cancel")], { description: "Run operation" }),
			run_id: Type.Optional(Type.String({ description: "Required for show, wait, and cancel", pattern: "^wf_[A-Za-z0-9_-]+$", maxLength: 128 })),
			timeout_seconds: Type.Optional(Type.Integer({ description: "Wait timeout without canceling the run", minimum: 1, maximum: 86400 })),
		}),
		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			const session = sessionID(ctx);
			if (params.action === "list") {
				if (params.run_id || params.timeout_seconds !== undefined) throw new Error("dyna_runs list does not accept run_id or timeout_seconds");
				const result = await runDyna(["runs", "list", "--json", "--session", session], undefined, signal);
				if (!result.ok) throw failedCLI("dyna runs list", result);
				return toolResult(result, { action: params.action, session });
			}
			if (!params.run_id) throw new Error(`run_id is required for dyna_runs ${params.action}`);
			if (params.action !== "wait" && params.timeout_seconds !== undefined) throw new Error(`timeout_seconds is only valid for dyna_runs wait`);
			const run = await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id));
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
		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			const session = sessionID(ctx);
			checkedString(params.message, "message", 2000);
			const run = await requireSessionRun(params.run_id, session, signal, launchedRunIDs.has(params.run_id));
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
		description: "Open the Dyna dashboard for this Pi session",
		handler: async (_args, ctx) => {
			if (ctx.mode !== "tui") {
				ctx.ui.notify("/dyna requires the interactive TUI", "error");
				return;
			}
			const session = sessionID(ctx);

			let launchFailure = "";
			await ctx.ui.custom<void>((tui, _theme, _keys, done) => {
				let finished = false;
				const finish = (failure = "") => {
					if (finished) return;
					finished = true;
					launchFailure = failure;
					try {
						tui.start();
						tui.requestRender(true);
					} finally {
						done(undefined);
					}
				};

				tui.stop();
				try {
					process.stdout.write("\x1b[2J\x1b[H");
					const child = spawn(DYNA, ["tui", "--session", session], {
						stdio: "inherit",
						env: process.env,
					});
					child.once("error", (error) => finish(error.message));
					child.once("close", (code, signal) => {
						if (signal) finish(`dyna tui exited on signal ${signal}`);
						else if (code !== 0) finish(`dyna tui exited with code ${code}`);
						else finish();
					});
				} catch (error) {
					finish(error instanceof Error ? error.message : String(error));
				}
				return { render: () => [], invalidate: () => {} };
			});
			if (launchFailure) ctx.ui.notify(`Unable to run Dyna dashboard: ${launchFailure}`, "error");
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
			const runs = await listRuns(sessionID(ctx));
			const running = runs.filter((run) => run.status === "running").length;
			if (running > 0) ctx.ui.setStatus("dyna", `${running} dyna run(s) — /dyna`);
		} catch {
			// Startup stays quiet; /dyna launches the dashboard on demand.
		}
	});
}
