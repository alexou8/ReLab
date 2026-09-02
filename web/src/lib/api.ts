/**
 * Typed access to the ReLab API.
 *
 * Every fetch happens on the server during rendering, so the browser never
 * talks to the API directly and there is no credential in the page. The
 * dashboard is a debugging surface, not the product: it reads, and it does not
 * write.
 */

const API_BASE = process.env.RELAB_API_URL ?? "http://localhost:8080";

/** How long a page will wait for the API before rendering an error state. */
const REQUEST_TIMEOUT_MS = 5_000;

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
  Status: "HEALTHY" | "SUSPECT" | "LOST";
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
      throw new ApiError(`the API returned ${response.status}`, path);
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
  return get<Stats>("/api/v1/stats");
}

export async function fetchRuns(limit = 50): Promise<Run[]> {
  const body = await get<{ runs: Run[] | null }>(`/api/v1/runs?limit=${limit}`);
  return body.runs ?? [];
}

export async function fetchRun(id: string): Promise<Run> {
  return get<Run>(`/api/v1/runs/${id}`);
}

export async function fetchTasks(id: string): Promise<Task[]> {
  const body = await get<{ tasks: Task[] | null }>(`/api/v1/runs/${id}/tasks`);
  return body.tasks ?? [];
}

export async function fetchEvents(id: string): Promise<RunEvent[]> {
  const body = await get<{ events: RunEvent[] | null }>(
    `/api/v1/runs/${id}/events`,
  );
  return body.events ?? [];
}

export async function fetchWorkers(): Promise<Worker[]> {
  const body = await get<{ workers: Worker[] | null }>("/api/v1/workers");
  return body.workers ?? [];
}
