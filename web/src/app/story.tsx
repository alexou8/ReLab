import type { RunEvent } from "@/lib/api";
import { preciseTime } from "@/lib/format";
import { phraseOf } from "@/lib/vocab";

const BREAK = new Set(["FAULT_INJECTED", "TASK_FAILED", "WORKER_LOST"]);
const RECOVERY = new Set([
  "TASK_LEASE_EXPIRED",
  "TASK_REQUEUED",
  "TASK_RETRY_SCHEDULED",
  "TASK_STARTED",
  "SIDE_EFFECT_SKIPPED",
]);
const END_OK = new Set(["RUN_SUCCEEDED", "TASK_SUCCEEDED"]);

/**
 * The arc of one run: what broke, what the system did about it, how it ended.
 *
 * Each step is a real event, and carries its own sequence number and timestamp
 * so it can be found in the full timeline. The plain-language line is on top
 * because that is the question a first-time reader has; the event type is
 * underneath it because that is the answer an engineer will want to check.
 */
export function Story({ steps }: { steps: RunEvent[] }) {
  if (steps.length === 0) return null;
  return (
    <ol className="story">
      {steps.map((event) => {
        const phrase = phraseOf(event.Type);
        const tone = BREAK.has(event.Type)
          ? "step-break"
          : RECOVERY.has(event.Type)
            ? "step-recovery"
            : END_OK.has(event.Type)
              ? "step-ok"
              : "step-plain";
        return (
          <li key={event.Seq} className={`step ${tone}`}>
            <div className="step-head">
              <span className="step-label">
                {phrase?.label ?? event.Type}
                {event.TaskName ? (
                  <span className="detail"> {event.TaskName}</span>
                ) : null}
              </span>
              <span className="step-seq mono">
                #{event.Seq} {preciseTime(event.OccurredAt)}
              </span>
            </div>
            <code className="step-type">{event.Type}</code>
            {phrase ? <p className="step-meaning">{phrase.meaning}</p> : null}
          </li>
        );
      })}
    </ol>
  );
}
