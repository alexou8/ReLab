import Link from "next/link";
import { notFound } from "next/navigation";
import { ApiError, fetchEvents, fetchRun, fetchTasks } from "@/lib/api";
import { evidenceOf, FILTERS, groupsOf, isGroup, storyOf } from "@/lib/events";
import { duration, preciseTime, short, summarisePayload } from "@/lib/format";
import { phraseOf } from "@/lib/vocab";
import { ErrorState } from "../../error-state";
import { Status } from "../../status";
import { Story } from "../../story";

export const dynamic = "force-dynamic";

// Highlighted in the timeline because finding them is the reason someone opens
// this page.
const TROUBLE = new Set([
  "TASK_FAILED",
  "TASK_LEASE_EXPIRED",
  "TASK_REQUEUED",
  "TASK_DEAD_LETTERED",
  "WORKER_LOST",
  "WORKER_SUSPECT",
  "FAULT_INJECTED",
  "RUN_FAILED",
]);

export default async function RunDetail({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ events?: string }>;
}) {
  const { id } = await params;
  const { events: filterParam } = await searchParams;

  let data;
  try {
    const [run, tasks, events] = await Promise.all([
      fetchRun(id),
      fetchTasks(id),
      fetchEvents(id),
    ]);
    data = { run, tasks, events };
  } catch (error) {
    if (error instanceof ApiError && error.notFound) notFound();
    return <ErrorState error={error} />;
  }

  const { run, tasks, events } = data;
  const evidence = evidenceOf(events);
  const story = storyOf(events);

  // The filter is a link, not a control: it survives a reload, it can be shared
  // with whoever is being asked to look at the run, and it costs the page no
  // JavaScript.
  const filter = isGroup(filterParam) ? filterParam : "all";
  const shown =
    filter === "all"
      ? events
      : events.filter((e) => groupsOf(e.Type).includes(filter));

  return (
    <>
      <Link className="back" href="/runs">
        ← all runs
      </Link>

      <h2>Run</h2>
      <div className="facts">
        <Fact label="Status">
          <Status value={run.Status} />
        </Fact>
        <Fact label="Workflow">
          {run.WorkflowName} v{run.WorkflowVer}
        </Fact>
        <Fact label="Run id">{run.ID}</Fact>
        <Fact label="Seed">{run.Seed}</Fact>
        {run.ScenarioName ? (
          <Fact label="Scenario">{run.ScenarioName}</Fact>
        ) : null}
        <Fact label="Duration">
          {duration(run.CreatedAt, run.CompletedAt) ?? "still running"}
        </Fact>
      </div>
      {run.FailureReason ? <p className="detail">{run.FailureReason}</p> : null}

      <Verdict
        status={run.Status}
        evidence={evidence}
        eventCount={events.length}
      />

      {story.length > 0 ? (
        <>
          <h2>What happened</h2>
          <p className="detail note">
            The milestones of this run, in the order they were recorded. Each
            one is a real event; the full sequence is in the timeline below.
          </p>
          <Story steps={story} />
        </>
      ) : null}

      <h2>What the journal proves</h2>
      <div className="counts">
        <Count label="Attempts" value={evidence.attempts} />
        <Count label="Workers involved" value={evidence.workers.length} />
        <Count label="Faults injected" value={evidence.faultsInjected} />
        <Count label="Leases expired" value={evidence.leaseExpiries} />
        <Count label="Tasks requeued" value={evidence.requeues} />
        <Count label="Retries scheduled" value={evidence.retriesScheduled} />
        <Count label="Workers lost" value={evidence.workersLost} />
        <Count label="Effects suppressed" value={evidence.effectsSuppressed} />
        <Count label="Dead-lettered" value={evidence.deadLettered} />
        <Count
          label="Recovery"
          value={
            evidence.recoveryMs === null
              ? "·"
              : `${(evidence.recoveryMs / 1000).toFixed(2)}s`
          }
        />
      </div>
      <p className="detail note">
        Every number above is a count of events in this run&rsquo;s journal.
        Recovery is measured from the first fault, lease expiry, task failure or
        lost worker to the run completing, the same interval{" "}
        <code>relab test</code> asserts on.
      </p>

      <h2>Tasks</h2>
      <div className="table-scroll">
        <table>
          <caption className="sr-only">
            Each task in the run, with the attempt it reached and the worker
            that last held it.
          </caption>
          <thead>
            <tr>
              <th scope="col">Status</th>
              <th scope="col">Task</th>
              <th scope="col" className="mono">
                Attempt
              </th>
              <th scope="col">Worker</th>
              <th scope="col">Error</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <tr key={task.ID}>
                <td>
                  <Status value={task.Status} />
                </td>
                <th scope="row" className="row-name">
                  {task.Name}
                </th>
                <td className="mono">
                  {task.Attempt}/{task.MaxAttempts}
                </td>
                <td className="mono">
                  {task.WorkerID ? short(task.WorkerID) : "·"}
                </td>
                <td className="detail wrap">{task.Error || "·"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h2 id="timeline">Timeline</h2>
      <p className="detail note">
        The run&rsquo;s complete recorded history, in sequence order. This is the
        same journal <code>relab replay</code> reduces. Filtering hides rows from
        this view; it never changes the sequence numbers, so a gap in them is
        still a gap.
      </p>
      <nav className="filters" aria-label="Filter timeline by event kind">
        {FILTERS.map(({ key, label }) => {
          const count =
            key === "all"
              ? events.length
              : events.filter((e) => groupsOf(e.Type).includes(key)).length;
          const active = key === filter;
          return (
            <Link
              key={key}
              href={key === "all" ? `?#timeline` : `?events=${key}#timeline`}
              aria-current={active ? "true" : undefined}
              className={active ? "filter filter-on" : "filter"}
            >
              {label} <span className="filter-count">{count}</span>
            </Link>
          );
        })}
      </nav>

      {shown.length === 0 ? (
        <p className="empty">
          No {filter} events in this run. That is a fact about the run, not a
          missing page: nothing of that kind was recorded.
        </p>
      ) : (
        <div className="table-scroll">
          <table className="timeline">
            <caption className="sr-only">
              {shown.length} of {events.length} recorded events, oldest first.
            </caption>
            <thead>
              <tr>
                <th scope="col" className="mono">
                  Seq
                </th>
                <th scope="col" className="mono">
                  Time
                </th>
                <th scope="col">What happened</th>
                <th scope="col">Task</th>
                <th scope="col">Worker</th>
                <th scope="col">Technical detail</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((event) => {
                const phrase = phraseOf(event.Type);
                const trouble = TROUBLE.has(event.Type);
                return (
                  <tr key={event.Seq} className={trouble ? "row-trouble" : undefined}>
                    <td className="mono">{event.Seq}</td>
                    <td className="mono">{preciseTime(event.OccurredAt)}</td>
                    <td>
                      {/* Plain language first, the event type under it. The
                          type is the evidence and is never replaced: a reader
                          checking this page against `relab replay` has to be
                          able to find the same row by name. */}
                      <span className={trouble ? "event-label event-trouble" : "event-label"}>
                        {phrase?.label ?? event.Type}
                      </span>
                      <code className="event-type">{event.Type}</code>
                    </td>
                    <td>{event.TaskName || "·"}</td>
                    <td className="mono">
                      {event.WorkerID ? short(event.WorkerID) : "·"}
                    </td>
                    <td className="detail wrap">
                      <details className="raw">
                        <summary>
                          {summarisePayload(event.Payload) || "no payload fields"}
                        </summary>
                        {phrase ? (
                          <p className="raw-meaning">{phrase.meaning}</p>
                        ) : null}
                        <dl className="raw-fields">
                          <dt>Event</dt>
                          <dd>{event.Type}</dd>
                          <dt>Sequence</dt>
                          <dd>{event.Seq}</dd>
                          <dt>Task</dt>
                          <dd>{event.TaskName || "·"}</dd>
                          <dt>Worker</dt>
                          <dd>{event.WorkerID ?? "·"}</dd>
                          <dt>Occurred at</dt>
                          <dd>{event.OccurredAt}</dd>
                        </dl>
                        <pre className="raw-payload">
                          {JSON.stringify(event.Payload, null, 2)}
                        </pre>
                      </details>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

/**
 * Verdict is the sentence a reader most wants before reading anything else: did
 * this run recover, and did recovering it cost a duplicate effect?
 *
 * It says only what the events support. A run with no trouble in its journal
 * gets no verdict at all rather than a reassuring one.
 */
function Verdict({
  status,
  evidence,
  eventCount,
}: {
  status: string;
  evidence: ReturnType<typeof evidenceOf>;
  eventCount: number;
}) {
  const disturbed =
    evidence.faultsInjected + evidence.leaseExpiries + evidence.workersLost > 0;
  if (!disturbed) return null;

  const recovered = status === "SUCCEEDED";
  return (
    <div className={recovered ? "notice notice-ok" : "notice"} role="note">
      <h3>
        {recovered
          ? "This run was disrupted and finished anyway"
          : "This run was disrupted and did not finish"}
      </h3>
      <p>
        {evidence.workersLost > 0
          ? `${plural(evidence.workersLost, "worker")} holding work for this run went away. `
          : null}
        {evidence.leaseExpiries > 0
          ? `${plural(evidence.leaseExpiries, "lease")} expired and the work came back through the reaper. `
          : null}
        {evidence.effectsSuppressed > 0
          ? `${plural(evidence.effectsSuppressed, "side effect")} already performed ${evidence.effectsSuppressed === 1 ? "was" : "were"} suppressed by the idempotency ledger, so the retry did not repeat work that had already happened. `
          : null}
        {evidence.deadLettered > 0
          ? `${plural(evidence.deadLettered, "task")} exhausted its attempts and was dead-lettered. `
          : null}
        All of it is in the {eventCount} events below.
      </p>
    </div>
  );
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

function Count({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="count">
      <div className="value">{value}</div>
      <div className="label">{label}</div>
    </div>
  );
}

function Fact({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="fact">
      <div className="label">{label}</div>
      <div className="value">{children}</div>
    </div>
  );
}
