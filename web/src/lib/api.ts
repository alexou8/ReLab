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

/**
 * The bearer token for a control plane that requires one.
 *
 * It is read on the server and never serialised into a page: every fetch here
 * happens during rendering, so the browser neither sees the token nor talks to
 * the API. A dashboard that shipped a viewer token to the browser would hand it
 * to anyone who opened developer tools.
 *
 * A viewer token is the right one to use. The dashboard reads and never writes.
 */
const API_TOKEN = process.env.RELAB_API_TOKEN ?? "";

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
    /**
     * unreadable marks a reply that arrived and could not be used, as opposed
     * to a control plane that was never reached. The distinction matters to
     * whoever is reading the error: one is "start the server", the other is
     * "this is not the API I think it is".
     */
    readonly unreadable = false,
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
      headers: {
        Accept: "application/json",
        ...(API_TOKEN ? { Authorization: `Bearer ${API_TOKEN}` } : {}),
      },
    });
    if (!response.ok) {
      // 401 is worth its own sentence: it is the one failure whose fix is a
      // configuration change here rather than something wrong with the control
      // plane, and "the API returned 401" sends the reader to the wrong place.
      if (response.status === 401) {
        throw new ApiError(
          API_TOKEN
            ? "the control plane rejected this dashboard's token"
            : "the control plane requires a token and RELAB_API_TOKEN is not set",
          path,
        );
      }
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

/**
 * Shape checks at the one boundary where an unknown body arrives.
 *
 * `get` casts the parsed JSON to the type the caller asked for, which is a
 * promise the response cannot keep: a control plane one version ahead, a proxy
 * answering 200 with an error object, or a truncated body all parse fine and
 * then throw somewhere inside a component, where the failure reads as a broken
 * dashboard rather than as an unusable answer. These functions turn that into
 * the same ApiError every other failure produces, so the page renders its error
 * state and names the endpoint.
 *
 * They check the shape a page actually depends on — a list is a list, a counter
 * is a number — and not every field. A missing optional field renders as a gap
 * in a row, which is honest; a `.map` on a string is a stack trace.
 */
function badBody(path: string): ApiError {
  return new ApiError(
    "the reply parsed as JSON but is not the shape this dashboard reads",
    path,
    false,
    true,
  );
}

function expectArray<T>(value: unknown, path: string): T[] {
  if (value === null || value === undefined) return [];
  if (!Array.isArray(value)) throw badBody(path);
  return value as T[];
}

function expectObject<T>(value: unknown, path: string): T {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw badBody(path);
  }
  return value as T;
}

function expectCounts(value: unknown, path: string): Record<string, number> {
  const map = expectObject<Record<string, unknown>>(value, path);
  for (const count of Object.values(map)) {
    if (typeof count !== "number") throw badBody(path);
  }
  return map as Record<string, number>;
}

export async function fetchStats(): Promise<Stats> {
  if (mode() === "demo") return snapshot().stats;
  const path = "/api/v1/stats";
  const body = expectObject<Record<string, unknown>>(await get(path), path);
  if (
    typeof body.queue_depth !== "number" ||
    typeof body.dead_letters !== "number"
  ) {
    throw badBody(path);
  }
  return {
    runs_by_status: expectCounts(body.runs_by_status ?? {}, path),
    tasks_by_status: expectCounts(body.tasks_by_status ?? {}, path),
    workers_by_status: expectCounts(body.workers_by_status ?? {}, path),
    queue_depth: body.queue_depth,
    dead_letters: body.dead_letters,
  };
}

export async function fetchRuns(limit = 50): Promise<Run[]> {
  if (mode() === "demo") {
    return snapshot().runs.map((r) => r.run).slice(0, limit);
  }
  const path = `/api/v1/runs?limit=${limit}`;
  const body = expectObject<{ runs?: unknown }>(await get(path), path);
  return expectArray<Run>(body.runs, path);
}

export async function fetchRun(id: string): Promise<Run> {
  if (mode() === "demo") return demoRun(id).run;
  const path = `/api/v1/runs/${id}`;
  const run = expectObject<Run>(await get(path), path);
  if (typeof run.ID !== "string") throw badBody(path);
  return run;
}

export async function fetchTasks(id: string): Promise<Task[]> {
  if (mode() === "demo") return demoRun(id).tasks;
  const path = `/api/v1/runs/${id}/tasks`;
  const body = expectObject<{ tasks?: unknown }>(await get(path), path);
  return expectArray<Task>(body.tasks, path);
}

export async function fetchEvents(id: string): Promise<RunEvent[]> {
  if (mode() === "demo") return demoRun(id).events;
  const path = `/api/v1/runs/${id}/events`;
  const body = expectObject<{ events?: unknown }>(await get(path), path);
  return expectArray<RunEvent>(body.events, path);
}

export async function fetchWorkers(): Promise<Worker[]> {
  if (mode() === "demo") return snapshot().workers;
  const path = "/api/v1/workers";
  const body = expectObject<{ workers?: unknown }>(await get(path), path);
  return expectArray<Worker>(body.workers, path);
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
