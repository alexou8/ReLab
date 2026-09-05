/**
 * Plain-language names for the journal's vocabulary.
 *
 * The event type is the evidence and is never replaced: every place this
 * translation is shown, the type is shown next to it. The translation exists
 * because a reader who has not read DATA.md cannot tell from
 * `TASK_LEASE_EXPIRED` whether something good or something bad just happened,
 * and the answer to that question is the whole point of the page.
 *
 * A type this build does not know about renders as itself. Guessing a friendly
 * name from the shape of an identifier would be exactly the permissive reading
 * the engine refuses to do.
 */

export interface Phrase {
  /** What happened, for a reader who has never used ReLab. */
  label: string;
  /** Why it matters, one sentence. Shown when an event is expanded. */
  meaning: string;
}

const PHRASES: Record<string, Phrase> = {
  RUN_CREATED: {
    label: "Run created",
    meaning: "The workflow was accepted and its tasks were written down.",
  },
  RUN_QUEUED: {
    label: "Run queued",
    meaning: "The run's first tasks became available for a worker to claim.",
  },
  RUN_STARTED: {
    label: "Run started",
    meaning: "A worker picked up the first task in the run.",
  },
  RUN_SUCCEEDED: {
    label: "Workflow completed",
    meaning: "Every task finished. This event is the run's last: a finished run's story cannot change.",
  },
  RUN_FAILED: {
    label: "Workflow failed",
    meaning: "A task ran out of attempts and the run could not continue.",
  },
  RUN_CANCELLED: {
    label: "Workflow cancelled",
    meaning: "The run was stopped on purpose before it finished.",
  },

  TASK_SCHEDULED: {
    label: "Task ready to run",
    meaning: "The task's dependencies are satisfied, so it entered the queue.",
  },
  TASK_LEASED: {
    label: "Task claimed by a worker",
    meaning:
      "One worker holds the task for a bounded time and renews that hold while it works. The hold is on this attempt: if it expires while that worker is still running, a second worker may take the task under a new attempt number.",
  },
  TASK_STARTED: {
    label: "Task running",
    meaning: "The handler began executing. Each attempt writes one of these.",
  },
  TASK_SUCCEEDED: {
    label: "Task finished",
    meaning: "The handler returned without an error and the result was recorded.",
  },
  TASK_FAILED: {
    label: "Task failed",
    meaning: "The attempt returned an error. Whether it is retried depends on attempts remaining.",
  },
  TASK_RETRY_SCHEDULED: {
    label: "Retry scheduled",
    meaning: "The task will be offered again after a backoff delay.",
  },
  TASK_LEASE_EXPIRED: {
    label: "Worker stopped responding",
    meaning:
      "Nobody renewed the hold on this task, so another process concluded the holder is gone. This is the mechanism that also works when a machine loses power.",
  },
  TASK_REQUEUED: {
    label: "Task returned to the queue",
    meaning: "The work a vanished worker was holding became claimable again.",
  },
  TASK_DEAD_LETTERED: {
    label: "Task gave up",
    meaning: "The task exhausted its attempts and will not be tried again.",
  },

  WORKER_REGISTERED: {
    label: "Worker joined",
    meaning: "A worker process announced itself and began heartbeating.",
  },
  WORKER_HEARTBEAT: {
    label: "Worker heartbeat",
    meaning: "The worker is alive. One missed beat never counts as failure.",
  },
  WORKER_SUSPECT: {
    label: "Worker doubted",
    meaning:
      "Three heartbeats missed. Its work is not reclaimed yet: a worker that stopped answering has not necessarily stopped working.",
  },
  WORKER_LOST: {
    label: "Worker declared gone",
    meaning:
      "The holder of this run's task is gone and its leases are released. Usually that is five missed heartbeats; a worker that shuts down deliberately while holding work writes the same event, because what the run experienced is the same either way.",
  },

  FAULT_INJECTED: {
    label: "Failure injected on purpose",
    meaning: "ReLab degraded the real system here. This is the break, and everything after it is the recovery.",
  },
  SIDE_EFFECT_SKIPPED: {
    label: "Duplicate effect prevented",
    meaning:
      "The retry asked to perform an effect already recorded under the same key, so it was not performed a second time.",
  },
};

