import Link from "next/link";
import { notFound } from "next/navigation";
import { ApiError, fetchEvents, fetchRun, fetchTasks } from "@/lib/api";
import { duration, preciseTime, short, summarisePayload } from "@/lib/format";
import { ErrorState } from "../../error-state";
import { Status } from "../../status";

export const dynamic = "force-dynamic";

// The event types that mark something going wrong. They are highlighted in the
// timeline because finding them is the reason someone opens this page.
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
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  let data;
  try {
    const [run, tasks, events] = await Promise.all([
      fetchRun(id),
      fetchTasks(id),
      fetchEvents(id),
    ]);
    data = { run, tasks, events };
  } catch (error) {
    if (error instanceof ApiError && error.message.includes("404")) {
      notFound();
    }
    return <ErrorState error={error} />;
  }

  const { run, tasks, events } = data;
  const recovered = events.some((e) => e.Type === "TASK_REQUEUED");
  const suppressed = events.filter(
    (e) => e.Type === "SIDE_EFFECT_SKIPPED",
  ).length;

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
      {run.FailureReason ? (
        <p className="detail">{run.FailureReason}</p>
      ) : null}

      {/* The one sentence a reader most wants: did this run recover, and did
          recovering it cost a duplicate effect? */}
      {recovered ? (
        <div className="notice" style={{ marginTop: 16 }}>
          <h3>This run recovered</h3>
          <p>
            A task was handed back after its lease expired and was executed
            again.{" "}
            {suppressed > 0
              ? `${suppressed} side effect${suppressed === 1 ? "" : "s"} were suppressed by the idempotency ledger, so the retry did not repeat work that had already happened.`
              : "No side effect needed suppressing."}
          </p>
        </div>
      ) : null}

      <h2>Tasks</h2>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Status</th>
              <th>Task</th>
              <th className="mono">Attempt</th>
              <th>Worker</th>
              <th>Error</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <tr key={task.ID}>
                <td>
                  <Status value={task.Status} />
                </td>
                <td>{task.Name}</td>
                <td className="mono">
                  {task.Attempt}/{task.MaxAttempts}
                </td>
                <td className="mono">
                  {task.WorkerID ? short(task.WorkerID) : "—"}
                </td>
                <td className="detail">{task.Error || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h2>Timeline</h2>
      <p className="detail" style={{ marginBottom: 12 }}>
        The run&rsquo;s complete recorded history, in sequence order. This is the
        same data <code>relab replay</code> reduces.
      </p>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th className="mono">Seq</th>
              <th className="mono">Time</th>
              <th>Event</th>
              <th>Task</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {events.map((event) => (
              <tr key={event.Seq}>
                <td className="mono">{event.Seq}</td>
                <td className="mono">{preciseTime(event.OccurredAt)}</td>
                <td className="mono">
                  <span
                    className={
                      TROUBLE.has(event.Type) ? "status status-warn" : "status"
                    }
                  >
                    {event.Type}
                  </span>
                </td>
                <td>{event.TaskName || "—"}</td>
                <td className="detail">{summarisePayload(event.Payload)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
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
