import Link from "next/link";
import { fetchRuns, fetchStats, fetchWorkers, mode } from "@/lib/api";
import { duration, short, time } from "@/lib/format";
import { ErrorState } from "./error-state";
import { Status } from "./status";

// The overview is the page someone leaves open while killing a worker, so it
// must reflect the database on every load rather than a cached render.
export const dynamic = "force-dynamic";

export default async function Overview() {
  let data;
  try {
    const [stats, runs, workers] = await Promise.all([
      fetchStats(),
      fetchRuns(10),
      fetchWorkers(),
    ]);
    data = { stats, runs, workers };
  } catch (error) {
    return <ErrorState error={error} />;
  }

  const { stats, runs, workers } = data;
  const live = workers.filter(
    (w) => w.Status === "HEALTHY" || w.Status === "SUSPECT",
  ).length;
  const running = stats.runs_by_status["RUNNING"] ?? 0;

  // The run a first-time visitor should open. A run that was disrupted and
  // finished anyway is the whole argument in one page; without one, the newest
  // run is still better than nothing.
  const headline = runs.find((r) => r.ScenarioName !== "") ?? runs[0];

  return (
    <>
      <h2>What this is</h2>
      <div className="explainer">
        <p>
          ReLab runs multi-step workflows across worker processes and records an
          append-only event history of what happened. It then breaks things on
          purpose — kills a worker mid-task, holds an upstream down, delivers the
          same task twice — and checks that the run recovers: the task comes
          back, the side effect is not repeated, and replaying the journal
          reconstructs the same result.
        </p>
        <p className="detail">
          It is a reliability testing and replay tool. It is not a Temporal
          replacement. This dashboard is a read-only view of what the journal
          recorded; everything it does is also available from{" "}
          <code>relab</code> on the command line.
        </p>
        {headline ? (
          <p>
            <Link href={`/runs/${headline.ID}`}>
              Open {headline.ScenarioName || headline.WorkflowName} →
            </Link>{" "}
            <span className="detail">
              {headline.ScenarioName
                ? "a run that was disrupted on purpose, with the whole recovery in its timeline"
                : "the most recent run"}
            </span>
          </p>
        ) : null}
      </div>

      <h2>Now</h2>
      <div className="counts">
        <Count label="Running" value={running} />
        <Count label="Queue depth" value={stats.queue_depth} />
        <Count label="Live workers" value={live} />
        <Count label="Dead letters" value={stats.dead_letters} />
      </div>
      <p className="detail note">
        A live worker is one that is HEALTHY or SUSPECT — still heartbeating, or
        doubted but not yet reclaimed. A dead letter is a task that exhausted
        its attempts.
        {mode() === "demo"
          ? " Nothing here is running: these are the counters as they stood when the recording was made."
          : null}
      </p>

      <h2>Runs by status</h2>
      {Object.keys(stats.runs_by_status).length === 0 ? (
        <p className="empty">
          No runs yet. Start one with{" "}
          <code>relab run examples/data-pipeline.yaml</code>.
        </p>
      ) : (
        <div className="counts">
          {Object.entries(stats.runs_by_status)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([status, count]) => (
              <Count key={status} label={status} value={count} />
            ))}
        </div>
      )}

      <h2>Latest runs</h2>
      {runs.length === 0 ? (
        <p className="empty">Nothing has run yet.</p>
      ) : (
        <div className="table-scroll">
          <table>
            <caption className="sr-only">
              The {runs.length} most recent runs, newest first.
            </caption>
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Workflow</th>
                <th scope="col">Run</th>
                <th scope="col">Scenario</th>
                <th scope="col" className="mono">
                  Started
                </th>
                <th scope="col" className="mono">
                  Duration
                </th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <tr key={run.ID}>
                  <td>
                    <Status value={run.Status} />
                  </td>
                  <th scope="row" className="row-name">
                    {run.WorkflowName}{" "}
                    <span className="detail">v{run.WorkflowVer}</span>
                  </th>
                  <td className="mono">
                    <Link href={`/runs/${run.ID}`}>{short(run.ID)}</Link>
                  </td>
                  <td>
                    {run.ScenarioName || <span className="detail">—</span>}
                  </td>
                  <td className="mono">{time(run.CreatedAt)}</td>
                  <td className="mono">
                    {duration(run.CreatedAt, run.CompletedAt) ?? "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function Count({ label, value }: { label: string; value: number }) {
  return (
    <div className="count">
      <div className="value">{value}</div>
      <div className="label">{label}</div>
    </div>
  );
}