/**
 * The event vocabulary, grouped the way a reader meets it: what a run does,
 * what happens to a task, what a worker does, and what ReLab does to it on
 * purpose. The glossary renders these in order.
 *
 * The grouping lives here rather than in the page because it is a statement
 * about the journal, not about a layout: a new event type has to be given a
 * phrase and a group in the same edit, or it will not appear at all.
 */
export const PHRASE_GROUPS: { heading: string; blurb: string; types: string[] }[] = [
  {
    heading: "A run",
    blurb: "One execution of a workflow, from acceptance to a terminal event.",
    types: [
      "RUN_CREATED",
      "RUN_QUEUED",
      "RUN_STARTED",
      "RUN_SUCCEEDED",
      "RUN_FAILED",
      "RUN_CANCELLED",
    ],
  },
  {
    heading: "A task",
    blurb: "One step of the workflow. Most of a recovery story is told here.",
    types: [
      "TASK_SCHEDULED",
      "TASK_LEASED",
      "TASK_STARTED",
      "TASK_SUCCEEDED",
      "TASK_FAILED",
      "TASK_RETRY_SCHEDULED",
      "TASK_LEASE_EXPIRED",
      "TASK_REQUEUED",
      "TASK_DEAD_LETTERED",
    ],
  },
  {
    heading: "A worker",
    blurb:
      "A process that claims tasks and heartbeats while it holds them. Only WORKER_LOST reaches a run's journal today: a worker's own comings and goings are state in the workers table, and the journal describes what happened to the run.",
    types: [
      "WORKER_REGISTERED",
      "WORKER_HEARTBEAT",
      "WORKER_SUSPECT",
      "WORKER_LOST",
    ],
  },
  {
    heading: "The break, and what stopped it costing twice",
    blurb: "The two events that are the reason this project exists.",
    types: ["FAULT_INJECTED", "SIDE_EFFECT_SKIPPED"],
  },
];

/**
 * Types that have a phrase but no group. It should always be empty; it is
 * computed rather than asserted so that a new event type shows up in the
 * glossary under "Not yet grouped" instead of vanishing from it. A page that
 * quietly omits an event would be the permissive reading this project refuses.
 */
export const UNGROUPED_TYPES: string[] = Object.keys(PHRASES).filter(
  (type) => !PHRASE_GROUPS.some((group) => group.types.includes(type)),
);

/**
 * Event types the engine defines and the reducer accepts, but which nothing
 * currently appends to a run journal. Worker registration, heartbeats and the
 * SUSPECT transition are recorded as state in the workers table instead
 * (`internal/event/payload.go`, `internal/engine/workers.go`).
 *
 * The set is written down rather than inferred, because a reader who saw these
 * listed as ordinary journal events would go looking for them in a run and
 * conclude something had been lost.
 */
export const UNJOURNALLED_TYPES = new Set([
  "WORKER_REGISTERED",
  "WORKER_HEARTBEAT",
  "WORKER_SUSPECT",
]);

/** phraseOf returns the plain-language reading of an event type, if known. */
export function phraseOf(type: string): Phrase | null {
  return PHRASES[type] ?? null;
}

/** labelOf is the phrase's label, falling back to the raw type. */
export function labelOf(type: string): string {
  return PHRASES[type]?.label ?? type;
}

/** What a run or task or worker status means, in one clause. */
export const STATUS_MEANING: Record<string, string> = {
  SUCCEEDED: "finished, with every task done",
  FAILED: "stopped because a task ran out of attempts",
  CANCELLED: "stopped on purpose",
  RUNNING: "in progress",
  QUEUED: "waiting for a worker",
  CREATED: "accepted, not yet queued",
  PENDING: "waiting on a dependency",
  READY: "claimable now",
  LEASED: "held by a worker",
  RETRYING: "failed, waiting for its next attempt",
  DEAD: "out of attempts",
  HEALTHY: "heartbeating",
  SUSPECT: "missed beats, work not yet reclaimed",
  LOST: "gone, leases released",
  STOPPED: "shut down and said so",
};
