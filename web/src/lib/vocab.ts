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
    meaning: "One worker holds the task for a bounded time and renews that hold while it works.",
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
    meaning: "Five heartbeats missed. Its leases are released and its tasks come back.",
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
