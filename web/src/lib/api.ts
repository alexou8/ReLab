/**
 * Typed access to ReLab's read API, and to the recording that stands in for it.
 *
 * Every fetch happens on the server during rendering, so the browser never
 * talks to the API directly and there is no credential in the page. The
 * dashboard is a debugging surface, not the product: it reads, and it does not
 * write.
 *
 * The dashboard runs in one of two modes, decided once by whether
 * RELAB_API_URL is set:
 *
 *   live   a ReLab control plane is configured and every page reads from it
 *   demo   no control plane is reachable, and the pages serve a recording of
 *          real runs made by scripts/record-demo.sh
 *
 * The two never mix. Falling back to the recording when a configured API is
 * down would show a reader last week's runs during precisely the incident they
 * opened the dashboard to look at, so a configured API that cannot be reached
 * is an error state and says so.
 */

import { snapshot } from "./demo";

const API_BASE = process.env.RELAB_API_URL?.replace(/\/+$/, "") ?? "";

/** How long a page will wait for the API before rendering an error state. */
const REQUEST_TIMEOUT_MS = 5_000;

export type Mode = "live" | "demo";

/** mode reports where this deployment's data comes from. */
export function mode(): Mode {
  return API_BASE === "" ? "demo" : "live";
}

/** apiBase is the configured control plane, for an error message to name. */
export function apiBase(): string {
  return API_BASE;
}

export type RunStatus =
  | "CREATED"
  | "QUEUED"
  | "RUNNING"
  | "SUCCEEDED"
  | "FAILED"
  | "CANCELLED";

export type TaskStatus =
  | "PENDING"
  | "READY"
  | "LEASED"
  | "RUNNING"
  | "SUCCEEDED"
  | "FAILED"
  | "RETRYING"
  | "DEAD";

export interface Run {
  ID: string;
  WorkflowName: string;
  WorkflowVer: number;
  Status: RunStatus;
  ScenarioName: string;
  Seed: number;
  FailureReason: string;
  CreatedAt: string;
  StartedAt: string | null;
  CompletedAt: string | null;
}

export interface Task {
  ID: string;
  Name: string;
  Status: TaskStatus;
  Attempt: number;
  MaxAttempts: number;
  WorkerID: string | null;
  Error: string;
  StartedAt: string | null;
  CompletedAt: string | null;
}

export interface RunEvent {
  Seq: number;
  Type: string;
  TaskName: string;
  WorkerID: string | null;
  Payload: Record<string, unknown>;
  OccurredAt: string;
}

export interface Worker {
  ID: string;
  Hostname: string;
  Version: string;
  Status: "HEALTHY" | "SUSPECT" | "LOST" | "STOPPED";
  Capacity: number;
  ActiveTasks: number;
  LastHeartbeat: string;
}

export interface Stats {
  runs_by_status: Record<string, number>;
  tasks_by_status: Record<string, number>;
  workers_by_status: Record<string, number>;
  queue_depth: number;
  dead_letters: number;
}

/** ApiError carries enough to render a useful empty state. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly endpoint: string,
    /** notFound distinguishes "no such run" from "the API is broken". */
    readonly notFound = false,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function get<T>(path: string): Promise<T> {
  const url = `${API_BASE}${path}`;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const response = await fetch(url, {
      signal: controller.signal,
      // The dashboard shows live state, so a cached page would be actively
      // misleading during the thing it exists to observe: a recovery.
      cache: "no-store",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      throw new ApiError(
        `the API returned ${response.status}`,
        path,
        response.status === 404,
      );
    }
    return (await response.json()) as T;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    const reason =
      error instanceof Error && error.name === "AbortError"
        ? `no response within ${REQUEST_TIMEOUT_MS}ms`
        : `could not reach ${API_BASE}`;
    throw new ApiError(reason, path);
  } finally {
    clearTimeout(timer);
  }
}

export async function fetchStats(): Promise<Stats> {
  if (mode() === "demo") return snapshot().stats;
  return get<Stats>("/api/v1/stats");
}

export async function fetchRuns(limit = 50): Promise<Run[]> {
  if (mode() === "demo") {
    return snapshot().runs.map((r) => r.run).slice(0, limit);
  }
  const body = await get<{ runs: Run[] | null }>(`/api/v1/runs?limit=${limit}`);
  return body.runs ?? [];
}

export async function fetchRun(id: string): Promise<Run> {
  if (mode() === "demo") return demoRun(id).run;
  return get<Run>(`/api/v1/runs/${id}`);
}

export async function fetchTasks(id: string): Promise<Task[]> {
  if (mode() === "demo") return demoRun(id).tasks;
  const body = await get<{ tasks: Task[] | null }>(`/api/v1/runs/${id}/tasks`);
  return body.tasks ?? [];
}

export async function fetchEvents(id: string): Promise<RunEvent[]> {
  if (mode() === "demo") return demoRun(id).events;
  const body = await get<{ events: RunEvent[] | null }>(
    `/api/v1/runs/${id}/events`,
  );
  return body.events ?? [];
}

export async function fetchWorkers(): Promise<Worker[]> {
  if (mode() === "demo") return snapshot().workers;
  const body = await get<{ workers: Worker[] | null }>("/api/v1/workers");
  return body.workers ?? [];
}

/**
 * demoRun looks a run up in the recording, raising the same not-found the live
 * API raises so the pages need no second code path for a bad run id.
 */
function demoRun(id: string) {
  const found = snapshot().runs.find((r) => r.run.ID === id);
  if (!found) {
    throw new ApiError("no run with that id in the recording", `/runs/${id}`, true);
  }
  return found;
}
