import { execFile } from "node:child_process";
import { open } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExtensionAPI, Theme } from "@earendil-works/pi-coding-agent";
import { matchesKey, truncateToWidth, type Component, type TUI } from "@earendil-works/pi-tui";

const SESSION = process.env.DYNA_SESSION ?? "";
const DYNA = process.env.DYNA_BIN || "dyna";
const MAX_OUTPUT = 16 * 1024 * 1024;
const MAX_EVENT_READ = 4 * 1024 * 1024;
const MAX_JOURNAL_TAIL = 256 * 1024;
const LIST_POLL_TICKS = 5;

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
		try {
			const runs = await listRuns();
			const running = runs.filter((run) => run.status === "running").length;
			if (running > 0) ctx.ui.setStatus("dyna", `${running} dyna run(s) — /dyna`);
		} catch {
			// The on-demand overlay reports CLI errors; startup stays quiet.
		}
	});
}
