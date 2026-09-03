/**
 * Classification of journal events, and the evidence derived from them.
 *
 * Everything here is a count of events that are actually in the journal. No
 * number on the page is estimated, sampled or interpolated: if the dashboard
 * says a side effect was suppressed twice, there are two SIDE_EFFECT_SKIPPED
 * events behind it, and `relab replay` will find the same two.
 */

import type { RunEvent } from "./api";

/** The groups the timeline filter offers. An event can be in several. */
export type Group =
  | "task"
  | "worker"
  | "recovery"
  | "fault"
  | "effect"
  | "error"
  | "run";

/**
 * groupsOf classifies one event.
 *
 * A type this build does not know about gets no group rather than a guessed
 * one. It still appears in the unfiltered timeline — the raw sequence is the
 * evidence and is never hidden — but it will not be counted as, say, a recovery
 * on the strength of its name looking like one.
 */
export function groupsOf(type: string): Group[] {
  switch (type) {
    case "RUN_CREATED":
    case "RUN_QUEUED":
    case "RUN_STARTED":
    case "RUN_SUCCEEDED":
      return ["run"];
    case "RUN_FAILED":
    case "RUN_CANCELLED":
      return ["run", "error"];

    case "TASK_SCHEDULED":
    case "TASK_LEASED":
    case "TASK_STARTED":
    case "TASK_SUCCEEDED":
      return ["task"];
    case "TASK_FAILED":
      return ["task", "error"];
    case "TASK_DEAD_LETTERED":
      return ["task", "error", "recovery"];
    // A lease expiring is how a dead worker's task comes back; the requeue and
    // the scheduled retry are the recovery itself.
    case "TASK_LEASE_EXPIRED":
      return ["task", "recovery", "worker"];
    case "TASK_REQUEUED":
    case "TASK_RETRY_SCHEDULED":
      return ["task", "recovery"];

    case "WORKER_REGISTERED":
    case "WORKER_HEARTBEAT":
      return ["worker"];
    case "WORKER_SUSPECT":
      return ["worker", "recovery"];
    case "WORKER_LOST":
      return ["worker", "recovery", "error"];

    case "FAULT_INJECTED":
      return ["fault"];
    case "SIDE_EFFECT_SKIPPED":
      return ["effect", "recovery"];

    default:
      return [];
  }
}

/** The filters offered above the timeline, in the order they are shown. */
export const FILTERS: { key: Group | "all"; label: string }[] = [
  { key: "all", label: "All" },
  { key: "task", label: "Task" },
  { key: "worker", label: "Worker" },
  { key: "recovery", label: "Recovery" },
  { key: "fault", label: "Faults" },
  { key: "effect", label: "Side effects" },
  { key: "error", label: "Errors" },
];

export function isGroup(value: string | undefined): value is Group {
  return FILTERS.some((f) => f.key === value && f.key !== "all");
}

/** Evidence is what the run's journal proves about its recovery. */
export interface Evidence {
  attempts: number;
  leaseExpiries: number;
  requeues: number;
  retriesScheduled: number;
  workersLost: number;
  faultsInjected: number;
  effectsSuppressed: number;
  deadLettered: number;
  workers: string[];
  /** null when nothing went wrong, or when the run has not finished. */
  recoveryMs: number | null;
}

const TROUBLE = new Set([
  "TASK_LEASE_EXPIRED",
  "FAULT_INJECTED",
  "TASK_FAILED",
  "WORKER_LOST",
]);
const TERMINAL = new Set(["RUN_SUCCEEDED", "RUN_FAILED", "RUN_CANCELLED"]);

export function evidenceOf(events: RunEvent[]): Evidence {
  const count = (type: string) => events.filter((e) => e.Type === type).length;

  const workers = new Set<string>();
  for (const e of events) {
    if (e.WorkerID) workers.add(e.WorkerID);
  }

  return {
    // Every execution of a handler writes TASK_STARTED, so this counts
    // attempts rather than tasks: a task retried once contributes two.
    attempts: count("TASK_STARTED"),
    leaseExpiries: count("TASK_LEASE_EXPIRED"),
    requeues: count("TASK_REQUEUED"),
    retriesScheduled: count("TASK_RETRY_SCHEDULED"),
    workersLost: count("WORKER_LOST"),
    faultsInjected: count("FAULT_INJECTED"),
    effectsSuppressed: count("SIDE_EFFECT_SKIPPED"),
    deadLettered: count("TASK_DEAD_LETTERED"),
    workers: [...workers].sort(),
    recoveryMs: recoveryMs(events),
  };
}

/**
 * recoveryMs measures from the first sign that something went wrong to the run
 * completing.
 *
 * The definition is `internal/assert.RecoveryTime`'s, and that is the
 * authority: this is the same measurement the scenario assertions are checked
 * against, so a number here and a number in `relab test` output describe the
 * same interval. "Something went wrong" is the first lease expiry, injected
 * fault, task failure or lost worker — not the first retry, which is already
 * the recovery.
 */
function recoveryMs(events: RunEvent[]): number | null {
  let trouble: number | null = null;
  let completed: number | null = null;
  for (const e of events) {
    const at = new Date(e.OccurredAt).getTime();
    if (!Number.isFinite(at)) continue;
    if (TROUBLE.has(e.Type) && trouble === null) trouble = at;
    if (TERMINAL.has(e.Type)) completed = at;
  }
  if (trouble === null || completed === null || completed < trouble) return null;
  return completed - trouble;
}

/**
 * The milestones of a run's failure and recovery, in sequence order.
 *
 * This is a selection from the journal, never a summary of it: every entry it
 * returns is an event that is actually in the run, at its real sequence number
 * and its real timestamp. Its job is to let someone read the arc of a run
 * without reading all of it, and the full sequence is always one link away.
 *
 * Returns an empty list for a run that was never disrupted. A run that had no
 * failure has no failure story, and inventing a shape for one would be the
 * marketing graphic this page is trying not to be.
 */
export function storyOf(events: RunEvent[]): RunEvent[] {
  const breakAt = events.findIndex((e) => TROUBLE.has(e.Type));
  if (breakAt === -1) return [];

  const picked = new Map<number, RunEvent>();
  const take = (e: RunEvent | undefined) => {
    if (e) picked.set(e.Seq, e);
  };

  const first = (type: string, from = 0) =>
    events.slice(from).find((e) => e.Type === type);

  take(first("RUN_STARTED"));
  // Everything from here is the break and what followed it. Taking the first
  // occurrence at or after the break keeps the chain to one worked example
  // even in a run where three tasks each failed.
  for (const type of [
    "FAULT_INJECTED",
    "WORKER_LOST",
    "TASK_LEASE_EXPIRED",
    "TASK_REQUEUED",
    "TASK_RETRY_SCHEDULED",
    "TASK_STARTED",
    "SIDE_EFFECT_SKIPPED",
    "TASK_SUCCEEDED",
    "TASK_DEAD_LETTERED",
  ]) {
    take(first(type, breakAt));
  }
  for (const type of TERMINAL) take(first(type));

  return [...picked.values()].sort((a, b) => a.Seq - b.Seq);
}
